package agentnode

import (
	"strings"
	"testing"
)

func TestNativeProviderSessionReuseDefaultsStayNodeOwned(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, input := range []string{"", "true", "false"} {
			t.Run(provider+"/"+input, func(t *testing.T) {
				env := Env{
					"OPENLINKER_AGENT_NODE_ADAPTER": provider,
					// The CLI/Plugin app's setting must not override Node defaults.
					"OPENLINKER_AGENT_SESSION_REUSE": "true",
				}
				if input != "" {
					env["OPENLINKER_AGENT_NODE_"+strings.ToUpper(provider)+"_SESSION_REUSE"] = input
				}
				node, err := NewFromEnvMap(env)
				if err != nil {
					t.Fatal(err)
				}
				var got bool
				switch adapter := node.Adapter.(type) {
				case *NativeAdapter:
					got = adapter.Config.SessionReuse
				case *CodexAdapter:
					got = adapter.SessionReuse
				default:
					t.Fatalf("unexpected adapter %T", node.Adapter)
				}
				if want := input == "true"; got != want {
					t.Fatalf("Node session reuse=%t, want %t for %q", got, want, input)
				}
			})
		}
	}
}
