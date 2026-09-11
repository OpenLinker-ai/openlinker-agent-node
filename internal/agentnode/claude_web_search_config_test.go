package agentnode

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const claudeWebSearchEnv = "OPENLINKER_AGENT_NODE_CLAUDE_WEB_SEARCH"

func TestClaudeWebSearchEnvironmentValues(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{"", false}, {"false", false}, {"0", false}, {"no", false}, {"off", false},
		{"true", true}, {"1", true}, {"yes", true}, {"on", true},
		{" TRUE ", true}, {"\tFalse\n", false}, {"YeS", true}, {"OFF", false},
	} {
		t.Run(test.value, func(t *testing.T) {
			node, err := NewFromEnvMap(Env{
				"OPENLINKER_AGENT_NODE_ADAPTER": "claude", claudeWebSearchEnv: test.value,
			})
			if err != nil {
				t.Fatal(err)
			}
			adapter := node.Adapter.(*NativeAdapter)
			if adapter.Config.WebSearch != test.want {
				t.Fatalf("WebSearch=%t, want %t", adapter.Config.WebSearch, test.want)
			}
			if adapter.Config.Permission != "dontAsk" || len(adapter.Config.AllowedTools) != 0 ||
				adapter.Config.SessionReuse || len(adapter.Config.DelegationTargets) != 0 {
				t.Fatal("web search changed unrelated default permissions or session settings")
			}
			if node.started || node.worker != nil || node.Helper != nil || node.PublicA2A != nil {
				t.Fatal("parsing web search configuration must not start a Worker or auxiliary server")
			}
		})
	}
}

