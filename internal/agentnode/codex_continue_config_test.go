package agentnode

import (
	"encoding/json"
	"strings"
	"testing"
)

const codexContinueEnv = "OPENLINKER_AGENT_NODE_CODEX_CONTINUE_ON_MAX_MESSAGES"

func TestCodexContinueOnMaxMessagesEnvironment(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{"", false}, {"false", false}, {"0", false}, {"off", false},
		{"true", true}, {"1", true}, {" YES ", true}, {"on", true},
	} {
		t.Run(test.value, func(t *testing.T) {
			node, err := NewFromEnvMap(Env{"OPENLINKER_AGENT_NODE_ADAPTER": "codex", "OPENLINKER_AGENT_NODE_SESSION_ISOLATION": "off", codexContinueEnv: test.value})
			if err != nil {
				t.Fatal(err)
			}
			config := node.Adapter.(*CodexAdapter).native().Config
			if config.CodexContinueOnMaxMessages != test.want {
				t.Fatalf("CodexContinueOnMaxMessages=%t, want %t", config.CodexContinueOnMaxMessages, test.want)
			}
			if !config.WebSearch || config.Sandbox != "read-only" || config.CodexApproval != "never" || config.SessionReuse {
				t.Fatal("continuation changed unrelated tool or session policy")
			}
			raw, _ := json.Marshal(node.ConfigurationReport())
			if strings.Contains(string(raw), `"codex_continue_on_max_messages":true`) != test.want {
				t.Fatalf("configuration report does not reflect the setting: %s", raw)
			}
			if node.started || node.worker != nil {
				t.Fatal("configuration unexpectedly started a Worker")
			}
		})
	}
}

func TestCodexContinueOnMaxMessagesRejectsInvalidEnvironment(t *testing.T) {
	for _, value := range []string{"tru", "2", "private-value-must-not-be-echoed"} {
		t.Run(value, func(t *testing.T) {
			_, err := NewFromEnvMap(Env{"OPENLINKER_AGENT_NODE_ADAPTER": "codex", "OPENLINKER_AGENT_NODE_SESSION_ISOLATION": "off", codexContinueEnv: value})
			if err == nil || err.Error() != codexContinueEnv+" must be a boolean" {
				t.Fatalf("invalid value was accepted or echoed: %v", err)
			}
		})
	}
}
