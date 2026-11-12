package agentnode

import (
	"strings"
	"testing"
)

func TestCodexImageInputEnvironment(t *testing.T) {
	for value, want := range map[string]bool{
		"": false, "false": false, "0": false, "off": false, " NO ": false,
		"true": true, "1": true, "yes": true, "On": true,
	} {
		node, err := NewFromEnvMap(Env{
			"OPENLINKER_AGENT_NODE_ADAPTER": "codex", "OPENLINKER_AGENT_NODE_SESSION_ISOLATION": "off",
			"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT": value,
		})
		if err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		if got := node.Adapter.(*CodexAdapter).native().Config.CodexImageInput; got != want {
			t.Fatalf("%q: CodexImageInput=%t, want %t", value, got, want)
		}
	}
	_, err := NewFromEnvMap(Env{
		"OPENLINKER_AGENT_NODE_ADAPTER": "codex", "OPENLINKER_AGENT_NODE_SESSION_ISOLATION": "off",
		"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT": "maybe-secret",
	})
	if err == nil || strings.Contains(err.Error(), "maybe-secret") {
		t.Fatalf("invalid value: %v", err)
	}
}
