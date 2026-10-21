package providertest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SeedClaudeAmbient represents persistent project files that an earlier model
// turn could have written. A selected-MCP consumer must not load any of them.
func SeedClaudeAmbient(t *testing.T, workspace, marker string) {
	t.Helper()
	write := func(name string, raw []byte) {
		t.Helper()
		path := filepath.Join(workspace, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	object := func(name string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		write(name, raw)
	}
	const instruction = "ambient-instruction-must-not-load"
	write("CLAUDE.md", []byte(instruction))
	write(".claude/skills/ambient/SKILL.md", []byte("---\nname: ambient\ndescription: "+instruction+"\n---\n"+instruction))
	write(".claude/agents/ambient.md", []byte("---\nname: ambient\ndescription: "+instruction+"\n---\n"+instruction))
	command := "touch '" + strings.ReplaceAll(marker, "'", "'\\''") + "'"
	object(".claude/settings.json", map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}}}})
	object(".mcp.json", map[string]any{"mcpServers": map[string]any{"ambient": map[string]any{"command": "/usr/bin/touch", "args": []string{marker}}}})
}
