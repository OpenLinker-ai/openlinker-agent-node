package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/codexrpc"
)

const fixtureThread = "11111111-1111-4111-8111-111111111111"
const fixtureTurn = "turn-1"

func writeCodexRPCFixture(t *testing.T, path, scenario string) {
	t.Helper()
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "native-home"))
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexport OPENLINKER_CODEX_RPC_FIXTURE='" + scenario + "'\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run=TestCodexRPCFixtureProcess -- \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

type rpcFixture struct {
	decoder  *json.Decoder
	encoder  *json.Encoder
	mode     string
	scenario string
	log      string
}

func startRPCFixture(scenario string) *rpcFixture {
	f := &rpcFixture{decoder: json.NewDecoder(os.Stdin), encoder: json.NewEncoder(os.Stdout), mode: "new", scenario: scenario, log: os.Getenv("TEST_LOG")}
	f.logFile(".args", strings.Join(os.Args, " ")+"\n", true)
	f.logFile(".proxy", os.Getenv("ALL_PROXY"), false)
	for {
		var message codexrpc.Message
		if f.decoder.Decode(&message) != nil {
			os.Exit(2)
		}
		f.logFile(".requests", string(message.Params)+" "+message.Method+"\n", true)
		switch message.Method {
		case "initialize":
			f.reply(message, map[string]any{"userAgent": "fixture"})
		case "initialized":
		case "thread/start", "thread/resume":
			if message.Method == "thread/resume" {
				f.mode = "resume"
				if scenario == "missing" || scenario == "bad-resume" {
					diagnostic := "no rollout found for thread id " + fixtureThread
					if scenario == "bad-resume" {
						diagnostic = "invalid sandbox"
					}
					_ = f.encoder.Encode(codexrpc.Message{ID: message.ID, Error: &codexrpc.Error{Code: -32600, Message: diagnostic}})
					continue
				}
			}
			f.reply(message, map[string]any{"thread": map[string]any{"id": fixtureThread}})
		case "turn/start":
			var params codexrpc.TurnStartParams
			if json.Unmarshal(message.Params, &params) != nil || len(params.Input) != 1 || params.Input[0].Text == nil {
				os.Exit(2)
			}
			prompt := *params.Input[0].Text
			f.logFile(".prompt", prompt, false)
			f.logFile("."+f.mode+".prompt", prompt, false)
			f.event("turn/started", map[string]any{"threadId": fixtureThread, "turn": map[string]any{"id": fixtureTurn, "status": "inProgress", "items": []any{}}})
			if scenario != "cancel-before-reply" {
				f.reply(message, map[string]any{"turn": map[string]any{"id": fixtureTurn, "status": "inProgress", "items": []any{}}})
			}
			f.logFile(".started", "yes", false)
			return f
		default:
			os.Exit(3)
		}
	}
}
func (f *rpcFixture) reply(request codexrpc.Message, value any) {
	raw, _ := json.Marshal(value)
	_ = f.encoder.Encode(codexrpc.Message{ID: request.ID, Result: raw})
}
func (f *rpcFixture) event(method string, value any) {
	raw, _ := json.Marshal(value)
	_ = f.encoder.Encode(codexrpc.Message{Method: method, Params: raw})
}
func (f *rpcFixture) logFile(suffix, text string, appendFile bool) {
	if f.log == "" {
		return
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if appendFile {
		flags = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	}
	file, err := os.OpenFile(f.log+suffix, flags, 0o600)
	if err == nil {
		_, _ = file.WriteString(text)
		_ = file.Close()
	}
}
func (f *rpcFixture) item(method string, item map[string]any) {
	f.event(method, map[string]any{"threadId": fixtureThread, "turnId": fixtureTurn, "item": item})
}
func (f *rpcFixture) finish(answer string) {
	f.item("item/completed", map[string]any{"id": "final", "type": "agentMessage", "phase": "final_answer", "text": answer})
	status := "completed"
	if f.scenario == "failed" {
		status = "failed"
	}
	f.event("turn/completed", map[string]any{"threadId": fixtureThread, "turn": map[string]any{"id": fixtureTurn, "status": status, "items": []any{}}})
	io.Copy(io.Discard, os.Stdin)
	if f.scenario == "shutdown-drain" {
		// A final large notification arrives only after the shutdown request.
		// The reader must finish consuming it before Wait closes StdoutPipe.
		f.event("fixture/shutdown", map[string]any{"padding": strings.Repeat("x", 512<<10)})
	}
	if f.scenario == "shutdown-malformed" {
		_, _ = os.Stdout.WriteString("not-json\n")
	}
	if f.scenario == "shutdown-hang" {
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}

func TestCodexRPCDrainsShutdownBeforeWaiting(t *testing.T) {
	for _, scenario := range []string{"shutdown-drain", "shutdown-malformed", "shutdown-hang"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "codex")
			writeCodexRPCFixture(t, bin, scenario)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			start := time.Now()
			_, answer, err := runCodexRPC(ctx, bin, dir, "read-only", "", "test shutdown", false, ProviderConfig{}, nil)
			if scenario == "shutdown-malformed" {
				if err == nil || !strings.Contains(err.Error(), "malformed JSON") {
					t.Fatalf("shutdown protocol error was lost: %v", err)
				}
			} else if err != nil || answer != "provider answer" {
				t.Fatalf("completed turn failed during shutdown: answer=%q err=%v", answer, err)
			}
			if time.Since(start) > 4*time.Second {
				t.Fatal("shutdown exceeded its process-tree kill bound")
			}
		})
	}
}
func TestCodexRPCFixtureProcess(t *testing.T) {
	scenario := os.Getenv("OPENLINKER_CODEX_RPC_FIXTURE")
	if scenario == "" {
		return
	}
	f := startRPCFixture(scenario)
	if strings.HasPrefix(scenario, "cancel") {
		if scenario == "cancel-ignore" {
			io.Copy(io.Discard, os.Stdin)
			os.Exit(0)
		}
		for {
			var message codexrpc.Message
			if f.decoder.Decode(&message) != nil {
				os.Exit(0)
			}
			if message.Method == "turn/interrupt" {
				f.logFile(".interrupt", string(message.Params), false)
				f.reply(message, map[string]any{})
				f.event("turn/completed", map[string]any{"threadId": fixtureThread, "turn": map[string]any{"id": fixtureTurn, "status": "interrupted", "items": []any{}}})
			}
		}
	}
	if scenario == "malformed" {
		_, _ = os.Stdout.WriteString("not-json\n")
		io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	if scenario == "overflow" {
		_, _ = os.Stdout.WriteString(strings.Repeat("x", codexrpc.MaxOutputBytes+1))
		io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	f.item("item/started", map[string]any{"id": "tool", "type": "webSearch", "query": "private query must not be emitted", "status": "inProgress"})
	if scenario == "progress" {
		for {
			if _, err := os.Stat(f.log + ".release"); err == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	f.item("item/completed", map[string]any{"id": "tool", "type": "webSearch", "query": "private query must not be emitted", "status": "completed"})
	// Different scopes, nested output, commentary and incomplete messages cannot
	// replace the completed final agent answer.
	f.event("item/completed", map[string]any{"threadId": "wrong", "turnId": fixtureTurn, "item": map[string]any{"type": "agentMessage", "text": "wrong thread"}})
	f.item("item/completed", map[string]any{"type": "mcpToolCall", "result": map[string]any{"type": "agentMessage", "text": "untrusted tool text"}})
	f.item("item/started", map[string]any{"type": "agentMessage", "text": "unfinished"})
	f.item("item/completed", map[string]any{"type": "agentMessage", "phase": "commentary", "text": "commentary"})
	answer := "provider answer"
	if scenario == "ephemeral" {
		answer = "final answer"
	}
	f.finish(answer)
}

func TestCodexRPCCancellationInterruptsScopedTurn(t *testing.T) {
	for _, scenario := range []string{"cancel", "cancel-before-reply", "cancel-ignore"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "codex")
			writeCodexRPCFixture(t, bin, scenario)
			log := filepath.Join(dir, "calls")
			config := ProviderConfig{Bin: bin, Workspace: dir, Env: append(os.Environ(), "TEST_LOG="+log), EnvAllowlist: []string{"TEST_LOG"}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := (CodexProvider{Config: config}).Run(ctx, RunContext{Input: "task"}); done <- err }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(log + ".started"); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("turn not started")
				}
				time.Sleep(5 * time.Millisecond)
			}
			start := time.Now()
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("process tree was not stopped")
			}
			if time.Since(start) > 3*time.Second {
				t.Fatal("unbounded interrupt")
			}
			if scenario != "cancel-ignore" {
				raw, err := os.ReadFile(log + ".interrupt")
				if err != nil || !strings.Contains(string(raw), fixtureTurn) || !strings.Contains(string(raw), fixtureThread) {
					t.Fatalf("interrupt scope: %s %v", raw, err)
				}
			}
		})
	}
}

