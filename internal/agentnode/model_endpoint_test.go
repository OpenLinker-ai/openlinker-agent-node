package agentnode

import (
	"strings"
	"testing"
)

func TestConfiguredGatewayReachesProductionNativeAdapter(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		for _, isolated := range []bool{false, true} {
			v := map[string]string{"OPENLINKER_AGENT_NODE_SESSION_ISOLATION": "off"}
			if isolated {
				v = isolationValues(t, name)
				v["OPENLINKER_AGENT_NODE_SESSION_NETWORK_DOMAINS"] = `["gateway.example"]`
			}
			key := "OPENLINKER_AGENT_NODE_" + strings.ToUpper(name) + "_BASE_URL"
			v[key] = "https://gateway.example/vendor/v1"
			a, err := adapterFromEnv(func(k string) string { return v[k] }, name)
			if err != nil {
				t.Fatal(err)
			}
			var native *NativeAdapter
			switch a := a.(type) {
			case *CodexAdapter:
				native = a.native()
			case *NativeAdapter:
				native = a
			default:
				t.Fatalf("unexpected adapter %T", a)
			}
			got := native.Config.CodexBaseURL
			if name == "claude" {
				got = native.Config.ClaudeBaseURL
			}
			if got != v[key] {
				t.Fatal("gateway configuration lost before provider creation")
			}
			v[key] = "https://secret@gateway.example"
			if _, err := adapterFromEnv(func(k string) string { return v[k] }, name); err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("invalid endpoint accepted or leaked: %v", err)
			}
		}
	}
}
