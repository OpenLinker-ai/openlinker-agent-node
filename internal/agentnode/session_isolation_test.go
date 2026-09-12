package agentnode

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSessionIsolationEnvironmentWiresBothNativeAdapters(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires non-root POSIX host")
	}
	for _, mode := range []string{"codex", "claude"} {
		t.Run(mode, func(t *testing.T) {
			prefix := "OPENLINKER_AGENT_NODE_" + strings.ToUpper(mode)
			env := Env{"OPENLINKER_URL": "https://core.example", "OPENLINKER_AGENT_NODE_SESSION_ISOLATION": "docker",
				"OPENLINKER_AGENT_NODE_SESSION_ROOT": filepath.Join(t.TempDir(), "sessions"), "OPENLINKER_AGENT_NODE_SESSION_IMAGE": "sha256:" + strings.Repeat("a", 64), prefix + "_SESSION_REUSE": "true"}
			get := func(key string) string { return env[key] }
			adapter, err := adapterFromEnv(get, mode)
			if err != nil {
				t.Fatal(err)
			}
			var native *NativeAdapter
			if mode == "codex" {
				native = adapter.(*CodexAdapter).native()
			} else {
				native = adapter.(*NativeAdapter)
			}
			if native.Config.SessionIsolation.Mode != "docker" || native.Config.SessionIsolation.Namespace != "https://core.example" || !native.Config.SessionReuse {
				t.Fatal("session config did not reach native provider")
			}
			for _, entry := range []struct{ key, value string }{
				{prefix + "_SESSION_REUSE", "false"}, {prefix + "_WORKSPACE", "/legacy-workspace"}, {prefix + "_SESSION_STORE", "/legacy-map"},
				{"OPENLINKER_AGENT_NODE_SESSION_ISOLATION", "true"}, {"OPENLINKER_AGENT_NODE_SESSION_IMAGE", "provider:latest"},
				{"OPENLINKER_AGENT_NODE_SESSION_NETWORK", "host"}, {"OPENLINKER_AGENT_NODE_SESSION_NETWORK", "container:another"},
				{"OPENLINKER_AGENT_NODE_DELEGATION_TARGETS", `["agent-2"]`},
				{"OPENLINKER_AGENT_NODE_DATA_DIR", env["OPENLINKER_AGENT_NODE_SESSION_ROOT"]},
				{"OPENLINKER_AGENT_NODE_DATA_DIR", filepath.Dir(env["OPENLINKER_AGENT_NODE_SESSION_ROOT"])},
			} {
				previous, present := env[entry.key]
				env[entry.key] = entry.value
				if _, err := adapterFromEnv(get, mode); err == nil {
					t.Fatalf("accepted incompatible %s", entry.key)
				}
				if present {
					env[entry.key] = previous
				} else {
					delete(env, entry.key)
				}
			}
			for _, unsupported := range []string{"http", "openclaw", "command", "a2a"} {
				if _, err := adapterFromEnv(get, unsupported); err == nil {
					t.Fatalf("ignored isolation on %s", unsupported)
				}
			}
			if mode == "codex" {
				for _, suffix := range []string{"_SANDBOX", "_APPROVAL", "_MOCK_RESPONSE"} {
					env[prefix+suffix] = "override"
					if _, err := adapterFromEnv(get, mode); err == nil {
						t.Fatalf("accepted Codex override %s", suffix)
					}
					delete(env, prefix+suffix)
				}
			}
		})
	}
}

func TestDisabledSessionIsolationPreservesDefaultsAndRejectsDanglingOptions(t *testing.T) {
	for _, mode := range []string{"codex", "claude", "http", "command"} {
		if _, err := adapterFromEnv(func(string) string { return "" }, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := adapterFromEnv(func(key string) string {
			if key == "OPENLINKER_AGENT_NODE_SESSION_ROOT" {
				return "/dangling"
			}
			return ""
		}, mode); err == nil {
			t.Fatal("silently ignored sandbox option")
		}
	}
}

func TestSessionIsolationRejectsSDKStateThroughDirectoryAlias(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires non-root POSIX host")
	}
	parent := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(parent, alias); err != nil {
		t.Fatal(err)
	}
	env := Env{"OPENLINKER_URL": "https://core.example", "OPENLINKER_AGENT_NODE_SESSION_ISOLATION": "docker",
		"OPENLINKER_AGENT_NODE_SESSION_ROOT": filepath.Join(alias, "future"), "OPENLINKER_AGENT_NODE_SESSION_IMAGE": "sha256:" + strings.Repeat("a", 64),
		"OPENLINKER_AGENT_NODE_CODEX_SESSION_REUSE": "true", "OPENLINKER_AGENT_NODE_DATA_DIR": filepath.Join(parent, "future", "spool")}
	if _, err := adapterFromEnv(func(key string) string { return env[key] }, "codex"); err == nil || !strings.Contains(err.Error(), "non-nested") {
		t.Fatalf("aliased SDK overlap accepted: %v", err)
	}
}
