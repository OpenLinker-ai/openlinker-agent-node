package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func reviewFakeCLI(t *testing.T, body string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "provider")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nset -eu\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func TestCodexEphemeralReturnsOnlyCompletedAnswer(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	writeCodexRPCFixture(t, bin, "ephemeral")
	t.Setenv("TEST_LOG", filepath.Join(dir, "calls"))
	result, err := (CodexProvider{Config: ProviderConfig{Bin: bin, Workspace: dir, EnvAllowlist: []string{"TEST_LOG"}}}).Run(context.Background(), RunContext{Input: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output.(map[string]any)["summary"] != "final answer" {
		t.Fatal(result)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "calls.requests"))
	for _, flag := range []string{`"ephemeral":true`, "thread/start", "turn/start"} {
		if !strings.Contains(string(args), flag) {
			t.Fatalf("missing %s: %s", flag, args)
		}
	}
	for _, method := range []string{"item/agentMessage/delta", "item/reasoning/summaryTextDelta", "item/reasoning/summaryPartAdded", "item/reasoning/textDelta"} {
		if !strings.Contains(string(args), method) {
			t.Fatalf("unused delta was not opted out: %s", method)
		}
	}
}

func TestClaudeStreamsRedactedProgressBeforeExit(t *testing.T) {
	bin, dir := reviewFakeCLI(t, `cat >/dev/null
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"secret-id","name":"Bash","input":{"command":"private command"}}}}'
while [ ! -f release ]; do sleep 0.02; done
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"secret-id","name":"Bash","input":{"command":"private command"}}]}}'
printf '%s\n' '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"secret-id","content":"private output"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","result":"answer","session_id":"private-session"}'
`)
	progress := make(chan any, 8)
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		_, err := (ClaudeProvider{Config: ProviderConfig{Bin: bin, Workspace: dir}}).Run(ctx, RunContext{Emit: func(kind string, payload any) error {
			if kind == "run.status.changed" {
				progress <- payload
			}
			return nil
		}})
		done <- err
	}()
	var events []any
	select {
	case event := <-progress:
		events = append(events, event)
	case err := <-done:
		t.Fatalf("completed before progress: %v", err)
	case <-ctx.Done():
		t.Fatal("no progress during execution")
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for len(progress) > 0 {
		events = append(events, <-progress)
	}
	if len(events) != 2 {
		t.Fatalf("duplicate/missing tool progress: %#v", events)
	}
	raw, _ := json.Marshal(events)
	for _, private := range []string{"private", "secret-id", "Bash"} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("leaked %q: %s", private, raw)
		}
	}
}

func TestProvidersRecoverMissingSessionFromStructuredFailure(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			failure := `{"type":"turn.failed","error":{"message":"No rollout found for thread"}}`
			fresh := `{"type":"thread.started","thread_id":"fresh"}
{"type":"item.completed","item":{"type":"agent_message","text":"recovered"}}`
			if name == "claude" {
				failure = `{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["No conversation found with session ID old"]}`
				fresh = `{"type":"result","subtype":"success","result":"recovered","session_id":"fresh"}`
			}
			bin, dir := reviewFakeCLI(t, fmt.Sprintf(`printf '%%s\n' "$*" >> args
cat > prompt
case "$*" in
*resume*) printf '%%s\n' '%s'; exit 0 ;;
esac
printf '%%s\n' '%s'
`, failure, fresh))
			mode, old := "standard", "old"
			if name == "codex" {
				writeCodexRPCFixture(t, bin, "missing")
				t.Setenv("TEST_LOG", filepath.Join(dir, "rpc"))
				mode = "codex_rpc_v1:standard"
				old = fixtureThread
			}
			store := filepath.Join(dir, "sessions.json")
			if err := saveSessionForClientMode(store, name, dir, "conversation", old, mode, 1); err != nil {
				t.Fatal(err)
			}
			provider, err := NewProvider(ProviderConfig{Provider: name, Bin: bin, Workspace: dir, EnvAllowlist: []string{"TEST_LOG"}, Timeout: 5 * time.Second, SessionReuse: true, SessionStore: store})
			if err != nil {
				t.Fatal(err)
			}
			result, err := provider.Run(context.Background(), RunContext{RunID: "now", Conversation: &ConversationContext{SessionKey: "conversation", HistoryBeforeCurrent: []ConversationMessage{{Content: "rehydrated history"}}}})
			if err != nil {
				t.Fatal(err)
			}
			if result.Output.(map[string]any)[name+"_session_recovered"] != true {
				t.Fatal(result)
			}
			argsPath := filepath.Join(dir, "args")
			if name == "codex" {
				argsPath = filepath.Join(dir, "rpc.args")
			}
			args, _ := os.ReadFile(argsPath)
			if len(strings.Split(strings.TrimSpace(string(args)), "\n")) != 2 {
				t.Fatalf("unexpected retry count: %s", args)
			}
			promptPath := filepath.Join(dir, "prompt")
			if name == "codex" {
				promptPath = filepath.Join(dir, "rpc.prompt")
			}
			prompt, _ := os.ReadFile(promptPath)
			if !strings.Contains(string(prompt), "rehydrated history") {
				t.Fatalf("recovery lost history: %s", prompt)
			}
		})
	}
}

