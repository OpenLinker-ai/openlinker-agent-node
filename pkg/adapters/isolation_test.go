package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/sessionsandbox"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func isolationConfig(t *testing.T, provider string) ProviderConfig {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires a non-root POSIX host")
	}
	return ProviderConfig{Provider: provider, Bin: "/" + provider, SessionReuse: true, Timeout: 30 * time.Second,
		Env:              []string{"CODEX_API_KEY=fixture-api-key", "ANTHROPIC_API_KEY=fixture-api-key", "OPENLINKER_AGENT_TOKEN=must-not-reach-client"},
		SessionIsolation: sessionsandbox.Config{Mode: "docker", Root: filepath.Join(t.TempDir(), "private"), Image: "sha256:" + strings.Repeat("a", 64), Namespace: "https://core.example"}}
}

func isolationRun(key string) RunContext {
	return RunContext{RunID: "run-1", AgentID: "agent-1", Input: "fixture:probe",
		Authority:    &openlinker.RuntimeAuthorityContext{PrincipalScopeID: "principal-1"},
		Conversation: &ConversationContext{Source: "core", SessionKey: key, CurrentRunID: "run-1"}}
}

func TestSessionIsolationRejectsUntrustedScopeBeforeStartingClient(t *testing.T) {
	config := isolationConfig(t, "codex")
	for _, test := range []struct {
		name   string
		mutate func(*RunContext)
	}{
		{"missing authority", func(run *RunContext) { run.Authority = nil }},
		{"missing principal", func(run *RunContext) { run.Authority.PrincipalScopeID = "" }},
		{"missing agent", func(run *RunContext) { run.AgentID = "" }},
		{"missing conversation", func(run *RunContext) { run.Conversation = nil }},
		{"caller source", func(run *RunContext) { run.Conversation.Source = "caller" }},
		{"wrong current run", func(run *RunContext) { run.Conversation.CurrentRunID = "other-run" }},
		{"legacy fallback key", func(run *RunContext) { run.Conversation.SessionKey = ""; run.Conversation.RootContextID = "caller-key" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := isolationRun("conversation")
			test.mutate(&run)
			run.Input = map[string]any{"source": "core", "principal_scope_id": "forged", "session_key": "forged"}
			_, _, err := prepareIsolatedSession(context.Background(), config, run)
			if err == nil || !strings.Contains(err.Error(), "trusted Core") {
				t.Fatalf("authority accepted: %v", err)
			}
			if _, err := os.Stat(config.SessionIsolation.Root); !os.IsNotExist(err) {
				t.Fatal("untrusted scope reached storage")
			}
		})
	}
}

func TestSessionIsolationConfigurationAndEnvironment(t *testing.T) {
	config := isolationConfig(t, "codex")
	for _, mutate := range []func(*ProviderConfig){
		func(c *ProviderConfig) { c.SessionReuse = false },
		func(c *ProviderConfig) { c.SessionStore = "old-map.json" },
		func(c *ProviderConfig) { c.DelegationTargets = []string{"target"} },
		func(c *ProviderConfig) { c.DelegationSocket = "/host/socket" },
		func(c *ProviderConfig) { c.EnvAllowlist = []string{"OPENLINKER_AGENT_TOKEN"} },
		func(c *ProviderConfig) { c.EnvAllowlist = []string{"DOCKER_HOST"} },
	} {
		c := config
		mutate(&c)
		if _, err := NewProvider(c); err == nil {
			t.Fatal("unsafe isolation config accepted")
		}
	}
	config.Env = append(config.Env, "HOME=/personal", "CODEX_HOME=/personal/codex", "CLAUDE_CONFIG_DIR=/personal/claude", "PATH=/host/bin", "CUSTOM=value")
	config.EnvAllowlist = []string{"HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "PATH", "CUSTOM"}
	env, err := isolatedEnvironment(config, "codex", true)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"/personal", "/host/bin", "OPENLINKER_AGENT_TOKEN", "ANTHROPIC_API_KEY"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("host env leaked: %s", forbidden)
		}
	}
	for _, required := range []string{"HOME=/session/home", "CODEX_HOME=/session/codex", "CLAUDE_CONFIG_DIR=/session/claude", "CUSTOM=value"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing %s", required)
		}
	}
	config.Env = []string{"HOME=/personal"}
	if _, err := isolatedEnvironment(config, "codex", true); err == nil {
		t.Fatal("OAuth silently imported or missing auth accepted")
	}
	config.Env = []string{"CODEX_API_KEY=secret\nINJECTED=true"}
	if _, err := isolatedEnvironment(config, "codex", true); err == nil {
		t.Fatal("env file injection accepted")
	}
}

