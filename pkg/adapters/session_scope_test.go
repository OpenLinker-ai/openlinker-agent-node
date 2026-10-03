package adapters

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

const sessionTestPrincipal = "ps1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// Existing protocol fixtures now provide the same trusted owner/current-run
// fields that the SDK Handler receives from Core. Never use this in negative tests.
func sessionTestRun(run RunContext) RunContext {
	if run.RunID == "" {
		run.RunID = "scope-test-run"
	}
	if run.AgentID == "" {
		run.AgentID = "11111111-1111-4111-8111-111111111111"
	}
	if run.Authority == nil {
		run.Authority = &openlinker.RuntimeAuthorityContext{PrincipalScopeID: sessionTestPrincipal}
	}
	if run.Conversation != nil {
		conversation := *run.Conversation
		conversation.Source, conversation.CurrentRunID = "core", run.RunID
		run.Conversation = &conversation
	}
	return run
}

func seedOwnedTestSession(path, provider, workspace, conversation, id, mode string) error {
	run := sessionTestRun(RunContext{Conversation: &ConversationContext{SessionKey: conversation}})
	namespace, key := providerSessionScope(provider, ProviderConfig{}, run)
	return saveSessionForClientMode(path, namespace, workspace, key, id, mode, 1)
}

func sessionScopeFixture(t *testing.T, name string) (ProviderConfig, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("protocol fixtures use a POSIX shell")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, name)
	trace, prompt := filepath.Join(dir, "args"), filepath.Join(dir, "prompt")
	config := ProviderConfig{Provider: name, Bin: bin, Workspace: dir, SessionStore: filepath.Join(dir, "sessions.json"), SessionReuse: true, Timeout: 10 * time.Second}
	if name == "codex" {
		writeCodexRPCFixture(t, bin, "standard")
		log := filepath.Join(dir, "rpc")
		config.Env = append(os.Environ(), "TEST_LOG="+log)
		config.EnvAllowlist = []string{"TEST_LOG"}
		return config, log + ".requests", log + ".prompt"
	}
	script := `#!/bin/sh
set -eu
printf '%s\n' "$@" > args
cat > prompt
resume=""
while [ "$#" -gt 0 ]; do
 if [ "$1" = "--resume" ]; then shift; resume="$1"; fi
 shift
done
if [ -z "$resume" ]; then
 count=0
 if [ -f count ]; then count=$(cat count); fi
 count=$((count + 1))
 printf '%s' "$count" > count
 resume="22222222-2222-4222-8222-$(printf '%012d' "$count")"
fi
printf '{"type":"result","subtype":"success","result":"answer","session_id":"%s"}\n' "$resume"
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	config.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}
	return config, trace, prompt
}

func sessionScopeHandle(t *testing.T, config ProviderConfig, run RunContext) openlinker.RuntimeResult {
	t.Helper()
	handler, err := NewHandler(config)
	if err != nil {
		t.Fatal(err)
	}
	metadata := openlinker.RuntimeJSONMap{"principal_scope_id": "spoofed-payload-identity"}
	if run.Conversation != nil {
		raw, err := json.Marshal(run.Conversation)
		if err != nil {
			t.Fatal(err)
		}
		var conversation map[string]any
		if err := json.Unmarshal(raw, &conversation); err != nil {
			t.Fatal(err)
		}
		metadata["conversation"] = conversation
	}
	// These are the SDK's trusted fields, not user metadata. A fresh Handler on
	// every call also verifies that continuation comes from the persisted map.
	result, err := handler.Handle(context.Background(), openlinker.RuntimeContext{
		RunID: run.RunID, AgentID: run.AgentID, Authority: run.Authority, Input: run.Input, Metadata: metadata,
	})
	if err != nil || result.Status != "success" {
		t.Fatalf("Handler failed: %v %#v", err, result)
	}
	return result
}

func sessionScopeInvocation(t *testing.T, config ProviderConfig, trace string, run RunContext, wantResume bool) openlinker.RuntimeResult {
	t.Helper()
	if err := os.WriteFile(trace, nil, 0600); err != nil {
		t.Fatal(err)
	}
	result := sessionScopeHandle(t, config, run)
	raw, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	marker := "--resume"
	if config.Provider == "codex" {
		marker = "thread/resume"
	}
	if got := strings.Contains(string(raw), marker); got != wantResume {
		t.Fatalf("actual %s resume=%t, want %t: %s", config.Provider, got, wantResume, raw)
	}
	return result
}

func TestProviderSessionScopeThroughHandler(t *testing.T) {
	for _, name := range []string{"claude", "codex"} {
		t.Run(name, func(t *testing.T) {
			config, trace, prompt := sessionScopeFixture(t, name)
			a := sessionTestRun(RunContext{RunID: "a1", Input: "current", Conversation: &ConversationContext{SessionKey: "same-context", HistoryBeforeCurrent: []ConversationMessage{{RunID: "prior", Role: "user", Content: "core-seed-history"}}}})
			legacyMode := "standard"
			if name == "codex" {
				legacyMode = "codex_rpc_v1:standard"
			}
			if err := saveSessionForClientMode(config.SessionStore, name, config.Workspace, a.Conversation.SessionKey, "unowned-legacy", legacyMode, 7); err != nil {
				t.Fatal(err)
			}
			legacy := readSessionStore(config.SessionStore)
			sessionScopeInvocation(t, config, trace, a, false)
			raw, err := os.ReadFile(prompt)
			if err != nil || !strings.Contains(string(raw), "core-seed-history") {
				t.Fatalf("fresh session lost Core history: %v", err)
			}
			a.RunID = "a2"
			a.Conversation.CurrentRunID = a.RunID
			sessionScopeInvocation(t, config, trace, a, true)
			raw, err = os.ReadFile(prompt)
			if err != nil || strings.Contains(string(raw), "core-seed-history") {
				t.Fatalf("owned history cursor was not reused: %v", err)
			}
			b := a
			b.RunID = "b1"
			bc := *a.Conversation
			bc.CurrentRunID = b.RunID
			b.Conversation = &bc
			b.Authority = &openlinker.RuntimeAuthorityContext{PrincipalScopeID: "ps1_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"}
			sessionScopeInvocation(t, config, trace, b, false)
			raw, err = os.ReadFile(prompt)
			if err != nil || !strings.Contains(string(raw), "core-seed-history") {
				t.Fatalf("new principal lost Core history: %v", err)
			}
			a.RunID = "a3"
			a.Conversation.CurrentRunID = a.RunID
			sessionScopeInvocation(t, config, trace, a, true)
			other := a
			other.AgentID = "33333333-3333-4333-8333-333333333333"
			sessionScopeInvocation(t, config, trace, other, false)
			next := a
			nc := *a.Conversation
			nc.SessionKey = "different-context"
			next.Conversation = &nc
			sessionScopeInvocation(t, config, trace, next, false)
			store := readSessionStore(config.SessionStore)
			if len(store.Sessions) != len(legacy.Sessions)+4 {
				t.Fatalf("expected four independent owned mappings plus untouched legacy: %#v", store)
			}
			for key, record := range legacy.Sessions {
				if !reflect.DeepEqual(store.Sessions[key], record) {
					t.Fatal("legacy record overwritten or adopted")
				}
			}
			before, err := os.ReadFile(config.SessionStore)
			if err != nil {
				t.Fatal(err)
			}
			config.SessionReuse = false
			sessionScopeInvocation(t, config, trace, a, false)
			after, err := os.ReadFile(config.SessionStore)
			if err != nil || string(after) != string(before) {
				t.Fatal("disabled reuse touched mappings")
			}
		})
	}
}

func TestProviderSessionScopeMissingAuthorityDoesNotReadOrWriteMaps(t *testing.T) {
	for _, name := range []string{"claude", "codex"} {
		t.Run(name, func(t *testing.T) {
			config, trace, _ := sessionScopeFixture(t, name)
			base := sessionTestRun(RunContext{Conversation: &ConversationContext{SessionKey: "same-context"}})
			sessionScopeInvocation(t, config, trace, base, false)
			before, err := os.ReadFile(config.SessionStore)
			if err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"missing-principal", "empty-principal", "missing-agent", "missing-run", "caller-source", "stale-run", "missing-conversation", "missing-session-key"} {
				t.Run(kind, func(t *testing.T) {
					run := base
					conv := *base.Conversation
					run.Conversation = &conv
					switch kind {
					case "missing-principal":
						run.Authority = nil
					case "empty-principal":
						run.Authority = &openlinker.RuntimeAuthorityContext{}
					case "missing-agent":
						run.AgentID = ""
					case "missing-run":
						run.RunID = ""
					case "caller-source":
						run.Conversation.Source = "caller"
					case "stale-run":
						run.Conversation.CurrentRunID = "other-run"
					case "missing-conversation":
						run.Conversation = nil
					case "missing-session-key":
						run.Conversation.SessionKey = ""
					}
					sessionScopeInvocation(t, config, trace, run, false)
					after, err := os.ReadFile(config.SessionStore)
					if err != nil || string(after) != string(before) {
						t.Fatal("untrusted/missing context touched an owned mapping")
					}
				})
			}
		})
	}
}

func TestProviderSessionScopeRejectsUntrustedConversationDirectly(t *testing.T) {
	for _, name := range []string{"claude", "codex"} {
		for _, kind := range []string{"caller-source", "fallback-only"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				run := sessionTestRun(RunContext{Conversation: &ConversationContext{SessionKey: "conversation", RootContextID: "fallback"}})
				if kind == "caller-source" {
					run.Conversation.Source = "caller"
				} else {
					run.Conversation.SessionKey = ""
				}
				if _, key := providerSessionScope(name, ProviderConfig{}, run); key != "" {
					t.Fatal("untrusted conversation enabled private session reuse")
				}
			})
		}
	}
}

func TestProviderSessionScopeCannotAliasCallerChosenLegacyKey(t *testing.T) {
	for _, name := range []string{"claude", "codex"} {
		t.Run(name, func(t *testing.T) {
			config, trace, _ := sessionScopeFixture(t, name)
			run := sessionTestRun(RunContext{Conversation: &ConversationContext{SessionKey: "conversation"}})
			_, digest := providerSessionScope(name, config, run)
			mode := "standard"
			if name == "codex" {
				mode = "codex_rpc_v1:standard"
			}
			// Even deliberately choosing the exact new digest as an old conversation
			// key cannot move that old mapping into the new provider hash domain.
			if err := saveSessionForClientMode(config.SessionStore, name, config.Workspace, digest, "legacy", mode, 1); err != nil {
				t.Fatal(err)
			}
			old := readSessionStore(config.SessionStore)
			sessionScopeInvocation(t, config, trace, run, false)
			now := readSessionStore(config.SessionStore)
			for key, record := range old.Sessions {
				if !reflect.DeepEqual(now.Sessions[key], record) {
					t.Fatal("legacy collision rewrote old state")
				}
			}
			sessionScopeInvocation(t, config, trace, run, true)
		})
	}
}

func TestProviderSessionScopePreservesExistingNativeMappings(t *testing.T) {
	for _, name := range []string{"claude", "codex"} {
		t.Run(name, func(t *testing.T) {
			fixture, _, _ := sessionScopeFixture(t, name)
			config := isolationConfig(t, name)
			config.Bin = fixture.Bin
			run := isolationRun("existing-conversation")
			prepared, closeSession, err := prepareIsolatedSession(context.Background(), config, run)
			if err != nil {
				t.Fatal(err)
			}
			mode, id := "host-auth-tools-v1", "22222222-2222-4222-8222-222222222222"
			if name == "codex" {
				mode = "codex_rpc_v1:" + mode
				id = fixtureThread
			}
			oldKey := sessionStoreKey(name, prepared.Workspace, run.Conversation.SessionKey)
			if err := saveSessionForClientMode(prepared.SessionStore, name, prepared.Workspace, run.Conversation.SessionKey, id, mode, 3); err != nil {
				t.Fatal(err)
			}
			if err := closeSession(); err != nil {
				t.Fatal(err)
			}
			result := sessionScopeHandle(t, config, run)
			if result.Output.(map[string]any)[name+"_session_resumed"] != true {
				t.Fatal("existing native mapping did not resume")
			}
			record := readSessionStore(prepared.SessionStore).Sessions[oldKey]
			if record.SessionID != id || record.ClientMode != mode || record.ClientModeGeneration != 3 {
				t.Fatalf("native identity/mode/generation changed: %#v", record)
			}
			for _, change := range []string{"principal", "agent", "core"} {
				next, nextConfig := run, config
				switch change {
				case "principal":
					next.Authority = &openlinker.RuntimeAuthorityContext{PrincipalScopeID: "principal-other"}
				case "agent":
					next.AgentID = "agent-other"
				case "core":
					nextConfig.SessionIsolation.Namespace = "https://other-core.example"
				}
				result := sessionScopeHandle(t, nextConfig, next)
				if result.Output.(map[string]any)[name+"_session_resumed"] != false {
					t.Fatal("native authority boundary crossed:", change)
				}
			}
		})
	}
}
