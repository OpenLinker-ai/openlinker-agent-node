package agentnode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const codexWebSearchEnv = "OPENLINKER_AGENT_NODE_CODEX_WEB_SEARCH"

func TestCodexWebSearchEnvironmentValues(t *testing.T) {
	for _, isolation := range []string{"off", "native"} {
		for _, test := range []struct {
			value string
			want  bool
		}{
			{"", true}, {"false", false}, {"0", false}, {"no", false}, {"off", false},
			{"true", true}, {"1", true}, {"yes", true}, {"on", true},
			{" TRUE ", true}, {"\tFalse\n", false}, {"YeS", true}, {"OFF", false},
		} {
			t.Run(isolation+"/"+test.value, func(t *testing.T) {
				env := Env{
					"OPENLINKER_AGENT_NODE_ADAPTER": "codex", codexWebSearchEnv: test.value,
					"OPENLINKER_AGENT_NODE_SESSION_ISOLATION": isolation,
				}
				if isolation == "native" {
					if os.Geteuid() == 0 || runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
						t.Skip("native isolation requires non-root macOS/Linux")
					}
					env["OPENLINKER_URL"] = "https://core.example/"
					env["OPENLINKER_AGENT_NODE_SESSION_ROOT"] = filepath.Join(t.TempDir(), "sessions")
				}
				node, err := NewFromEnvMap(env)
				if err != nil {
					t.Fatal(err)
				}
				config := node.Adapter.(*CodexAdapter).native().Config
				if config.WebSearch != test.want {
					t.Fatalf("provider WebSearch=%t, want %t", config.WebSearch, test.want)
				}
				if config.Sandbox != "read-only" || config.CodexApproval != "never" ||
					config.SessionReuse != (isolation == "native") || len(config.DelegationTargets) != 0 ||
					config.SessionIsolation.Enabled() != (isolation == "native") {
					t.Fatal("web search changed unrelated security or session policy")
				}
				if node.started || node.worker != nil || node.Helper != nil || node.PublicA2A != nil {
					t.Fatal("configuration unexpectedly started a Worker or listener")
				}
			})
		}
	}
}

func TestCodexWebSearchRejectsInvalidEnvironmentBeforeExecution(t *testing.T) {
	for _, value := range []string{"tru", "2", "maybe", "false,true", "private-value-must-not-be-echoed"} {
		t.Run(value, func(t *testing.T) {
			root := t.TempDir()
			node, err := NewFromEnvMap(Env{
				"OPENLINKER_AGENT_NODE_ADAPTER":           "codex",
				"OPENLINKER_AGENT_NODE_SESSION_ISOLATION": "off",
				"OPENLINKER_AGENT_NODE_CODEX_BIN":         filepath.Join(root, "must-not-execute"),
				"OPENLINKER_AGENT_NODE_CODEX_WORKSPACE":   root,
				"OPENLINKER_AGENT_NODE_DATA_DIR":          filepath.Join(root, "must-not-create"),
				codexWebSearchEnv:                         value,
			})
			if node != nil || err == nil || !strings.Contains(err.Error(), codexWebSearchEnv) {
				t.Fatalf("invalid setting must reject Node construction: node=%v err=%v", node != nil, err)
			}
			if strings.Contains(err.Error(), value) {
				t.Fatal("configuration error echoed an untrusted value")
			}
			entries, readErr := os.ReadDir(root)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("invalid configuration touched runtime state: %v", readErr)
			}
		})
	}
}

// Exercise NewFromEnv through the production adapter and subprocess launcher.
// The child speaks app-server RPC; it does not access a real account or search.
func TestCodexWebSearchEnvironmentReachesExecutedArguments(t *testing.T) {
	for _, test := range []struct {
		name, value, want string
	}{
		{"unset", "", `web_search="live"`},
		{"false", "false", `web_search="disabled"`},
		{"true", "true", `web_search="live"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if strings.HasPrefix(key, "OPENLINKER_") {
					t.Setenv(key, "")
				}
			}
			workspace := t.TempDir()
			bin := writeNodeRPCFixture(t)
			for key, value := range map[string]string{
				"OPENLINKER_AGENT_NODE_ADAPTER":             "codex",
				"OPENLINKER_AGENT_NODE_SESSION_ISOLATION":   "off",
				"OPENLINKER_AGENT_NODE_CODEX_BIN":           bin,
				"OPENLINKER_AGENT_NODE_CODEX_WORKSPACE":     workspace,
				"OPENLINKER_AGENT_NODE_CODEX_SESSION_REUSE": "true",
				"OPENLINKER_AGENT_NODE_CODEX_SESSION_STORE": filepath.Join(workspace, "sessions.json"),
				"OPENLINKER_AGENT_NODE_DATA_DIR":            filepath.Join(workspace, "unused-sdk"),
				codexWebSearchEnv:                           test.value,
			} {
				t.Setenv(key, value)
			}
			node, err := NewFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			for turn := 0; turn < 2; turn++ {
				result, err := node.Adapter.Run(context.Background(), "synthetic search-policy task", RunContext{
					RunID:        []string{"first", "second"}[turn],
					Conversation: &ConversationContext{Source: "core", SessionKey: "search-policy-session"},
				})
				if err != nil {
					t.Fatal(err)
				}
				output := normalizeAdapterResult(result).Output.(JSONMap)
				if output["summary"] != "shared answer" || output["codex_session_resumed"] != (turn == 1) {
					t.Fatalf("execution or session resume failed: %v", output)
				}
				raw, err := os.ReadFile(filepath.Join(workspace, "actual-argv.json"))
				if err != nil {
					t.Fatal(err)
				}
				var argv []string
				if err := json.Unmarshal(raw, &argv); err != nil {
					t.Fatal(err)
				}
				searchSettings := 0
				for i, arg := range argv {
					if strings.HasPrefix(arg, "web_search=") {
						searchSettings++
						if i == 0 || argv[i-1] != "-c" || arg != test.want {
							t.Fatalf("turn %d executed search policy %q, want %q", turn, arg, test.want)
						}
					}
				}
				if searchSettings != 1 {
					t.Fatalf("expected one explicit search policy, got %d", searchSettings)
				}
				if !strings.Contains(strings.Join(argv, " "), `approval_policy="never"`) {
					t.Fatal("search opt-in changed approval policy")
				}
			}
			if node.started || node.worker != nil {
				t.Fatal("adapter fixture unexpectedly started SDK Worker")
			}
			if _, err := os.Stat(os.Getenv("OPENLINKER_AGENT_NODE_DATA_DIR")); !os.IsNotExist(err) {
				t.Fatal("adapter fixture unexpectedly created SDK state")
			}
		})
	}
}