type dockerWorkerRequest struct {
	Config ProviderConfig
	Run    dockerWorkerRun
	Output string
}

type dockerWorkerRun struct {
	RunID, AgentID string
	Input          any
	Authority      *openlinker.RuntimeAuthorityContext
	Conversation   *ConversationContext
}

// A separate Node-side OS process for each turn proves that persistence does
// not depend on an in-memory provider, mutex, or protocol fixture instance.
func TestDockerSessionWorkerProcess(t *testing.T) {
	requestFile := os.Getenv("OPENLINKER_TEST_ISOLATED_REQUEST")
	if requestFile == "" {
		t.Skip("subprocess helper")
	}
	raw, err := os.ReadFile(requestFile)
	if err != nil {
		t.Fatal(err)
	}
	var request dockerWorkerRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	provider, err := NewProvider(request.Config)
	if err != nil {
		t.Fatal(err)
	}
	run := request.Run
	result, err := provider.Run(context.Background(), RunContext{RunID: run.RunID, AgentID: run.AgentID, Input: run.Input, Authority: run.Authority, Conversation: run.Conversation})
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

func dockerWorker(t *testing.T, config ProviderConfig, run RunContext) map[string]any {
	t.Helper()
	dir := t.TempDir()
	requestFile := filepath.Join(dir, "request.json")
	output := filepath.Join(dir, "output.json")
	raw, err := json.Marshal(dockerWorkerRequest{config, dockerWorkerRun{run.RunID, run.AgentID, run.Input, run.Authority, run.Conversation}, output})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestDockerSessionWorkerProcess$", "-test.v")
	command.Env = append(os.Environ(), "OPENLINKER_TEST_ISOLATED_REQUEST="+requestFile)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("isolated Node subprocess: %v\n%s", err, out)
	}
	raw, err = os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Output map[string]any `json:"output"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result.Output
}

func dockerConfig(t *testing.T, provider string) ProviderConfig {
	t.Helper()
	image := os.Getenv("OPENLINKER_TEST_SESSION_IMAGE")
	if image == "" {
		t.Skip("run scripts/test-session-isolation.sh for real Docker acceptance")
	}
	config := isolationConfig(t, provider)
	config.SessionIsolation.Image = image
	return config
}

func probeReport(t *testing.T, output map[string]any) map[string]any {
	t.Helper()
	var report map[string]any
	if err := json.Unmarshal([]byte(output["summary"].(string)), &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func assertContainerBoundary(t *testing.T, report map[string]any) {
	t.Helper()
	for _, key := range []string{"symlink_blocked", "docker_socket_blocked", "root_write_blocked", "child_process_works"} {
		if report[key] != true {
			t.Fatalf("boundary probe %s failed: %v", key, report)
		}
	}
	if report["uid"] == float64(0) || report["agent_token"] != "" || report["non_loopback_interfaces"] != float64(0) {
		t.Fatalf("process/environment/network boundary failed: %v", report)
	}
	for path, blocked := range report["blocked"].(map[string]any) {
		if blocked != true {
			t.Fatalf("read escaped container: %s", path)
		}
	}
}

func TestDockerSessionIsolationAndResumeAcrossProcesses(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			config := dockerConfig(t, provider)
			hostFile := filepath.Join(t.TempDir(), "host-private-canary")
			if err := os.WriteFile(hostFile, []byte("host-private"), 0o600); err != nil {
				t.Fatal(err)
			}
			config.EnvAllowlist = []string{"PROBE_DENIED_PATHS", "PROBE_HOST_FILE"}
			config.Env = append(config.Env, "PROBE_HOST_FILE="+hostFile, "PROBE_DENIED_PATHS="+hostFile+"|"+config.SessionIsolation.Root+"/secret")
			if _, err := CheckProviderCLI(context.Background(), config); err != nil {
				t.Fatal(err)
			}
			a := isolationRun("conversation-A")
			a.Input = "fixture:remember"
			first := dockerWorker(t, config, a)
			firstReport := probeReport(t, first)
			assertContainerBoundary(t, firstReport)
			if first[provider+"_session_resumed"] != false {
				t.Fatal("first turn claims resume")
			}
			var memory, privateMap string
			if err := filepath.WalkDir(config.SessionIsolation.Root, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.Name() == "memory" {
					memory = path
				}
				if d.Name() == "native-session.json" {
					privateMap = path
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if memory == "" || privateMap == "" {
				t.Fatal("native data/map was not persisted")
			}
			config.Env[len(config.Env)-1] = "PROBE_DENIED_PATHS=" + hostFile + "|" + memory + "|" + privateMap + "|/session/native-session.json"
			b := isolationRun("conversation-B")
			b.Input = map[string]any{"session_id": firstReport["id"], "query": "fixture:probe"}
			second := dockerWorker(t, config, b)
			secondReport := probeReport(t, second)
			assertContainerBoundary(t, secondReport)
			if secondReport["previous"] != "" || secondReport["id"] == firstReport["id"] || second[provider+"_session_resumed"] != false {
				t.Fatal("B reused A's private session")
			}
			a.RunID = "run-2"
			a.Conversation.CurrentRunID = a.RunID
			a.Input = "fixture:probe"
			a.Authority.RuntimeSessionID = "new-runtime-session"
			a.Authority.RuntimeSessionEpoch = 2
			resumed := dockerWorker(t, config, a)
			resumedReport := probeReport(t, resumed)
			assertContainerBoundary(t, resumedReport)
			if resumedReport["previous"] != "conversation-private-canary" || resumedReport["id"] != firstReport["id"] || resumed[provider+"_session_resumed"] != true {
				t.Fatalf("A did not resume after Node process restart: %v", resumed)
			}
			// Identical conversation strings cannot cross a caller or Agent boundary.
			for _, change := range []string{"principal", "agent", "core", "provider"} {
				other := isolationRun("conversation-A")
				otherConfig := config
				switch change {
				case "principal":
					other.Authority.PrincipalScopeID = "principal-2"
				case "agent":
					other.AgentID = "agent-2"
				case "core":
					otherConfig.SessionIsolation.Namespace = "https://another-core.example"
				case "provider":
					otherConfig.Provider = "claude"
					if provider == "claude" {
						otherConfig.Provider = "codex"
					}
					otherConfig.Bin = "/" + otherConfig.Provider
				}
				report := probeReport(t, dockerWorker(t, otherConfig, other))
				if report["previous"] != "" || report["id"] == firstReport["id"] {
					t.Fatalf("scope collision across %s", change)
				}
			}
			if raw, err := os.ReadFile(hostFile); err != nil || string(raw) != "host-private" {
				t.Fatal("host canary changed")
			}
		})
	}
}

func TestDockerSessionCancellationStopsDescendants(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			config := dockerConfig(t, name)
			provider, err := NewProvider(config)
			if err != nil {
				t.Fatal(err)
			}
			run := isolationRun("cancel")
			run.Input = "fixture:hang"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := provider.Run(ctx, run); result <- err }()
			var heartbeat string
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				_ = filepath.WalkDir(config.SessionIsolation.Root, func(path string, d os.DirEntry, err error) error {
					if err == nil && d.Name() == "heartbeat" {
						heartbeat = path
					}
					return nil
				})
				if heartbeat != "" {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation: %v", err)
				}
			case <-time.After(25 * time.Second):
				t.Fatal("cancelled container did not settle")
			}
			if heartbeat == "" {
				t.Fatal("fixture never launched its child")
			}
			before, err := os.ReadFile(heartbeat)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(200 * time.Millisecond)
			after, err := os.ReadFile(heartbeat)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("container grandchild survived cancellation")
			}
		})
	}
}

// Optional real-client startup probes use existing pinned images. They call
// --version/help only, under the same mount/user/network policy as real turns.
func TestDockerSessionNativeCompatibility(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			image := os.Getenv("OPENLINKER_TEST_NATIVE_" + strings.ToUpper(provider) + "_IMAGE")
			if image == "" {
				t.Skip("set an existing native image ID for compatibility validation")
			}
			config := isolationConfig(t, provider)
			config.SessionIsolation.Image = image
			config.Bin = "/usr/local/bin/" + provider
			version, err := CheckProviderCLI(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("isolated %s version/help passed: %s", provider, version)
		})
	}
}
