package adapters

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexWebSearchPromptRespectsTaskToolRestrictions(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		name, searchMode := "disabled", "disabled"
		if enabled {
			name, searchMode = "enabled", "live"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "codex")
			writeCodexRPCFixture(t, bin, "standard")
			logPrefix := filepath.Join(dir, "rpc")
			provider := CodexProvider{Config: ProviderConfig{
				Bin: bin, Workspace: dir, WebSearch: enabled, Timeout: 5 * time.Second,
				Env:          []string{"CODEX_HOME=" + os.Getenv("CODEX_HOME"), "TEST_LOG=" + logPrefix},
				EnvAllowlist: []string{"TEST_LOG"},
			}}
			if _, err := provider.Run(context.Background(), RunContext{
				RunID: "tool-restricted-task",
				Input: "Find current public information using only the tool I permit; do not search.",
			}); err != nil {
				t.Fatal(err)
			}
			rawPrompt, err := os.ReadFile(logPrefix + ".prompt")
			if err != nil {
				t.Fatal(err)
			}
			prompt := string(rawPrompt)
			for _, instruction := range []string{
				"Live public-web access is enabled",
				"obtain live evidence using a tool permitted by the current user request",
				"Respect explicit tool restrictions: enabling web search makes it available, not mandatory",
				"report that limitation instead of silently substituting a forbidden tool",
				"Do not claim that internet access is unavailable merely because one tool is unavailable",
				"Identify the public source hosts or URLs",
				"private, loopback, link-local, metadata, or credential-bearing destinations",
			} {
				if got := strings.Contains(prompt, instruction); got != enabled {
					t.Errorf("web_search=%t: prompt contains %q = %t", enabled, instruction, got)
				}
			}
			for _, forbidden := range []string{
				"use web search or a permitted public HTTP tool before answering",
				"Do not claim that internet access is unavailable unless an actual web tool attempt fails",
				"isolated Browser tool",
				"Browser observations are valid live evidence",
			} {
				if strings.Contains(prompt, forbidden) {
					t.Errorf("standard prompt contains obsolete or Plugin-owned instruction %q", forbidden)
				}
			}
			args, err := os.ReadFile(logPrefix + ".args")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(args), `web_search="`+searchMode+`"`) {
				t.Fatalf("web-search availability changed: %s", args)
			}
		})
	}
}
