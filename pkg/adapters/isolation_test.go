package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
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
		Env:              []string{"PATH=" + os.Getenv("PATH"), "CODEX_API_KEY=fixture-key", "ANTHROPIC_API_KEY=fixture-key", "OPENLINKER_AGENT_TOKEN=private-platform-canary", "NODE_OPTIONS=must-not-inherit", "HOME=/personal"},
		SessionIsolation: sessionsandbox.Config{Mode: "native", Root: filepath.Join(t.TempDir(), "private"), Namespace: "https://core.example", RuntimeBin: os.Getenv("OPENLINKER_TEST_NATIVE_SANDBOX_BIN")}}
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
		env, err := isolatedEnvironment(c)
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(env, "\n")
		for _, forbidden := range []string{"NODE_OPTIONS", "OPENLINKER_", "/personal"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("environment leaked %s", forbidden)
			}
		}
		c.Env = []string{"HOME=/personal"}
		if _, err := isolatedEnvironment(c); err == nil {
			t.Fatal("OAuth silently imported")
		}
	}
}

type isolationWorkerRequest struct {
	Config         ProviderConfig
	RunID, AgentID string
	Input          any
	Authority      *openlinker.RuntimeAuthorityContext
	Conversation   *ConversationContext
	Output         string
}

func TestNativeSessionWorkerProcess(t *testing.T) {
	path := os.Getenv("OPENLINKER_TEST_NATIVE_WORKER_REQUEST")
	if path == "" {
		t.Skip("worker subprocess helper")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var request isolationWorkerRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	p, err := NewProvider(request.Config)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Run(context.Background(), RunContext{RunID: request.RunID, AgentID: request.AgentID, Input: request.Input, Authority: request.Authority, Conversation: request.Conversation})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request.Output, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func nativeWorker(t *testing.T, c ProviderConfig, r RunContext) map[string]any {
	t.Helper()
	dir := t.TempDir()
	request := filepath.Join(dir, "request.json")
	output := filepath.Join(dir, "output.json")
	raw, err := json.Marshal(isolationWorkerRequest{c, r.RunID, r.AgentID, r.Input, r.Authority, r.Conversation, output})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-test.run=^TestNativeSessionWorkerProcess$", "-test.v")
	cmd.Env = append(os.Environ(), "OPENLINKER_TEST_NATIVE_WORKER_REQUEST="+request)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Node-side subprocess: %v\n%s", err, out)
	}
	raw, err = os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var result struct{ Output map[string]any }
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result.Output
}

func fixtureReport(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	var report map[string]any
	summary, ok := result["summary"].(string)
	if !ok {
		t.Fatalf("missing summary: %#v", result)
	}
	if err := json.Unmarshal([]byte(summary), &report); err != nil {
		t.Fatal(err)
	}
	if result["session_isolation"] != "native" {
		t.Fatal("missing isolation evidence")
	}
	return report
}

func TestNativeSandboxRealProviderResumeAndHostProtection(t *testing.T) {
	if os.Getenv("OPENLINKER_TEST_NATIVE_SANDBOX_BIN") == "" {
		t.Skip("set OPENLINKER_TEST_NATIVE_SANDBOX_BIN for real OS acceptance")
	}
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			c := isolationConfig(t, name)
			c.Bin = filepath.Join(t.TempDir(), name)
			build := exec.Command("go", "build", "-o", c.Bin, "./testdata/session-client")
			build.Env = append(os.Environ(), "GOWORK=off")
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build protocol peer: %v %s", err, out)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			if err := CheckSessionIsolation(ctx, c); err != nil {
				t.Fatal(err)
			}
			if _, err := CheckProviderCLI(ctx, c); err != nil {
				t.Fatal(err)
			}
			a := isolationRun("a")
			a.Input = "fixture:remember"
			firstResult := nativeWorker(t, c, a)
			first := fixtureReport(t, firstResult)
			if first["previous"] != "" {
				t.Fatal("new A inherited history")
			}
			b := isolationRun("b")
			second := fixtureReport(t, nativeWorker(t, c, b))
			if second["previous"] != "" || first["id"] == second["id"] {
				t.Fatal("B inherited A native history")
			}
			a.Input = "fixture:probe"
			a.RunID = "run-2"
			a.Conversation.CurrentRunID = a.RunID
			a.Authority.RuntimeSessionID = "new-runtime-session"
			a.Authority.RuntimeSessionEpoch = 2
			thirdResult := nativeWorker(t, c, a)
			third := fixtureReport(t, thirdResult)
			if first["id"] != third["id"] || third["previous"] != "conversation-private-canary" || thirdResult[name+"_session_resumed"] != true {
				t.Fatal("A-B-A resume failed across Node process/runtime epoch")
			}
			host := filepath.Join(t.TempDir(), "private-canary")
			if err := os.WriteFile(host, []byte("host-private"), 0o600); err != nil {
				t.Fatal(err)
			}
			adir := filepath.Dir(first["home"].(string))
			bdir := filepath.Dir(second["home"].(string))
			denied := []string{host, filepath.Join(adir, "workspace/memory"), filepath.Join(adir, name, "native-id"), filepath.Join(filepath.Dir(bdir), "native-session.json"), "/var/run/docker.sock"}
			spec, _ := json.Marshal(map[string]any{"Denied": denied, "Host": host})
			b.Input = "fixture_spec=" + base64.StdEncoding.EncodeToString(spec)
			probed := fixtureReport(t, nativeWorker(t, c, b))
			for target, blocked := range probed["blocked"].(map[string]any) {
				if blocked != true {
					t.Fatalf("client read forbidden path %s", target)
				}
			}
			for _, field := range []string{"symlink_blocked", "root_write_blocked", "child_process_works"} {
				if probed[field] != true {
					t.Fatalf("failed %s: %#v", field, probed)
				}
			}
			if probed["agent_token"] != "" || probed["loader_env"] != "" {
				t.Fatal("platform or loader env inherited")
			}
			if name == "claude" && probed["bare"] != true {
				t.Fatal("isolated Claude didn't use bare mode")
			}
		})
	}
}

