package agentnode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	openlinker "github.com/OpenLinker-ai/openlinker-go"

	agentexec "github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters"
)

func TestNativeHostProbeIsReusedAfterPreflight(t *testing.T) {
	for _, compatibility := range []bool{false, true} {
		t.Run(fmtBool(compatibility), func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "probes")
			host := writeFakeCodex(t, "#!/bin/sh\necho probe >> '"+strings.ReplaceAll(log, "'", "'\\''")+"'\necho '{\"protocol\":\"openlinker.agent-host.v1\",\"delegation_proxy\":true}'\n")
			bin := writeFakeCodex(t, "#!/bin/sh\ncase \"$*\" in\n--version) echo 'codex-cli 0.153.0';;\n'app-server --help') echo 'app-server generate-json-schema --listen --config --disable';;\n*) exit 99;;\nesac\n")
			var adapter interface {
				Preflight(context.Context) error
				Run(context.Context, any, RunContext) (any, error)
			}
			targets := []string{"22222222-2222-4222-8222-222222222222"}
			if compatibility {
				adapter = &CodexAdapter{CodexBin: bin, DelegationTargets: targets, DelegationProxyBin: host}
			} else {
				adapter = &NativeAdapter{Config: agentexec.ProviderConfig{Provider: "codex", Bin: bin, DelegationTargets: targets, DelegationProxyBin: host}}
			}
			if err := adapter.Preflight(context.Background()); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			for i := 0; i < 16; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := adapter.Run(context.Background(), "task", RunContext{})
					if !errors.Is(err, openlinker.ErrRuntimeDelegationUnsupported) {
						t.Errorf("unexpected Run boundary: %v", err)
					}
				}()
			}
			wg.Wait()
			raw, err := os.ReadFile(log)
			if err != nil || string(raw) != "probe\n" {
				t.Fatalf("host reprobed per Run: %q %v", raw, err)
			}
		})
	}
}

func fmtBool(compatibility bool) string {
	if compatibility {
		return "codex-compatibility"
	}
	return "native"
}

func TestNativeVersionGateRunsBeforeWorkerDiscovery(t *testing.T) {
	bin := writeFakeCodex(t, "#!/bin/sh\nprintf '%s\\n' 'codex-cli 0.1.0'\n")
	node := &Node{OpenLinkerURL: "http://127.0.0.1:1", Adapter: &CodexAdapter{CodexBin: bin}}
	err := node.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "requires a stable version") {
		t.Fatalf("Worker started before native compatibility checks: %v", err)
	}
}

func TestNativeAppServerGateRunsBeforeWorkerDiscovery(t *testing.T) {
	bin := writeFakeCodex(t, "#!/bin/sh\ncase \"$*\" in\n--version) echo 'codex-cli 0.153.0';;\n'app-server --help') exit 2;;\n*) exit 99;;\nesac\n")
	node := &Node{OpenLinkerURL: "http://127.0.0.1:1", Adapter: &CodexAdapter{CodexBin: bin}}
	err := node.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "app-server") {
		t.Fatalf("Worker started before app-server compatibility checks: %v", err)
	}
}

