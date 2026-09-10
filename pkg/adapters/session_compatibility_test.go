package adapters

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These vectors freeze the standard path of Plugin commit
// 78a07dc9b845e44cf665a82001a2f86607dcd0d4, before the Node extraction.
// They intentionally use a separator-free workspace so the domain is the same
// on all operating systems. Plugin has the same vectors, plus its deep modes.
func TestStandardSessionIdentityMatchesPreExtractionPlugin(t *testing.T) {
	for _, test := range []struct {
		provider, key string
	}{
		{"codex", "c08446c2529ca0fb053fb543e5afe30caafccf65a4f92354cfbf710bfaee7aae"},
		{"claude", "8433f70bde587dd3c1c61314631fa3e23f31b596cd472dafa3eb6ac55693b88c"},
	} {
		t.Run(test.provider, func(t *testing.T) {
			if key := sessionStoreKey(" "+test.provider+" ", "workspace", " root-context "); key != test.key {
				t.Fatalf("session hash domain changed: %s", key)
			}
			if hash := sessionKeyHash(test.provider, "workspace", "root-context"); hash != test.key[:24] {
				t.Fatalf("public session evidence changed: %s", hash)
			}
			for _, profile := range []string{"", "standard"} {
				config := ProviderConfig{Provider: test.provider, ExecutionProfile: profile}
				if mode := providerSessionClientMode(config); mode != "standard" {
					t.Fatalf("bridge mode changed: %q", mode)
				}
				config.DelegationTargets, config.DelegationProxyBin = []string{"b", "a"}, "openlinker"
				if mode := providerSessionClientMode(config); mode != "standard_delegation_v1_b74172aca8150458c0c1c42b" {
					t.Fatalf("delegation hash domain changed: %q", mode)
				}
			}
			path := filepath.Join(t.TempDir(), "sessions.json")
			mode := "standard"
			if test.provider == "codex" {
				mode = "codex_rpc_v1:" + mode
			}
			if err := saveSessionForClientMode(path, test.provider, "workspace", "root-context", "saved-session", mode, 7); err != nil {
				t.Fatal(err)
			}
			id, generation, changed := loadSessionForClientMode(path, test.provider, "workspace", "root-context", mode)
			if id != "saved-session" || generation != 7 || changed {
				t.Fatalf("standard resume changed: %q %d %v", id, generation, changed)
			}
			record := readSessionStore(path).Sessions[test.key]
			if record.SessionKeyHash != test.key[:24] || record.ClientMode != mode || record.ClientModeGeneration != 7 {
				t.Fatalf("persisted record contract changed: %+v", record)
			}
			id, generation, changed = loadSessionForClientMode(path, test.provider, "workspace", "root-context", mode+"_delegation_v1_b74172aca8150458c0c1c42b")
			if id != "" || generation != 8 || !changed {
				t.Fatalf("permission rotation changed: %q %d %v", id, generation, changed)
			}
		})
	}
}

func TestLegacyStandardSessionKeepsGenerationAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	// A pre-generation file has neither field. Reading must not rewrite it.
	raw := []byte(`{"sessions":{"8433f70bde587dd3c1c61314631fa3e23f31b596cd472dafa3eb6ac55693b88c":{"session_id":"legacy","session_key_hash":"8433f70bde587dd3c1c613146","workspace":"workspace"}}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	id, generation, changed := loadSessionForClientMode(path, "claude", "workspace", "root-context", "standard")
	if id != "legacy" || generation != 1 || changed {
		t.Fatalf("legacy bridge session changed: %q %d %v", id, generation, changed)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(raw) {
		t.Fatalf("read rewrote legacy identity: %v", err)
	}
}

func TestBridgeFactoryRejectsDeepProfilesInsteadOfDowngrading(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, profile := range []string{"browser", "custom", " BROWSER "} {
			instance, err := NewProvider(ProviderConfig{Provider: provider, ExecutionProfile: profile})
			if instance != nil || err == nil || !strings.Contains(err.Error(), "requires Plugin") {
				t.Fatalf("profile %q silently downgraded: %T %v", profile, instance, err)
			}
		}
	}
}
