// Package codexturn runs one Codex app-server turn. It owns protocol ordering,
// bounded output, cancellation and process shutdown, not product tool/session
// policy or the SDK Runtime Worker.
package codexturn

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/codexrpc"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/provideroutput"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerprocess"
)

// ErrSessionMissing only represents the exact missing-rollout protocol errors.
// Callers decide whether to retry a fresh thread and how to persist its identity.
var ErrSessionMissing = errors.New("Codex native session no longer exists")

// ErrProcessCleanup must not be discarded merely because the request context
// was canceled. Cancellation acknowledgement is not process-cleanup evidence.
var ErrProcessCleanup = errors.New("Codex native process cleanup failed")

// ErrResponseMessageLimit is a semantic upstream stop, not a transport outage.
// The active turn is interrupted before returning; callers may retain its
// native session for an explicit follow-up, but must not replay this Run.
var ErrResponseMessageLimit = errors.New("Codex upstream response reached its message limit (max_messages)")

type Observer interface{ ObserveLine([]byte) }

// PreparedCommand is an unstarted, process-tree-owned command. Workspace is the
// path seen by app-server (which may be inside a container). Cleanup runs after
// process shutdown, including failed preparation/start and canceled requests.
// The factory must not assign stdio: Run owns its RPC pipes and bounded stderr.
type PreparedCommand struct {
	Command   *exec.Cmd
	Workspace string
	Cleanup   func()
	// Native factories opt into host-side descendant tracking. Container
	// factories retain their own process namespace and cleanup authority.
	TrackNativeDescendants bool
}

type Config struct {
	// Prepare uses a process context deliberately independent from the request:
	// cancellation first sends a scoped turn/interrupt, then kills the tree.
	Prepare                                     func(context.Context) (PreparedCommand, error)
	SessionID, Prompt, Sandbox, Model, Approval string
	Persistent                                  bool
	// UseConfiguredPermissions omits the legacy sandbox override on both start
	// and resume. The product must supply an enforced named permission profile.
	UseConfiguredPermissions bool
	// ThreadConfig supplies product overrides, optionally populated by BeforeThread.
	ThreadConfig map[string]json.RawMessage
	// BeforeThread runs after initialized and before thread/start or resume.
	// Any error prevents thread/turn creation and closes the app-server.
	BeforeThread func(context.Context, *codexrpc.Client) error
	Observer     Observer
	Emit         func(string, any) error
}