func TestResumedProvidersReceiveInterveningCoreHistory(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			result := `{"type":"thread.started","thread_id":"native"}
{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`
			if name == "claude" {
				result = `{"type":"result","result":"done","session_id":"native"}`
			}
			bin, dir := reviewFakeCLI(t, "cat > prompt\nprintf '%s\\n' '"+result+"'\n")
			if name == "codex" {
				writeCodexRPCFixture(t, bin, "standard")
				t.Setenv("TEST_LOG", filepath.Join(dir, "rpc"))
			}
			provider, err := NewProvider(ProviderConfig{Provider: name, Bin: bin, Workspace: dir, EnvAllowlist: []string{"TEST_LOG"}, Timeout: 5 * time.Second, SessionReuse: true, SessionStore: filepath.Join(dir, "sessions.json")})
			if err != nil {
				t.Fatal(err)
			}
			prior := ConversationMessage{RunID: "prior", Role: "user", Content: "already supplied"}
			run := RunContext{RunID: "own", Conversation: &ConversationContext{SessionKey: "conversation", HistoryBeforeCurrent: []ConversationMessage{prior}}}
			if _, err := provider.Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			// Exercise the real Provider.Run path, not only the identity helper:
			// this catches changes to Codex's RPC prefix or production save calls.
			mode := "standard"
			if name == "codex" {
				mode = "codex_rpc_v1:standard"
			}
			key := sessionStoreKey(name, dir, "conversation")
			record := readSessionStore(filepath.Join(dir, "sessions.json")).Sessions[key]
			if record.ClientMode != mode || record.ClientModeGeneration != 1 || record.SessionKeyHash != key[:24] {
				t.Fatalf("production session identity changed: %+v", record)
			}
			run.RunID = "next"
			run.Conversation.HistoryBeforeCurrent = append(run.Conversation.HistoryBeforeCurrent,
				ConversationMessage{RunID: "own", Role: "agent", Content: "own completed answer"},
				ConversationMessage{RunID: "other-provider", Role: "agent", Content: "new cross-provider result"})
			if _, err := provider.Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			promptPath := filepath.Join(dir, "prompt")
			if name == "codex" {
				promptPath = filepath.Join(dir, "rpc.prompt")
			}
			prompt, _ := os.ReadFile(promptPath)
			if !strings.Contains(string(prompt), "new cross-provider result") || strings.Contains(string(prompt), "already supplied") || !strings.Contains(string(prompt), "own completed answer") {
				t.Fatalf("incorrect history delta: %s", prompt)
			}
			// After restart the persisted cursor still works.
			provider, _ = NewProvider(ProviderConfig{Provider: name, Bin: bin, Workspace: dir, EnvAllowlist: []string{"TEST_LOG"}, Timeout: 5 * time.Second, SessionReuse: true, SessionStore: filepath.Join(dir, "sessions.json")})
			run.RunID = "third"
			if _, err := provider.Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			prompt, _ = os.ReadFile(promptPath)
			if strings.Contains(string(prompt), "new cross-provider result") {
				t.Fatalf("cursor not persisted: %s", prompt)
			}
		})
	}
}
