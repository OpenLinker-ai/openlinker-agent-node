// Package claudepolicy contains Claude protocol options shared by Node and Plugin.
// It does not read credentials, select a sandbox or start a process.
package claudepolicy

import (
	"encoding/json"
	"slices"
	"strings"
)

// BuiltinTools preserves the operator's tool rules separately from tool
// discovery. For example Bash(git *) enables Bash without broadening its allow
// rule. The caller still passes the original rules through --allowedTools.
func BuiltinTools(rules []string, webSearch bool) []string {
	tools := []string{"Read", "Glob", "Grep"}
	for _, rule := range rules {
		name, _, _ := strings.Cut(strings.TrimSpace(rule), "(")
		if name != "" && !strings.HasPrefix(name, "mcp__") && !slices.Contains(tools, name) {
			tools = append(tools, name)
		}
	}
	if webSearch {
		tools = append(tools, "WebSearch", "WebFetch")
	}
	return tools
}

// Settings disables ambient executable customizations and instruction discovery
// without --bare, which disables the official client's OAuth/Keychain auth.
// Managed administrator policy remains authoritative. Callers add their own
// filesystem/network sandbox policy to this fresh map.
func Settings() map[string]any {
	return map[string]any{
		"disableAllHooks":   true,
		"autoMemoryEnabled": false,
		"claudeMdExcludes":  []string{"/**"},
	}
}

// MCPArgs retains native authentication while loading only explicitly supplied
// MCP servers and built-in tools. Restricted mode ignores user/project/local
// settings, including installed plugin enablement. Skills and Chrome are off.
func MCPArgs(mcp, settings string, tools []string) []string {
	if settings == "" {
		raw, _ := json.Marshal(Settings())
		settings = string(raw)
	}
	return []string{"--restricted", "--no-chrome", "--disable-slash-commands", "--strict-mcp-config", "--mcp-config", mcp, "--permission-prompts", "none", "--settings", settings, "--tools", strings.Join(tools, ",")}
}
