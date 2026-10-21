package claudepolicy

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestMCPKeepsNativeAuthAndControlledConfiguration(t *testing.T) {
	args := MCPArgs(`{"mcpServers":{}}`, "", BuiltinTools([]string{"Bash(git *)", "Read", "mcp__private__write"}, true))
	for _, bad := range []string{"--bare", "--safe-mode", "--dangerously-skip-permissions", "--plugin-dir"} {
		if slices.Contains(args, bad) {
			t.Fatal("unsafe mode:", bad)
		}
	}
	for _, required := range []string{"--restricted", "--strict-mcp-config", "--no-chrome", "--disable-slash-commands"} {
		if !slices.Contains(args, required) {
			t.Fatal("missing restriction:", required)
		}
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(args[slices.Index(args, "--settings")+1]), &settings); err != nil {
		t.Fatal(err)
	}
	if settings["disableAllHooks"] != true || settings["autoMemoryEnabled"] != false || settings["claudeMdExcludes"].([]any)[0] != "/**" {
		t.Fatal(settings)
	}
	if args[slices.Index(args, "--tools")+1] != "Read,Glob,Grep,Bash,WebSearch,WebFetch" {
		t.Fatal("tool discovery must not contain permission patterns or ambient MCP", args)
	}
}
