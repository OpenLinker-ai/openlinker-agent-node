package adapters

import (
	"context"

	"errors"
	"os"

	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/sessionsandbox"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func isolationConfig(t *testing.T, provider string) ProviderConfig {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" || os.Geteuid() == 0 {
		t.Skip("non-root macOS/Linux")
	}
	return ProviderConfig{Provider: provider, SessionReuse: true, Timeout: 30 * time.Second,
		Env:              []string{"PATH=" + os.Getenv("PATH"), "CODEX_API_KEY=fixture-key", "ANTHROPIC_API_KEY=fixture-key", "OPENLINKER_AGENT_TOKEN=private-platform-canary", "NODE_OPTIONS=must-not-inherit", "HOME=" + t.TempDir()},
		SessionIsolation: sessionsandbox.Config{Mode: "native", Root: filepath.Join(t.TempDir(), "private"), Namespace: "https://core.example"}}
}

func isolationRun(key string) RunContext {
	return RunContext{RunID: "run-1", AgentID: "agent-1", Input: "fixture:probe", Authority: &openlinker.RuntimeAuthorityContext{PrincipalScopeID: "principal-1"},
		Conversation: &ConversationContext{Source: "core", SessionKey: key, CurrentRunID: "run-1"}}
}

func TestSessionIsolationRejectsForgedScopeBeforeAccessingState(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		config := isolationConfig(t, name)
		for _, mutate := range []func(*RunContext){
			func(r *RunContext) { r.Authority = nil }, func(r *RunContext) { r.Authority.PrincipalScopeID = "" }, func(r *RunContext) { r.AgentID = "" },
			func(r *RunContext) { r.Conversation = nil }, func(r *RunContext) { r.Conversation.Source = "caller" },
			func(r *RunContext) { r.Conversation.CurrentRunID = "another-run" }, func(r *RunContext) { r.Conversation.SessionKey = ""; r.Conversation.RootContextID = "forged-fallback" },
		} {
			r := isolationRun("a")
			mutate(&r)
			r.Input = map[string]any{"source": "core", "principal_scope_id": "fake", "session_key": "fake"}
			_, _, err := prepareIsolatedSession(context.Background(), config, r)
			if err == nil || !strings.Contains(err.Error(), "trusted Core") {
				t.Fatalf("forged authority accepted: %v", err)
			}
			if _, err := os.Stat(config.SessionIsolation.Root); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("untrusted identity reached state")
			}
		}
	}
}

func TestSessionIsolationConfigurationAndCredentials(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		c := isolationConfig(t, name)
		for _, change := range []func(*ProviderConfig){
			func(c *ProviderConfig) { c.SessionReuse = false }, func(c *ProviderConfig) { c.SessionStore = "old-map" },
			func(c *ProviderConfig) { c.DelegationTargets = []string{"a"} }, func(c *ProviderConfig) { c.DelegationSocket = "/host/socket" },
			func(c *ProviderConfig) { c.EnvAllowlist = []string{"NODE_OPTIONS"} },
		} {
			bad := c
			change(&bad)
			if _, err := NewProvider(bad); err == nil {
				t.Fatal("unsafe native config accepted")
			}
		}
		env, err := nativeClientEnvironment(c)
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(env, "\n")
		for _, forbidden := range []string{"NODE_OPTIONS", "OPENLINKER_"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("environment leaked %s", forbidden)
			}
		}
		c.Env = []string{"HOME=" + t.TempDir()}
		if _, err := nativeClientEnvironment(c); err != nil {
			t.Fatal("host authentication incorrectly requires a key", err)
		}
	}
}
