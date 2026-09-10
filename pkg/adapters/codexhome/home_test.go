package codexhome

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func homeValue(environment []string) string {
	for _, v := range environment {
		if s, ok := strings.CutPrefix(v, "CODEX_HOME="); ok {
			return s
		}
	}
	return ""
}
func TestFreshHomeSharesOnlyCheckedAuthAndOwnedRollouts(t *testing.T) {
	source := t.TempDir()
	for _, name := range []string{"config.toml", "auth.json", "rules", "hooks", "plugins"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first, cleanup, err := Prepare([]string{"CODEX_HOME=" + source, "PATH=/bin"})
	if err != nil {
		t.Fatal(err)
	}
	firstHome := homeValue(first)
	if firstHome == source {
		t.Fatal("original home used")
	}
	for _, name := range []string{"config.toml", "rules", "hooks", "plugins"} {
		if _, err := os.Lstat(filepath.Join(firstHome, name)); !os.IsNotExist(err) {
			t.Fatal("personal configuration imported", name)
		}
	}
	link, err := os.Readlink(filepath.Join(firstHome, "auth.json"))
	if err != nil || link != filepath.Join(source, "auth.json") {
		t.Fatal("native auth refresh lost", link, err)
	}
	if err := os.WriteFile(filepath.Join(firstHome, "sessions", "rollout"), []byte("session"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Stat(firstHome); !os.IsNotExist(err) {
		t.Fatal("private home not cleaned")
	}
	second, cleanup, err := Prepare([]string{"CODEX_HOME=" + source})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if homeValue(second) == firstHome {
		t.Fatal("configuration directory reused")
	}
	if raw, err := os.ReadFile(filepath.Join(homeValue(second), "sessions", "rollout")); err != nil || string(raw) != "session" {
		t.Fatal("session not retained", err)
	}
}
func TestHomeRejectsUnsafeNativeFilesAndState(t *testing.T) {
	for _, kind := range []string{"auth-mode", "auth-symlink", "state-symlink", "state-mode"} {
		t.Run(kind, func(t *testing.T) {
			source := t.TempDir()
			auth := filepath.Join(source, "auth.json")
			state := filepath.Join(source, "openlinker-rpc-v1")
			switch kind {
			case "auth-mode":
				_ = os.WriteFile(auth, []byte("fake"), 0o644)
			case "auth-symlink":
				_ = os.Symlink(filepath.Join(t.TempDir(), "auth"), auth)
			case "state-symlink":
				_ = os.Symlink(t.TempDir(), state)
			case "state-mode":
				_ = os.Mkdir(state, 0o755)
			}
			_, cleanup, err := Prepare([]string{"CODEX_HOME=" + source})
			if cleanup != nil {
				cleanup()
			}
			if err == nil {
				t.Fatal("unsafe home accepted")
			}
		})
	}
}

func TestAPIKeyDoesNotReplaceNativeLogin(t *testing.T) {
	source := t.TempDir()
	original := filepath.Join(source, "auth.json")
	if err := os.WriteFile(original, []byte("original native login"), 0o600); err != nil {
		t.Fatal(err)
	}
	env, cleanup, err := Prepare([]string{"CODEX_HOME=" + source, "CODEX_API_KEY=test-only-key"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	info, err := os.Lstat(filepath.Join(homeValue(env), "auth.json"))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatal("private authentication permissions", err)
	}
	raw, _ := os.ReadFile(original)
	if string(raw) != "original native login" {
		t.Fatal("native login was overwritten")
	}
}
