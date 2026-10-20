package providertest

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/agentdelegation"
)

// WriteMCPFixture starts the consumer's own test binary as an installed client
// and transport host. Its provider must actually inject and use the stdio MCP
// server; merely placing tool text in the prompt cannot pass this fixture.
func WriteMCPFixture(t *testing.T, path, provider string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := "#!/bin/sh\nif [ \"$*\" = \"plugin capabilities\" ]; then printf '%s\\n' '{\"protocol\":\"openlinker.agent-host.v1\",\"browser_proxy\":true,\"delegation_proxy\":true}'; exit 0; fi\nexport OPENLINKER_MCP_FIXTURE=" + quote(provider) + "\nexec " + quote(binary) + " -test.run=TestMCPProviderFixtureProcess -- \"$@\"\n"
	if err = os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}
func MCPFixtureProcess() {
	provider := os.Getenv("OPENLINKER_MCP_FIXTURE")
	if provider == "" {
		return
	}
	fail := func(err any) { fmt.Fprintln(os.Stderr, err); os.Exit(2) }
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) > 0 && args[0] == "plugin" {
		if err := agentdelegation.Proxy(context.Background(), os.Stdin, os.Stdout, os.Getenv(agentdelegation.SocketEnvironment)); err != nil {
			fail(err)
		}
		os.Exit(0)
	}
	var rpc *RPCFixture
	if provider == "codex" {
		dir := filepath.Join(os.Getenv("HOME"), ".codex/tmp/arg0/mcp-fixture")
		_ = os.MkdirAll(dir, 0700)
		binary, _ := os.Executable()
		_ = os.Symlink(binary, filepath.Join(dir, "codex-linux-sandbox"))
		rpc = StartRPCFixture("mcp-binding")
	} else {
		_, _ = io.ReadAll(os.Stdin)
	}
	binary := ""
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, "mcp_servers.openlinker_delegation.command="); ok {
			_ = json.Unmarshal([]byte(v), &binary)
		}
		if a == "--mcp-config" && i+1 < len(args) {
			var cfg struct {
				Servers map[string]struct {
					Command string `json:"command"`
				} `json:"mcpServers"`
			}
			_ = json.Unmarshal([]byte(args[i+1]), &cfg)
			binary = cfg.Servers["openlinker_delegation"].Command
		}
	}
	summary := "no selected MCP tools"
	if binary != "" {
		for _, a := range args {
			if a == "--safe-mode" {
				fail("safe-mode disables selected MCP tools")
			}
		}
		command := exec.Command(binary, "plugin", "delegation-proxy", "--host", provider)
		command.Stderr = os.Stderr
		input, _ := command.StdinPipe()
		output, _ := command.StdoutPipe()
		if err := command.Start(); err != nil {
			fail(err)
		}
		scanner := bufio.NewScanner(output)
		call := func(method string, params any) map[string]any {
			_ = json.NewEncoder(input).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
			if !scanner.Scan() {
				fail("missing MCP response")
			}
			var reply map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &reply); err != nil {
				fail(err)
			}
			if reply["error"] != nil {
				fail(reply)
			}
			return reply["result"].(map[string]any)
		}
		call("initialize", map[string]any{})
		listed := call("tools/list", map[string]any{})["tools"].([]any)
		if len(listed) != 1 {
			fail("unexpected unselected tools")
		}
		name := listed[0].(map[string]any)["name"].(string)
		response := call("tools/call", map[string]any{"name": name, "arguments": map[string]any{"arguments": map[string]any{"text": "lookup"}, "request_key": "fixture-intent"}})
		raw, _ := json.Marshal(response)
		if response["isError"] == true || !strings.Contains(string(raw), "TOOL-MARKER") {
			fail(response)
		}
		_ = input.Close()
		if err := command.Wait(); err != nil {
			fail(err)
		}
		summary = "used TOOL-MARKER"
	}
	if rpc != nil {
		rpc.Finish(summary)
	} else {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "result", "subtype": "success", "result": summary, "session_id": FixtureThread})
	}
	os.Exit(0)
}
