package agentnode

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func isolationValues(t *testing.T, provider string) map[string]string {
	t.Helper()
	if os.Geteuid() == 0 || runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("non-root macOS/Linux")
	}
	return map[string]string{"OPENLINKER_URL": "https://core.example/", "OPENLINKER_AGENT_NODE_ADAPTER": provider,
		"OPENLINKER_AGENT_NODE_SESSION_ISOLATION": "native", "OPENLINKER_AGENT_NODE_SESSION_ROOT": filepath.Join(t.TempDir(), "sessions"),
		"OPENLINKER_AGENT_NODE_" + strings.ToUpper(provider) + "_SESSION_REUSE": "true",
		"OPENLINKER_AGENT_NODE_SESSION_NETWORK_DOMAINS":                         `["api.openai.com","api.anthropic.com"]`}
}

func TestNativeIsolationConfigurationReachesBothProductionAdapters(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		values := isolationValues(t, name)
		values["OPENLINKER_AGENT_NODE_SESSION_TEMP_ROOT"] = "/tmp/ol-private-temp"
		adapter, err := adapterFromEnv(func(k string) string { return values[k] }, name)
		if err != nil {
			t.Fatal(err)
		}
		var native *NativeAdapter
		switch a := adapter.(type) {
		case *CodexAdapter:
			native = a.native()
		case *NativeAdapter:
			native = a
		default:
			t.Fatalf("unexpected adapter %T", adapter)
		}
		if !native.Config.SessionIsolation.Enabled() || native.Config.SessionIsolation.Namespace != "https://core.example" || !native.Config.SessionReuse || native.Config.SessionIsolation.TempRoot != values["OPENLINKER_AGENT_NODE_SESSION_TEMP_ROOT"] {
			t.Fatal("isolation settings lost")
		}
		// No personal login can turn an invalid startup into a green result.
		native.Config.Env = []string{"HOME=/personal", "PATH=" + os.Getenv("PATH")}
		if err := native.Preflight(context.Background()); err == nil || !strings.Contains(err.Error(), "API_KEY") {
			t.Fatalf("missing isolated credential accepted: %v", err)
		}
	}
}

func TestNativeIsolationRejectsIgnoredOptionsAndMockBypass(t *testing.T) {
	for _, mode := range []string{"http", "openclaw", "command", "a2a"} {
		v := isolationValues(t, "codex")
		if _, err := adapterFromEnv(func(k string) string { return v[k] }, mode); err == nil {
			t.Fatalf("isolation ignored for %s", mode)
		}
	}
	for _, change := range []func(map[string]string){
		func(v map[string]string) { v["OPENLINKER_AGENT_NODE_SESSION_ISOLATION"] = "off" },
		func(v map[string]string) { v["OPENLINKER_URL"] = "https://token@core.example" },
		func(v map[string]string) { v["OPENLINKER_AGENT_NODE_CODEX_MOCK_RESPONSE"] = "fake" },
		func(v map[string]string) { v["OPENLINKER_AGENT_NODE_SESSION_READ_PATHS"] = `["/home/*"]` },
		func(v map[string]string) { v["OPENLINKER_AGENT_NODE_SESSION_NETWORK_DOMAINS"] = `["127.0.0.1"]` },
	} {
		v := isolationValues(t, "codex")
		change(v)
		if _, err := adapterFromEnv(func(k string) string { return v[k] }, "codex"); err == nil {
			t.Fatal("invalid isolation options accepted")
		}
	}
}