func TestCodexRPCFailureDoesNotPersistSession(t *testing.T) {
	for _, scenario := range []string{"failed", "malformed", "overflow"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "codex")
			writeCodexRPCFixture(t, bin, scenario)
			config := ProviderConfig{Bin: bin, Workspace: dir, SessionReuse: true, SessionStore: filepath.Join(dir, "sessions"), Timeout: 5 * time.Second}
			_, err := (CodexProvider{Config: config}).Run(context.Background(), RunContext{Conversation: &ConversationContext{SessionKey: "conversation"}})
			if err == nil {
				t.Fatal("bad provider succeeded")
			}
			if _, err := os.Stat(config.SessionStore); !os.IsNotExist(err) {
				t.Fatal("failed turn persisted a session")
			}
		})
	}
}

func TestCodexRPCDoesNotRecoverUnrelatedResumeErrors(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	writeCodexRPCFixture(t, bin, "bad-resume")
	log := filepath.Join(dir, "calls")
	store := filepath.Join(dir, "sessions.json")
	if err := saveSessionForClientMode(store, "codex", dir, "conversation", fixtureThread, "codex_rpc_v1:standard", 1); err != nil {
		t.Fatal(err)
	}
	config := ProviderConfig{Bin: bin, Workspace: dir, SessionReuse: true, SessionStore: store, Timeout: 5 * time.Second, Env: append(os.Environ(), "TEST_LOG="+log), EnvAllowlist: []string{"TEST_LOG"}}
	_, err := (CodexProvider{Config: config}).Run(context.Background(), RunContext{Conversation: &ConversationContext{SessionKey: "conversation"}})
	if err == nil {
		t.Fatal("unrelated -32600 error was recovered")
	}
	raw, _ := os.ReadFile(log + ".requests")
	if strings.Count(string(raw), "thread/resume") != 1 || strings.Contains(string(raw), "thread/start") || strings.Contains(string(raw), "turn/start") {
		t.Fatal("invalid request caused a replacement turn")
	}
}

func TestCodexRPCResolvesRelativeWorkspaceAndDottedTrustKey(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "workspace.with.dots")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "codex")
	writeCodexRPCFixture(t, bin, "standard")
	t.Chdir(dir)
	log := filepath.Join(dir, "calls")
	config := ProviderConfig{Bin: bin, Workspace: ".", Timeout: 5 * time.Second, Env: append(os.Environ(), "TEST_LOG="+log), EnvAllowlist: []string{"TEST_LOG"}}
	if _, err := (CodexProvider{Config: config}).Run(context.Background(), RunContext{Input: "test relative cwd"}); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(log + ".requests")
	if !strings.Contains(string(raw), `"cwd":`+jsonString(canonical)) {
		t.Fatal("RPC used a relative or differently resolved cwd")
	}
	args, _ := os.ReadFile(log + ".args")
	if !strings.Contains(string(args), "projects={"+jsonString(canonical)+"={trust_level=\"untrusted\"}}") {
		t.Fatal("dotted workspace was not a literal trust key")
	}
}