func TestClaudeWebSearchRejectsInvalidEnvironmentBeforeExecution(t *testing.T) {
	for _, value := range []string{"tru", "2", "maybe", "false,true", "private-value-must-not-be-echoed"} {
		t.Run(value, func(t *testing.T) {
			root := t.TempDir()
			node, err := NewFromEnvMap(Env{
				"OPENLINKER_AGENT_NODE_ADAPTER":          "claude",
				"OPENLINKER_AGENT_NODE_CLAUDE_BIN":       filepath.Join(root, "must-not-execute"),
				"OPENLINKER_AGENT_NODE_CLAUDE_WORKSPACE": root,
				"OPENLINKER_AGENT_NODE_DATA_DIR":         filepath.Join(root, "must-not-create"),
				claudeWebSearchEnv:                       value,
			})
			if node != nil || err == nil || !strings.Contains(err.Error(), claudeWebSearchEnv) {
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

// Execute the public Node environment entry through NativeAdapter and the real
// provider subprocess launcher. The child is a protocol fixture, not a model or
// network-search test. Capturing argv prevents a disconnected config-only test.
func TestClaudeWebSearchEnvironmentReachesExecutedArguments(t *testing.T) {
	for _, test := range []struct {
		name        string
		value       *string
		allowed     string
		wantAllowed string
		want        bool
		processEnv  bool
	}{
		{name: "unset", allowed: `["Read","Glob"]`, wantAllowed: "Read,Glob"},
		{name: "empty", value: stringPointer(""), allowed: `["Read","Glob"]`, wantAllowed: "Read,Glob"},
		{name: "false", value: stringPointer("false"), allowed: `["Read","Glob"]`, wantAllowed: "Read,Glob"},
		{name: "true", value: stringPointer("true"), allowed: `["Read","Glob"]`, wantAllowed: "Read,Glob", want: true},
		{name: "true-from-process-environment", value: stringPointer("true"), allowed: `["Read","Glob"]`, wantAllowed: "Read,Glob", want: true, processEnv: true},
		{name: "true-without-implicit-tool-approval", value: stringPointer("true"), allowed: `[]`, want: true},
		{name: "false-deny-remains-even-if-tool-allowed", value: stringPointer("false"), allowed: `["WebSearch","WebFetch"]`, wantAllowed: "WebSearch,WebFetch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			binary := writeFakeCodex(t, "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$@\" > actual-args\ncat > actual-prompt\nprintf '%s\\n' '{\"type\":\"result\",\"result\":\"fixture completed\",\"session_id\":\"web-config-session\"}'\n")
			env := Env{
				"OPENLINKER_AGENT_NODE_ADAPTER":              "claude",
				"OPENLINKER_AGENT_NODE_CLAUDE_BIN":           binary,
				"OPENLINKER_AGENT_NODE_CLAUDE_WORKSPACE":     workspace,
				"OPENLINKER_AGENT_NODE_CLAUDE_MODEL":         "fixture-model",
				"OPENLINKER_AGENT_NODE_CLAUDE_PERMISSION":    "dontAsk",
				"OPENLINKER_AGENT_NODE_CLAUDE_ALLOWED_TOOLS": test.allowed,
				"OPENLINKER_AGENT_NODE_CLAUDE_SESSION_REUSE": "true",
				"OPENLINKER_AGENT_NODE_CLAUDE_SESSION_STORE": filepath.Join(workspace, "sessions.json"),
				"OPENLINKER_AGENT_NODE_DATA_DIR":             filepath.Join(workspace, "unused-sdk"),
				"OPENLINKER_AGENT_NODE_HELPER":               "false",
				"OPENLINKER_AGENT_NODE_PUBLIC_A2A":           "false",
			}
			if test.value != nil {
				env[claudeWebSearchEnv] = *test.value
			}
			node, err := NewFromEnvMap(env)
			if test.processEnv {
				// Isolate host OpenLinker settings before exercising os.Getenv.
				// No real credential or provider executable is used by this fixture.
				for _, entry := range os.Environ() {
					key, _, _ := strings.Cut(entry, "=")
					if strings.HasPrefix(key, "OPENLINKER_") {
						t.Setenv(key, "")
					}
				}
				for key, value := range env {
					t.Setenv(key, value)
				}
				node, err = NewFromEnv()
			}
			if err != nil {
				t.Fatal(err)
			}
			for turn := 0; turn < 2; turn++ {
				run := RunContext{RunID: []string{"first", "second"}[turn], Conversation: &ConversationContext{
					Source: "core", SessionKey: "web-config-conversation",
				}}
				result, runErr := node.Adapter.Run(context.Background(), "synthetic task", run)
				if runErr != nil {
					t.Fatal(runErr)
				}
				output := normalizeAdapterResult(result).Output.(JSONMap)
				if output["summary"] != "fixture completed" || output["claude_session_resumed"] != (turn == 1) {
					t.Fatalf("fixture execution or native resume failed: %v", output)
				}
				raw, readErr := os.ReadFile(filepath.Join(workspace, "actual-args"))
				if readErr != nil {
					t.Fatal(readErr)
				}
				actual := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
				want := []string{"--safe-mode", "--no-chrome", "--disable-slash-commands", "-p",
					"--output-format", "stream-json", "--verbose", "--include-partial-messages",
					"--permission-mode", "dontAsk", "--model", "fixture-model"}
				if test.wantAllowed != "" {
					want = append(want, "--allowedTools", test.wantAllowed)
				}
				if !test.want {
					want = append(want, "--disallowedTools", "WebSearch,WebFetch")
				}
				if turn == 1 {
					want = append(want, "--resume", "web-config-session")
				}
				if !reflect.DeepEqual(actual, want) {
					t.Fatalf("turn %d executed argv=%q, want %q", turn, actual, want)
				}
				prompt, promptErr := os.ReadFile(filepath.Join(workspace, "actual-prompt"))
				if promptErr != nil || !strings.Contains(string(prompt), "synthetic task") {
					t.Fatal("fixture did not receive the actual task through stdin")
				}
			}
			if node.started || node.worker != nil {
				t.Fatal("adapter fixture unexpectedly started the SDK Worker")
			}
			if _, err := os.Stat(env["OPENLINKER_AGENT_NODE_DATA_DIR"]); !os.IsNotExist(err) {
				t.Fatal("adapter fixture unexpectedly created SDK state")
			}
		})
	}
}

func stringPointer(value string) *string { return &value }