// Optional no-model check of installed official clients. This does not use
// credentials or claim model/WebSearch acceptance.
func TestNativeSandboxInstalledProviderCLI(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			bin := os.Getenv("OPENLINKER_TEST_NATIVE_" + strings.ToUpper(name) + "_BIN")
			if bin == "" {
				t.Skip("installed-client check not requested")
			}
			c := isolationConfig(t, name)
			c.Bin = bin
			if raw := os.Getenv("OPENLINKER_TEST_NATIVE_READ_PATHS"); raw != "" {
				if err := json.Unmarshal([]byte(raw), &c.SessionIsolation.ReadPaths); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			version, err := CheckProviderCLI(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("sandboxed %s CLI compatibility: %s", name, version)
		})
	}
}

func TestNativeSandboxCredentialReadThroughRunOutput(t *testing.T) {
	if os.Getenv("OPENLINKER_TEST_NATIVE_SANDBOX_BIN") == "" {
		t.Skip("set OPENLINKER_TEST_NATIVE_SANDBOX_BIN for real OS acceptance")
	}
	if runtime.GOOS == "darwin" {
		// Establish that the same OS binary works outside the sandbox. On some
		// macOS versions Seatbelt rejects ps at exec, before even -L can run.
		keywords, err := exec.Command("/bin/ps", "-L").Output()
		if err != nil || !strings.Contains(string(keywords), "command") {
			t.Fatal("host ps positive control failed")
		}
	}
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			c := isolationConfig(t, name)
			c.Bin = filepath.Join(t.TempDir(), name)
			build := exec.Command("go", "build", "-o", c.Bin, "./testdata/session-client")
			build.Env = append(os.Environ(), "GOWORK=off")
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build credential peer: %v %s", err, out)
			}
			// Synthetic per-test value: no installed client or real credential is read.
			canary := "test-only-parent-key-" + strings.ReplaceAll(t.Name(), "/", "-")
			key := "CODEX_API_KEY"
			if name == "claude" {
				key = "ANTHROPIC_API_KEY"
			}
			c.Env = []string{"PATH=" + os.Getenv("PATH"), key + "=" + canary}
			r := isolationRun("credential-audit")
			raw, _ := json.Marshal(map[string]any{"Credentials": true})
			r.Input = "fixture_spec=" + base64.StdEncoding.EncodeToString(raw)
			report := fixtureReport(t, nativeWorker(t, c, r))
			probe, ok := report["credential_probe"].(map[string]any)
			deniedAtExec := runtime.GOOS == "darwin" && probe["exec_permission_denied"] == true && probe["probe_executable_present"] == true
			if !ok || probe["child_has_key"] != false || probe["positive_control"] != true && !deniedAtExec {
				t.Fatalf("credential probe lacked a clean child or positive control: %#v", probe)
			}
			digest, ok := probe["parent_key_sha256"].(string)
			if !ok {
				t.Fatal("missing credential probe result")
			}
			if digest != "" {
				sum := sha256.Sum256([]byte(canary))
				if digest != hex.EncodeToString(sum[:]) || probe["read_permitted"] != true {
					t.Fatal("unexpected parent credential evidence")
				}
				t.Logf("KNOWN EXPOSURE: %s child with no API key read the parent's key via %s; digest reached Run output", runtime.GOOS, probe["method"])
			} else {
				t.Logf("%s: no parent key recovered via %s (read permitted=%v, exec denied=%v); this does not establish credential secrecy", runtime.GOOS, probe["method"], probe["read_permitted"], deniedAtExec)
			}
		})
	}
}
