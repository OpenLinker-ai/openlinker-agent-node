package adapters

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostAuthSessionStorageAndCredentials(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		c := isolationConfig(t, provider)
		home := t.TempDir()
		c.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "CODEX_HOME=" + filepath.Join(home, "codex-auth"), "CLAUDE_CONFIG_DIR=" + filepath.Join(home, "claude-auth")}
		paths := map[string]string{}
		for _, key := range []string{"a", "b", "a"} {
			p, close, err := prepareIsolatedSession(context.Background(), c, isolationRun(key))
			if err != nil {
				t.Fatal(err)
			}
			if prev := paths[key]; prev != "" && p.Workspace != prev {
				t.Fatal("scope did not persist")
			}
			paths[key] = p.Workspace
			cmd := nativeHostCommand(context.Background(), p, provider, nil)
			if !strings.Contains(strings.Join(cmd.Env, "\n"), "HOME="+home+"\n") {
				t.Fatal("host auth HOME replaced")
			}
			if strings.Contains(strings.Join(cmd.Env, "\n"), "API_KEY=") {
				t.Fatal("introduced a mandatory/invented key")
			}
			if _, _, err := prepareIsolatedSession(context.Background(), c, isolationRun(key)); err == nil {
				t.Fatal("same session can run concurrently")
			}
			if err := close(); err != nil {
				t.Fatal(err)
			}
		}
		if paths["a"] == paths["b"] {
			t.Fatal("two conversations share a workspace")
		}
		for _, unsafe := range []string{home, filepath.Dir(c.SessionIsolation.Root), c.SessionIsolation.Root} {
			bad := c
			bad.SessionIsolation.ReadPaths = []string{unsafe}
			if _, close, err := prepareIsolatedSession(context.Background(), bad, isolationRun("blocked")); err == nil {
				_ = close()
				t.Fatal("unsafe read grant accepted")
			}
		}
		entries, err := os.ReadDir(home)
		if err != nil || len(entries) != 0 {
			t.Fatal("Node modified host authentication state")
		}
		bad := c
		key := "CODEX_HOME"
		if provider == "claude" {
			key = "CLAUDE_CONFIG_DIR"
		}
		bad.Env = append(append([]string{}, c.Env...), key+"=/usr/lib/openlinker-auth-fixture")
		if _, close, err := prepareIsolatedSession(context.Background(), bad, isolationRun("unsafe-auth-location")); err == nil {
			_ = close()
			t.Fatal("authentication under an implicitly readable code root was accepted")
		}
	}
}
