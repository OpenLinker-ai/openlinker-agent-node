package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/internal/agentnode"
	agentexec "github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/agentdelegation"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/agenthost"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

const transportParent = "11111111-1111-4111-8111-111111111111"
const transportTarget = "22222222-2222-4222-8222-222222222222"
const transportChild = "33333333-3333-4333-8333-333333333333"

func buildTransportFixture(t *testing.T, name, source string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), name)
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binary, source)
	build.Env = append(os.Environ(), "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, output)
	}
	return binary
}

func transportEnv(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	env := []string{"HOME=" + home, "USERPROFILE=" + home, "PATH=" + os.Getenv("PATH")}
	if runtime.GOOS == "windows" {
		env = append(env, "SystemRoot="+os.Getenv("SystemRoot"))
	}
	return env
}

func TestCompiledNodeOwnsDelegationTransport(t *testing.T) {
	binary := buildTransportFixture(t, "agent-node", ".")
	t.Run("capabilities without configuration or startup", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, "plugin", "capabilities")
		state := filepath.Join(t.TempDir(), "must-not-create")
		command.Env = append(transportEnv(t), "OPENLINKER_AGENT_NODE_ADAPTER=invalid", "OPENLINKER_AGENT_NODE_DATA_DIR="+state)
		var stderr strings.Builder
		command.Stderr = &stderr
		output, err := command.Output()
		var capabilities agenthost.Capabilities
		if err != nil || stderr.Len() != 0 || json.Unmarshal(output, &capabilities) != nil ||
			capabilities.Protocol != "openlinker.agent-host.v1" || capabilities.BrowserProxy || !capabilities.DelegationProxy {
			t.Fatalf("Node handshake: %v %s %s", err, output, stderr.String())
		}
		if _, err := os.Stat(state); !os.IsNotExist(err) {
			t.Fatal("capabilities created Node state", err)
		}
		if resolved, err := agenthost.Resolve(ctx, binary, "delegation_proxy"); err != nil || resolved != binary {
			t.Fatal("production host resolution failed", err)
		}
		if _, err := agenthost.Resolve(ctx, binary, "browser_proxy"); err == nil {
			t.Fatal("Node advertised Browser support")
		}
	})
	t.Run("reject unsupported commands and missing socket", func(t *testing.T) {
		for _, args := range [][]string{
			{"plugin", "capabilities", "extra"}, {"plugin", "browser-proxy", "--host", "codex"},
			{"plugin", "delegation-proxy"}, {"plugin", "delegation-proxy", "--host", "invalid"},
			{"plugin", "delegation-proxy", "--host", "claude", "extra"},
			{"plugin", "delegation-proxy", "--host", "claude"},
		} {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			command := exec.CommandContext(ctx, binary, args...)
			command.Env = append(transportEnv(t), "OPENLINKER_AGENT_TOKEN=must-not-leak", "ANTHROPIC_API_KEY=must-not-leak")
			output, err := command.CombinedOutput()
			cancel()
			if err == nil || strings.Contains(string(output), "must-not-leak") {
				t.Fatalf("invalid command accepted or leaked a credential: %v %s", err, output)
			}
		}
	})
	if runtime.GOOS == "windows" {
		t.Skip("native delegation requires POSIX private sockets")
	}
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider+" MCP round trip", func(t *testing.T) { testNodeMCPRoundTrip(t, binary, provider) })
	}
	claude := buildTransportFixture(t, "claude", "./testdata/native-client")
	codex := buildTransportFixture(t, "codex", "./testdata/native-client")
	t.Run("default self host reaches Worker validation", func(t *testing.T) {
		for _, provider := range []string{"codex", "claude"} {
			client := codex
			if provider == "claude" {
				client = claude
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			command := exec.CommandContext(ctx, binary)
			command.Dir = t.TempDir()
			command.Env = append(transportEnv(t),
				"OPENLINKER_AGENT_NODE_ADAPTER="+provider,
				"OPENLINKER_AGENT_NODE_"+strings.ToUpper(provider)+"_BIN="+client,
				`OPENLINKER_AGENT_NODE_DELEGATION_TARGETS=["`+transportTarget+`"]`,
				"ANTHROPIC_API_KEY=synthetic-delegation-key")
			// No override host, Core URL or credentials: the first downstream
			// SDK error proves CLI preflight and self-host discovery succeeded.
			output, err := command.CombinedOutput()
			cancel()
			if err == nil || !strings.Contains(string(output), "OpenLinker address is required") {
				t.Fatalf("%s default host failed before Worker validation: %v %s", provider, err, output)
			}
		}
	})
	t.Run("OAuth-only delegation fails before any provider process", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary)
		command.Env = append(transportEnv(t), "OPENLINKER_AGENT_NODE_ADAPTER=claude",
			"OPENLINKER_AGENT_NODE_CLAUDE_BIN="+filepath.Join(t.TempDir(), "must-not-execute"),
			`OPENLINKER_AGENT_NODE_DELEGATION_TARGETS=["`+transportTarget+`"]`, "CLAUDE_CODE_OAUTH_TOKEN=synthetic-oauth-only")
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "--bare") || !strings.Contains(string(output), "ANTHROPIC_API_KEY") || strings.Contains(string(output), "synthetic-oauth-only") {
			t.Fatalf("startup did not enforce API auth: %v %s", err, output)
		}
	})
	t.Run("ordinary Claude keeps native authentication", func(t *testing.T) {
		adapter := &agentnode.NativeAdapter{Config: agentexec.ProviderConfig{Provider: "claude", Bin: claude, Env: transportEnv(t)}}
		if err := adapter.Preflight(context.Background()); err != nil {
			t.Fatal("ordinary Claude now requires API key", err)
		}
	})
	for _, source := range []string{"environment", "file"} {
		t.Run("validated "+source+" key reaches delegated Run", func(t *testing.T) {
			env := append(transportEnv(t), "OPENLINKER_AGENT_TOKEN=must-not-reach-provider")
			if source == "file" {
				file := filepath.Join(t.TempDir(), "key")
				if err := os.WriteFile(file, []byte("synthetic-delegation-key\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				env = append(env, "ANTHROPIC_API_KEY_FILE="+file)
			} else {
				env = append(env, "ANTHROPIC_API_KEY=synthetic-delegation-key")
			}
			adapter := &agentnode.NativeAdapter{Config: agentexec.ProviderConfig{Provider: "claude", Bin: claude,
				Workspace: t.TempDir(), Env: env, EnvAllowlist: []string{"ANTHROPIC_API_KEY_FILE"},
				DelegationTargets: []string{transportTarget}, DelegationProxyBin: binary}}
			if err := adapter.Preflight(context.Background()); err != nil {
				t.Fatal(err)
			}
			callbacks := successfulDelegationCallbacks(nil)
			result, err := adapter.Run(context.Background(), "task", agentnode.RunContext{RunID: transportParent,
				ReadDelegatedRun: callbacks.ReadRun,
				CallAgent: func(ctx context.Context, target string, input any, options agentnode.CallAgentOptions) (any, error) {
					return callbacks.CallAgent(ctx, target, input, openlinker.RuntimeCallOptions{IdempotencyKey: options.IdempotencyKey})
				}})
			encoded, _ := json.Marshal(result)
			if err != nil || !strings.Contains(string(encoded), "validated delegated Claude execution") || strings.Contains(string(encoded), "synthetic-delegation-key") {
				t.Fatalf("delegated execution: %v %s", err, encoded)
			}
		})
	}
}

func successfulDelegationCallbacks(calls *atomic.Int32) agentdelegation.Callbacks {
	return agentdelegation.Callbacks{
		CallAgent: func(context.Context, string, any, openlinker.RuntimeCallOptions) (any, error) {
			if calls != nil {
				calls.Add(1)
			}
			return openlinker.RuntimeRunSummary{RunID: transportChild, Status: openlinker.RuntimeRunRunning, DispatchState: openlinker.RuntimeDispatchPending}, nil
		},
		ReadRun: func(context.Context, string) (*openlinker.RuntimeDelegatedRun, error) {
			return &openlinker.RuntimeDelegatedRun{RuntimeRunSummary: openlinker.RuntimeRunSummary{RunID: transportChild, Status: openlinker.RuntimeRunSuccess, DispatchState: openlinker.RuntimeDispatchTerminal}, Output: map[string]any{"summary": "delegated result"}}, nil
		},
	}
}

func testNodeMCPRoundTrip(t *testing.T, binary, provider string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var calls atomic.Int32
	broker, err := agentdelegation.Start(ctx, "", transportParent, []string{transportTarget}, successfulDelegationCallbacks(&calls))
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	command := exec.CommandContext(ctx, binary, agenthost.DelegationProxyArguments(provider)...)
	command.Env = append(transportEnv(t), agentdelegation.SocketEnvironment+"="+broker.Socket, "OPENLINKER_AGENT_NODE_ADAPTER=invalid")
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	scanner := bufio.NewScanner(output)
	request := func(method string, params any) map[string]any {
		t.Helper()
		if err := json.NewEncoder(input).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		if !scanner.Scan() {
			t.Fatalf("missing %s response: %v", method, scanner.Err())
		}
		var response map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response["error"] != nil {
			t.Fatalf("%s failed: %v", method, response)
		}
		return response["result"].(map[string]any)
	}
	request("initialize", map[string]any{})
	if listed := request("tools/list", map[string]any{}); len(listed["tools"].([]any)) != len(agentdelegation.ToolNames) {
		t.Fatal(listed)
	}
	params := map[string]any{"name": "delegate_agent", "arguments": map[string]any{"target_agent_id": transportTarget, "input": map[string]any{"prompt": "review"}, "request_key": "review-1"}}
	for i := 0; i < 2; i++ {
		if result := request("tools/call", params); result["isError"] == true {
			t.Fatal(result)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("delegation replay created %d children", calls.Load())
	}
	result := request("tools/call", map[string]any{"name": "wait_delegated_run", "arguments": map[string]any{"child_run_id": transportChild, "wait_ms": 100}})
	encoded, _ := json.Marshal(result)
	if !strings.Contains(string(encoded), "delegated result") {
		t.Fatal(result)
	}
	if err := broker.EnsureComplete(); err != nil {
		t.Fatal(err)
	}
	input.Close()
	io.Copy(io.Discard, output)
	if err := command.Wait(); err != nil || stderr.Len() != 0 {
		t.Fatalf("proxy did not exit cleanly on EOF: %v %s", err, stderr.String())
	}
}