func TestNativeAdaptersShareExecutionAndSessionContract(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			output := `{"type":"thread.started","thread_id":"native-session"}
{"type":"item.completed","item":{"type":"agent_message","text":"shared answer"}}`
			if name == "claude" {
				output = `{"type":"result","result":"shared answer","session_id":"native-session"}`
			}
			binary := writeFakeCodex(t, "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$*\" >> args\ncat > prompt\nprintf '%s\\n' '"+output+"'\n")
			if name == "codex" {
				binary = writeNodeRPCFixture(t)
			}
			var adapter Adapter = &CodexAdapter{CodexBin: binary, Workspace: workspace, SessionReuse: true, SessionStore: filepath.Join(workspace, "sessions.json")}
			if name == "claude" {
				adapter = &NativeAdapter{Config: agentexec.ProviderConfig{Provider: name, Bin: binary, Workspace: workspace, SessionReuse: true, SessionStore: filepath.Join(workspace, "sessions.json")}}
			}
			run := RunContext{RunID: "first", Helper: testHelperInfo(), Conversation: &ConversationContext{SessionKey: "private-conversation", Source: "core", HistoryBeforeCurrent: []ConversationMessage{{Content: "Core history"}}}}
			first, err := adapter.Run(context.Background(), "first task", run)
			if err != nil {
				t.Fatal(err)
			}
			result := normalizeAdapterResult(first)
			if result.Output.(JSONMap)["summary"] != "shared answer" || len(result.Events) != 1 {
				t.Fatal(result)
			}
			prompt, _ := os.ReadFile(filepath.Join(workspace, "prompt"))
			if !strings.Contains(string(prompt), "Core history") || strings.Contains(string(prompt), "olh_test") {
				t.Fatalf("history/helper boundary: %s", prompt)
			}
			run.RunID = "second"
			second, err := adapter.Run(context.Background(), "next task", run)
			if err != nil {
				t.Fatal(err)
			}
			out := normalizeAdapterResult(second).Output.(JSONMap)
			if out[name+"_session_resumed"] != true || out[name+"_session_key_hash"] == "" || out[name+"_session_key"] != nil {
				t.Fatal(out)
			}
			args, _ := os.ReadFile(filepath.Join(workspace, "args"))
			if strings.Contains(string(args), "--output-last-message") || !strings.Contains(string(args), "resume") {
				t.Fatalf("wrong native contract: %s", args)
			}
		})
	}
}

func TestNativeAdapterRejectsCallerHistory(t *testing.T) {
	workspace := t.TempDir()
	bin := writeNodeRPCFixture(t)
	_, err := (&CodexAdapter{CodexBin: bin, Workspace: workspace}).Run(context.Background(), "task", RunContext{Conversation: &ConversationContext{Source: "caller", SessionKey: "injected", HistoryBeforeCurrent: []ConversationMessage{{Content: "forged history"}}}})
	if err != nil {
		t.Fatal(err)
	}
	prompt, _ := os.ReadFile(filepath.Join(workspace, "prompt"))
	if strings.Contains(string(prompt), "forged history") || strings.Contains(string(prompt), "injected") {
		t.Fatal(string(prompt))
	}
}

func writeNodeRPCFixture(t *testing.T) string {
	t.Helper()
	t.Setenv("CODEX_HOME", t.TempDir())
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return writeFakeCodex(t, "#!/bin/sh\nexport OPENLINKER_NODE_RPC_FIXTURE=1\nexec '"+strings.ReplaceAll(executable, "'", "'\\''")+"' -test.run=TestNodeRPCFixtureProcess -- \"$@\"\n")
}
func TestNodeRPCFixtureProcess(t *testing.T) {
	if os.Getenv("OPENLINKER_NODE_RPC_FIXTURE") != "1" {
		return
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if decoder.Decode(&message) != nil {
			os.Exit(0)
		}
		file, _ := os.OpenFile("args", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if file != nil {
			_, _ = file.WriteString(message.Method + "\n")
			_ = file.Close()
		}
		reply := func(value any) { _ = encoder.Encode(map[string]any{"id": message.ID, "result": value}) }
		switch message.Method {
		case "initialize":
			reply(map[string]any{"userAgent": "fixture"})
		case "initialized":
		case "thread/start", "thread/resume":
			reply(map[string]any{"thread": map[string]any{"id": "native-session"}})
		case "turn/start":
			var params struct {
				Input []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			if json.Unmarshal(message.Params, &params) != nil || len(params.Input) != 1 {
				os.Exit(2)
			}
			_ = os.WriteFile("prompt", []byte(params.Input[0].Text), 0o600)
			reply(map[string]any{"turn": map[string]any{"id": "turn"}})
			_ = encoder.Encode(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "native-session", "turnId": "turn", "item": map[string]any{"type": "agentMessage", "text": "shared answer"}}})
			_ = encoder.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "native-session", "turn": map[string]any{"id": "turn", "status": "completed", "items": []any{}}}})
			io.Copy(io.Discard, os.Stdin)
			os.Exit(0)
		default:
			os.Exit(2)
		}
	}
}