func Run(ctx context.Context, config Config) (threadID, final string, resultErr error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if config.Prepare == nil {
		return "", "", errors.New("Codex command factory is required")
	}
	processCtx, stop := context.WithCancel(context.Background())
	defer stop()
	prepared, err := config.Prepare(processCtx)
	if prepared.Cleanup != nil {
		defer prepared.Cleanup()
	}
	if err != nil {
		return "", "", err
	}
	command := prepared.Command
	if command == nil {
		return "", "", errors.New("Codex command factory returned no command")
	}
	workspace, sandbox := prepared.Workspace, config.Sandbox
	sessionID, prompt, persistent := config.SessionID, config.Prompt, config.Persistent
	emit := config.Emit
	stdin, err := command.StdinPipe()
	if err != nil {
		return "", "", err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return "", "", err
	}
	stderr := &provideroutput.Tail{}
	command.Stderr = stderr
	if err := ctx.Err(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return "", "", err
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return "", "", err
	}
	finishDescendants := func() error { return nil }
	if prepared.TrackNativeDescendants {
		finishDescendants, err = providerprocess.TrackDescendants(command.Process)
		if err != nil {
			stop()
			_ = stdin.Close()
			_ = stdout.Close()
			_ = command.Wait()
			return "", "", err
		}
	}
	client := codexrpc.New(stdin, stdout, func() { stop(); _ = stdin.Close(); _ = stdout.Close() })
	turnID := ""
	turnRequested := false
	defer func() {
		if (ctx.Err() != nil || errors.Is(resultErr, ErrResponseMessageLimit)) && turnRequested {
			interruptCodexRPC(client, threadID, turnID)
		}
		if resultErr == nil || ctx.Err() != nil {
			// EOF requests app-server shutdown and lets native rollout writes
			// finish. Drain StdoutPipe before Wait: Wait closes that pipe and
			// would race the RPC reader's final read with a successful exit.
			// A wedged shutdown still has a bounded process-tree kill.
			// interrupted is a protocol outcome, not proof that tool processes
			// have exited. Give canceled providers an EOF shutdown opportunity
			// as well; the identity-scoped native guard remains the fallback.
			grace := 2 * time.Second
			if ctx.Err() != nil {
				grace = 500 * time.Millisecond
			}
			_ = stdin.Close()
			select {
			case <-client.Done():
			case <-time.After(grace):
			}
		}
		client.Close()
		if err := finishDescendants(); err != nil {
			resultErr = errors.Join(resultErr, ErrProcessCleanup, err)
		}
		_ = command.Wait()
		if resultErr == nil {
			if err := client.Err(); err != nil && !errors.Is(err, io.EOF) {
				resultErr = err
			}
		}
	}()
	var initialized codexrpc.InitializeResponse
	if err := client.Call(ctx, "initialize", codexrpc.InitializeParams{
		ClientInfo: codexrpc.ClientInfo{Name: "openlinker_worker", Version: "1"},
		Capabilities: &codexrpc.InitializeCapabilities{OptOutNotificationMethods: []string{
			"item/agentMessage/delta", "item/reasoning/summaryTextDelta",
			"item/reasoning/summaryPartAdded", "item/reasoning/textDelta",
		}},
	}, &initialized); err != nil {
		return "", "", err
	}
	if err := client.Notify(ctx, "initialized", struct{}{}); err != nil {
		return "", "", err
	}
	if config.BeforeThread != nil {
		if err := config.BeforeThread(ctx, client); err != nil {
			return "", "", err
		}
	}
	approval := json.RawMessage(`"never"`)
	if config.Approval != "" {
		raw, _ := json.Marshal(config.Approval)
		approval = raw
	}
	mode := codexrpc.SandboxMode(sandbox)
	modeOverride := &mode
	if config.UseConfiguredPermissions {
		modeOverride = nil
	}
	model := strings.TrimSpace(config.Model)
	var modelOverride *string
	if model != "" {
		modelOverride = &model
	}
	if sessionID != "" {
		var resumed codexrpc.ThreadResumeResponse
		err := client.Call(ctx, "thread/resume", codexrpc.ThreadResumeParams{Config: config.ThreadConfig, ThreadID: sessionID, Cwd: &workspace, Sandbox: modeOverride, ApprovalPolicy: &approval, Model: modelOverride, ExcludeTurns: boolPtr(true)}, &resumed)
		if err != nil {
			var rpcErr *codexrpc.Error
			if errors.As(err, &rpcErr) && rpcErr.Code == -32600 && (rpcErr.Message == "no rollout found for thread id "+sessionID || rpcErr.Message == "thread not found: "+sessionID) {
				return "", "", ErrSessionMissing
			}
			return "", "", err
		}
		threadID = resumed.Thread.ID
		if threadID != sessionID {
			return "", "", errors.New("Codex resumed a different thread")
		}
	} else {
		var started codexrpc.ThreadStartResponse
		if err := client.Call(ctx, "thread/start", codexrpc.ThreadStartParams{Config: config.ThreadConfig, Cwd: &workspace, Sandbox: modeOverride, ApprovalPolicy: &approval, Model: modelOverride, Ephemeral: boolPtr(!persistent)}, &started); err != nil {
			return "", "", err
		}
		threadID = started.Thread.ID
	}
	if strings.TrimSpace(threadID) == "" || len(threadID) > 256 {
		return "", "", errors.New("Codex returned an invalid thread ID")
	}
	turnRequested = true
	var turn codexrpc.TurnStartResponse
	if err := client.Call(ctx, "turn/start", codexrpc.TurnStartParams{ThreadID: threadID, Input: []codexrpc.UserInput{{Type: "text", Text: &prompt}}}, &turn); err != nil {
		return threadID, "", err
	}
	turnID = turn.Turn.ID
	if strings.TrimSpace(turnID) == "" || len(turnID) > 256 {
		return threadID, "", errors.New("Codex returned an invalid turn ID")
	}
	observe := config.Observer
	handle := func(event codexrpc.Message) (bool, error) {
		switch event.Method {
		case "item/started", "item/completed":
			// Both item notification shapes share threadId/turnId/item. Decode exact
			// schema fields; arbitrary nested tool output cannot select a session.
			var item codexrpc.ItemCompletedNotification
			if json.Unmarshal(event.Params, &item) != nil {
				return false, errors.New("Codex emitted an invalid item event")
			}
			if item.ThreadID != threadID || item.TurnID != turnID {
				return false, nil
			}
			if event.Method == "item/completed" {
				if text := codexRPCFinal(item.Item); text != "" {
					final = text
				}
			}
			var safeSource map[string]any
			raw, _ := json.Marshal(item.Item)
			_ = json.Unmarshal(raw, &safeSource)
			switch safeSource["type"] {
			case "commandExecution":
				safeSource["type"] = "command_execution"
			case "mcpToolCall":
				safeSource["type"] = "mcp_tool_call"
			case "webSearch":
				safeSource["type"] = "web_search"
			}
			raw, _ = json.Marshal(map[string]any{"type": strings.ReplaceAll(event.Method, "/", "."), "item": safeSource})
			if observe != nil {
				observe.ObserveLine(raw)
			}
		case "error":
			var failure codexrpc.ErrorNotification
			if json.Unmarshal(event.Params, &failure) != nil {
				return false, errors.New("Codex emitted an invalid error notification")
			}
			if failure.ThreadID != threadID || failure.TurnID != turnID {
				return false, nil
			}
			// The provider supplies this reason; it is not a local item-count
			// heuristic. Never include raw upstream text in results or events.
			const incomplete = "Incomplete response returned, reason: max_messages"
			if strings.HasSuffix(strings.TrimSpace(failure.Error.Message), incomplete) ||
				(failure.Error.AdditionalDetails != nil && strings.HasSuffix(strings.TrimSpace(*failure.Error.AdditionalDetails), incomplete)) {
				if emit != nil {
					_ = emit("run.status.changed", map[string]any{
						"provider": "codex", "status": "provider_failed", "phase": "failed",
						"provider_error_kind": "incomplete_response", "provider_error_reason": "max_messages",
					})
				}
				final = ""
				return true, ErrResponseMessageLimit
			}
			if failure.WillRetry && emit != nil {
				_ = emit("run.status.changed", map[string]any{"provider": "codex", "status": "provider_retrying", "phase": "retrying"})
			}
		case "turn/completed":
			var completed codexrpc.TurnCompletedNotification
			if json.Unmarshal(event.Params, &completed) != nil {
				return false, errors.New("Codex emitted an invalid completed turn")
			}
			if completed.ThreadID != threadID || completed.Turn.ID != turnID {
				return false, nil
			}
			switch completed.Turn.Status {
			case "completed":
				for _, item := range completed.Turn.Items {
					if text := codexRPCFinal(item); text != "" {
						final = text
					}
				}
				if final == "" {
					return true, errors.New("Codex completed without a final message")
				}
				return true, nil
			case "interrupted":
				return true, context.Canceled
			case "failed":
				return true, errors.New("Codex reported a failed turn")
			default:
				return true, errors.New("Codex completed with an invalid turn status")
			}
		}
		return false, nil
	}
	for {
		// Consume already received terminal events before acting on a trailing EOF.
		select {
		case event := <-client.Events():
			done, err := handle(event)
			if done || err != nil {
				return threadID, final, err
			}
			continue
		default:
		}
		select {
		case <-ctx.Done():
			return threadID, "", ctx.Err()
		case event := <-client.Events():
			done, err := handle(event)
			if done || err != nil {
				return threadID, final, err
			}
		case <-client.Done():
			select {
			case event := <-client.Events():
				done, err := handle(event)
				if done || err != nil {
					return threadID, final, err
				}
			default:
				return threadID, "", client.Err()
			}
		}
	}
}

