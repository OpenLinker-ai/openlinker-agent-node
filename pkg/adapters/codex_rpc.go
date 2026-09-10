package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/codexhome"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/codexrpc"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerstream"
)

var errCodexSessionMissing = errors.New("Codex native session no longer exists")

// One process per invocation, owned by this Attempt. Credentials and static
// tool configuration remain launch-time overrides so the hardened launcher's
// credential proxy can still rewrite its endpoint before dropping privileges.
func runCodexRPC(ctx context.Context, bin, workspace, sandbox, sessionID, prompt string, persistent bool, config ProviderConfig, emit func(string, any) error) (threadID, final string, resultErr error) {
	// Resolve cwd once for both the protocol and the explicit trust key. A
	// relative cwd must not be resolved again against the child's new cwd.
	absoluteWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", "", fmt.Errorf("resolve Codex workspace: %w", err)
	}
	resolvedWorkspace, err := filepath.EvalSymlinks(absoluteWorkspace)
	if err != nil {
		return "", "", fmt.Errorf("resolve Codex workspace: %w", err)
	}
	workspace = resolvedWorkspace
	processCtx, stop := context.WithCancel(context.Background())
	defer stop()
	environment := config.Env
	if environment == nil {
		environment = os.Environ()
	}
	command := exec.CommandContext(processCtx, bin, codexAppServerArguments(config, workspace, sandbox)...)
	configureProviderProcess(command)
	command.Dir = workspace
	launcher := false
	for _, entry := range environment {
		if entry == codexhome.LauncherEnvironment+"=1" {
			launcher = true
		}
	}
	allowed := append([]string{"CODEX_API_KEY", "CODEX_HOME", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "CODEX_CA_CERTIFICATE", "SSL_CERT_FILE"}, config.EnvAllowlist...)
	environment = append(sanitizedEnvironment(environment, allowed), "LC_ALL=C", "LANG=C")
	if launcher {
		environment = append(environment, codexhome.PrepareEnvironment+"=1")
	} else {
		var cleanup func()
		var err error
		environment, cleanup, err = codexhome.Prepare(environment)
		if err != nil {
			return "", "", err
		}
		defer cleanup()
	}
	command.Env = environment
	stdin, err := command.StdinPipe()
	if err != nil {
		return "", "", err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return "", "", err
	}
	stderr := &outputTail{}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return "", "", err
	}
	client := codexrpc.New(stdin, stdout, func() { stop(); _ = stdin.Close(); _ = stdout.Close() })
	turnID := ""
	turnRequested := false
	defer func() {
		if ctx.Err() != nil && turnRequested {
			interruptCodexRPC(client, threadID, turnID)
		}
		if resultErr == nil {
			// EOF requests app-server shutdown and lets native rollout writes
			// finish. Drain StdoutPipe before Wait: Wait closes that pipe and
			// would race the RPC reader's final read with a successful exit.
			// A wedged shutdown still has a bounded process-tree kill.
			_ = stdin.Close()
			select {
			case <-client.Done():
			case <-time.After(2 * time.Second):
			}
		}
		client.Close()
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
	approval := json.RawMessage(`"never"`)
	if config.CodexApproval != "" {
		raw, _ := json.Marshal(config.CodexApproval)
		approval = raw
	}
	mode := codexrpc.SandboxMode(sandbox)
	model := strings.TrimSpace(config.Model)
	var modelOverride *string
	if model != "" {
		modelOverride = &model
	}
	if sessionID != "" {
		var resumed codexrpc.ThreadResumeResponse
		err := client.Call(ctx, "thread/resume", codexrpc.ThreadResumeParams{ThreadID: sessionID, Cwd: &workspace, Sandbox: &mode, ApprovalPolicy: &approval, Model: modelOverride, ExcludeTurns: boolPtr(true)}, &resumed)
		if err != nil {
			var rpcErr *codexrpc.Error
			if errors.As(err, &rpcErr) && rpcErr.Code == -32600 && (rpcErr.Message == "no rollout found for thread id "+sessionID || rpcErr.Message == "thread not found: "+sessionID) {
				return "", "", errCodexSessionMissing
			}
			return "", "", err
		}
		threadID = resumed.Thread.ID
		if threadID != sessionID {
			return "", "", errors.New("Codex resumed a different thread")
		}
	} else {
		var started codexrpc.ThreadStartResponse
		if err := client.Call(ctx, "thread/start", codexrpc.ThreadStartParams{Cwd: &workspace, Sandbox: &mode, ApprovalPolicy: &approval, Model: modelOverride, Ephemeral: boolPtr(!persistent)}, &started); err != nil {
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
	observe := providerstream.NewCodexObserver(emit, nil)
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
			observe.ObserveLine(raw)
		case "error":
			var failure codexrpc.ErrorNotification
			if json.Unmarshal(event.Params, &failure) != nil {
				return false, errors.New("Codex emitted an invalid error notification")
			}
			if failure.ThreadID == threadID && failure.TurnID == turnID && failure.WillRetry && emit != nil {
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

func codexAppServerArguments(config ProviderConfig, workspace, sandbox string) []string {
	args := codexLaunchConfiguration(config, sandbox)
	// Do not hydrate account-installed plugins or start remote-control/hooks
	// from a native login. Deep plugin installation belongs to Plugin.
	args = append(args, "--disable", "remote_plugin", "--disable", "remote_control", "--disable", "hooks", "--disable", "apps")
	args = append(args, "--disable", "plugins")
	args = append(args, "-c", `cli_auth_credentials_store="file"`, "-c", "projects={"+jsonString(workspace)+"={trust_level=\"untrusted\"}}")
	return append(args, "app-server", "--listen", "stdio://")
}

func jsonString(value string) string { raw, _ := json.Marshal(value); return string(raw) }
