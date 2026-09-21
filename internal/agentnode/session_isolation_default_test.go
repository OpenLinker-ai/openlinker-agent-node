package agentnode

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func defaultIsolationEnv(t *testing.T, provider string) Env {
	t.Helper()
	if os.Geteuid() == 0 || runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("native isolation requires non-root macOS/Linux")
	}
	return Env{"OPENLINKER_URL": "https://core.example/", "OPENLINKER_AGENT_NODE_ADAPTER": provider, "HOME": t.TempDir()}
}

func loadedIsolation(t *testing.T, env Env) (config struct {
	enabled, reuse bool
	root, provider string
}) {
	t.Helper()
	node, err := NewFromEnvMap(env)
	if err != nil {
		t.Fatal(err)
	}
	switch a := node.Adapter.(type) {
	case *CodexAdapter:
		c := a.native().Config
		config.enabled, config.reuse, config.root, config.provider = c.SessionIsolation.Enabled(), c.SessionReuse, c.SessionIsolation.Root, c.Provider
	case *NativeAdapter:
		c := a.Config
		config.enabled, config.reuse, config.root, config.provider = c.SessionIsolation.Enabled(), c.SessionReuse, c.SessionIsolation.Root, c.Provider
	default:
		t.Fatalf("unexpected adapter %T", node.Adapter)
	}
	return config
}

func TestLocalClientsDefaultToNativeIsolation(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		env := defaultIsolationEnv(t, provider)
		got := loadedIsolation(t, env)
		want := filepath.Join(env["HOME"], ".local", "state", "openlinker-agent-node-sessions")
		if !got.enabled || !got.reuse || got.root != want || got.provider != provider {
			t.Fatalf("%s did not default to native isolation with reuse and the HOME root: %+v", provider, got)
		}
		// The default root must not be created merely by parsing configuration.
		if _, err := os.Stat(want); !os.IsNotExist(err) {
			t.Fatal("configuration created the session root")
		}
		// An explicit root and an explicit opt-out are both honoured.
		env["OPENLINKER_AGENT_NODE_SESSION_ROOT"] = filepath.Join(t.TempDir(), "sessions")
		if got := loadedIsolation(t, env); got.root != env["OPENLINKER_AGENT_NODE_SESSION_ROOT"] {
			t.Fatal("explicit SESSION_ROOT ignored")
		}
		env["OPENLINKER_AGENT_NODE_SESSION_ISOLATION"] = "off"
		delete(env, "OPENLINKER_AGENT_NODE_SESSION_ROOT")
		if got := loadedIsolation(t, env); got.enabled || got.reuse {
			t.Fatal("explicit off did not restore the unisolated defaults")
		}
	}
}

func TestDefaultNativeIsolationRejectsIgnoredSettingsWithOptOutHint(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		prefix := "OPENLINKER_AGENT_NODE_" + strings.ToUpper(provider) + "_"
		for name, change := range map[string]func(Env){
			"workspace": func(e Env) { e[prefix+"WORKSPACE"] = "/synthetic/project" },
			"no reuse":  func(e Env) { e[prefix+"SESSION_REUSE"] = "false" },
			"no home":   func(e Env) { delete(e, "HOME") },
			"core url":  func(e Env) { delete(e, "OPENLINKER_URL") },
			"repo root": func(e Env) {
				repo := t.TempDir()
				if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
					t.Fatal(err)
				}
				e["OPENLINKER_AGENT_NODE_SESSION_ROOT"] = filepath.Join(repo, "not-yet-created", "sessions")
			},
		} {
			env := defaultIsolationEnv(t, provider)
			change(env)
			node, err := NewFromEnvMap(env)
			if node != nil || err == nil || !strings.Contains(err.Error(), "SESSION_ISOLATION=off") {
				t.Fatalf("%s/%s: default isolation accepted a conflicting setting or lost the opt-out hint: %v", provider, name, err)
			}
			env["OPENLINKER_AGENT_NODE_SESSION_ISOLATION"] = "off"
			if _, err := NewFromEnvMap(env); err != nil && name != "core url" && name != "repo root" {
				t.Fatalf("%s/%s: explicit off still rejected: %v", provider, name, err)
			}
		}
	}
}

func TestMockAndBridgeAdaptersKeepIsolationOff(t *testing.T) {
	env := defaultIsolationEnv(t, "codex")
	env["OPENLINKER_AGENT_NODE_CODEX_MOCK_RESPONSE"] = "synthetic"
	if got := loadedIsolation(t, env); got.enabled {
		t.Fatal("a mock response starts no client and must not require isolation")
	}
	// The exemption belongs to Codex alone: Claude still runs its real client.
	claude := defaultIsolationEnv(t, "claude")
	claude["OPENLINKER_AGENT_NODE_CODEX_MOCK_RESPONSE"] = "synthetic"
	if got := loadedIsolation(t, claude); !got.enabled {
		t.Fatal("a leftover Codex mock variable disabled Claude isolation")
	}
	claude["OPENLINKER_AGENT_NODE_SESSION_ISOLATION"] = "native"
	if got := loadedIsolation(t, claude); !got.enabled {
		t.Fatal("explicit native Claude rejected a Codex-only mock variable")
	}
	for _, mode := range []string{"http", "command", "a2a"} {
		env := defaultIsolationEnv(t, mode)
		if _, err := NewFromEnvMap(env); err != nil {
			t.Fatalf("%s must keep isolation off by default: %v", mode, err)
		}
	}
}