func boolPtr(value bool) *bool { return &value }
func codexRPCFinal(item codexrpc.ThreadItem) string {
	if item.Type != "agentMessage" || item.Text == nil {
		return ""
	}
	if item.Phase != nil && string(*item.Phase) != "final_answer" {
		return ""
	}
	return strings.TrimSpace(*item.Text)
}

func interruptCodexRPC(client *codexrpc.Client, threadID, turnID string) {
	if threadID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	// turn/started may arrive before the canceled turn/start response. Learn the
	// ID from that scoped notification, then interrupt only that active turn.
	for turnID == "" {
		select {
		case <-ctx.Done():
			return
		case <-client.Done():
			return
		case event := <-client.Events():
			if event.Method == "turn/started" {
				var started codexrpc.TurnStartedNotification
				if json.Unmarshal(event.Params, &started) == nil && started.ThreadID == threadID {
					turnID = started.Turn.ID
				}
			}
		}
	}
	var response codexrpc.TurnInterruptResponse
	if client.Call(ctx, "turn/interrupt", codexrpc.TurnInterruptParams{ThreadID: threadID, TurnID: turnID}, &response) != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-client.Done():
			return
		case event := <-client.Events():
			if event.Method == "turn/completed" {
				var completed codexrpc.TurnCompletedNotification
				if json.Unmarshal(event.Params, &completed) == nil && completed.ThreadID == threadID && completed.Turn.ID == turnID {
					return
				}
			}
		}
	}
}
