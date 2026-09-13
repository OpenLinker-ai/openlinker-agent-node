package adapters

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestModelEndpointRejectsAmbiguousOrCredentialBearingURLs(t *testing.T) {
	for _, endpoint := range []string{"https://gateway.example", "https://gateway.example/", "https://gateway.example:443/proxy/v1", "https://GATEWAY.example/anthropic/"} {
		if err := ValidateModelEndpoint(endpoint); err != nil {
			t.Fatalf("valid endpoint rejected: %v", err)
		}
	}
	for _, endpoint := range []string{"", "http://gateway.example", "https://gateway.example:8443", "https://gateway.example:", "https://secret@gateway.example", "https://gateway.example?key=secret", "https://gateway.example?", "https://gateway.example#secret", "https://gateway.example/v1%2fadmin", "https://gateway.example/../admin", "https://gateway.example/./v1", "https://gateway.example//v1", "https://gateway.example/v1//", "https://127.0.0.1", "https://[::1]", "https://gateway.local", "https://gateway.localhost", "https://gateway", "https://gateway.example.", "https://bad_host.example", "https://gateway.example/\nsecret", "https://gateway.example/\\evil.test"} {
		err := ValidateModelEndpoint(endpoint)
		if err == nil {
			t.Fatalf("invalid endpoint accepted: %q", endpoint)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("diagnostic echoed credential")
		}
	}
}

func TestNativeModelEndpointRequiresExplicitNetworkGrant(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		c := isolationConfig(t, name)
		c.CodexBaseURL, c.ClaudeBaseURL = "https://gateway.example/vendor/v1", "https://gateway.example/vendor"
		for _, domains := range [][]string{nil, {"other.example"}, {"gateway.example.evil.test"}} {
			c.SessionIsolation.AllowedDomains = domains
			if _, err := NewProvider(c); err == nil || !strings.Contains(err.Error(), "SESSION_NETWORK_DOMAINS") {
				t.Fatalf("endpoint grant not enforced: %v", err)
			}
		}
		for _, domains := range [][]string{{"gateway.example"}, {"gateway.example:443"}} {
			c.SessionIsolation.AllowedDomains = domains
			if _, err := NewProvider(c); err != nil {
				t.Fatal(err)
			}
		}
		c.Env = append(c.Env, "ANTHROPIC_BASE_URL=https://ambient.example", "OPENAI_BASE_URL=https://ambient.example")
		env, err := isolatedEnvironment(c)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.Join(env, "\n"), "ambient.example") {
			t.Fatal("ambient gateway inherited")
		}
		if name == "claude" && !strings.Contains(strings.Join(env, "\n"), "ANTHROPIC_BASE_URL="+c.ClaudeBaseURL) {
			t.Fatal("explicit Claude gateway lost")
		}
	}
}

func TestClaudeGatewayReachesLaunchedProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fixture")
	}
	bin, dir := reviewFakeCLI(t, `cat >/dev/null
printf '%s' "$ANTHROPIC_BASE_URL" > endpoint
printf '%s\n' '{"type":"result","subtype":"success","result":"ok"}'
`)
	const endpoint = "https://gateway.example/anthropic"
	p, err := NewProvider(ProviderConfig{Provider: "claude", Bin: bin, Workspace: dir, ClaudeBaseURL: endpoint,
		Env: []string{"PATH=/usr/bin:/bin", "ANTHROPIC_BASE_URL=https://ambient.example"}, EnvAllowlist: []string{"ANTHROPIC_BASE_URL"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Run(context.Background(), RunContext{RunID: "gateway-fixture", Input: "fixture"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "endpoint"))
	if err != nil || string(got) != endpoint {
		t.Fatalf("launched gateway = %q: %v", got, err)
	}
}

func TestCodexGatewayPreservesFullResponsesBasePath(t *testing.T) {
	const endpoint = "https://gateway.example/proxy/openai/v1"
	args := strings.Join(codexLaunchConfiguration(ProviderConfig{CodexBaseURL: endpoint}, "workspace-write"), "\n")
	for _, value := range []string{`model_providers.openlinker_proxy.base_url="` + endpoint + `"`, `model_providers.openlinker_proxy.env_key="CODEX_API_KEY"`, `model_providers.openlinker_proxy.supports_websockets=false`} {
		if !strings.Contains(args, value) {
			t.Fatalf("gateway launch option lost: %s", value)
		}
	}
}
