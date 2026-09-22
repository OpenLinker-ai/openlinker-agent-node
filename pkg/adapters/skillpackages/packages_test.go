package skillpackages

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSkillPackageMaterializationIsConcurrentAndConfined(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	files := map[string]string{"SKILL.md": "instructions", "refs/a.txt": "reference"}
	var wg sync.WaitGroup
	failures := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() { defer wg.Done(); failures <- materialize(root, "packages/version", files, false) }()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, "packages/version/SKILL.md"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "packages/version/SKILL.md"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := materialize(root, "packages/version", files, false); err == nil {
		t.Fatal("silently overwrote tampered package")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Skip(err)
	}
	if err := materialize(root, "escape/version", files, false); err == nil {
		t.Fatal("followed a symlink out of the workspace")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("wrote outside workspace")
	}
}

func TestSkillPackageCacheStaysOutOfGit(t *testing.T) {
	for _, nested := range []bool{false, true} {
		repo := t.TempDir()
		git := func(args ...string) string {
			t.Helper()
			out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
			if err != nil {
				t.Fatalf("git failed: %s %v", out, err)
			}
			return string(out)
		}
		git("init", "--quiet")
		workspace := repo
		if nested {
			workspace = filepath.Join(repo, "nested")
			if err := os.MkdirAll(workspace, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := protectSkillPackageCache(context.Background(), workspace); err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(workspace)
		if err != nil {
			t.Fatal(err)
		}
		err = materialize(root, ".openlinker-skills/agent/digest", map[string]string{"SKILL.md": "PRIVATE"}, false)
		root.Close()
		if err != nil {
			t.Fatal(err)
		}
		git("add", "-A")
		if got := git("ls-files"); got != "" {
			t.Fatalf("private package entered index: %s", got)
		}
		if err := os.WriteFile(filepath.Join(repo, "user.txt"), []byte("user"), 0600); err != nil {
			t.Fatal(err)
		}
		git("add", "-A")
		if got := git("ls-files"); strings.TrimSpace(got) != "user.txt" {
			t.Fatalf("unrelated files affected: %s", got)
		}
	}
}
