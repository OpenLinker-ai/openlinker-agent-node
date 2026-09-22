package skillpackages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Windows represents an owner-read-only file as 0444 rather than Unix 0400.
// Reusing verified private files must work on every native Host platform.
func TestLoadReusesPrivateFiles(t *testing.T) {
	payload, err := json.Marshal(Contents{Name: "portable", Providers: []string{"codex"}, Files: map[string]string{"SKILL.md": "PRIVATE-INSTRUCTION", "references/input.txt": "reference"}})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	version := Version{BindingID: "11111111-1111-4111-8111-111111111111", PackageID: "22222222-2222-4222-8222-222222222222", VersionID: "33333333-3333-4333-8333-333333333333", Digest: hex.EncodeToString(digest[:]), Payload: string(payload)}
	loaded := 0
	req := Request{AgentID: "55555555-5555-4555-8555-555555555555", Trusted: true, Snapshot: Snapshot{Schema: 1, Bundles: []Version{version}}, Emit: func(kind string, _ any) error {
		if kind == "run.skill_packages.loaded" {
			loaded++
		}
		return nil
	}}
	workspace := t.TempDir()
	first, err := Load(context.Background(), req, "codex", workspace, Cache{})
	if err != nil {
		t.Fatal(err)
	}
	// Also reproduce Windows' permission representation on Unix. This is a
	// private, same-identity cache; strict shared-group permissions are separate.
	for _, name := range first.Packages[0].Files {
		if err := os.Chmod(filepath.Join(first.Packages[0].Directory, name), 0444); err != nil {
			t.Fatal(err)
		}
	}
	second, err := Load(context.Background(), req, "codex", workspace, Cache{})
	if err != nil {
		t.Fatal(err)
	}
	if loaded != 2 || first.Digest != second.Digest {
		t.Fatal("cache reuse lost load evidence or changed selection")
	}
	index := Instructions(second.Packages, true)
	if strings.Contains(index, "PRIVATE-INSTRUCTION") || !strings.Contains(index, "SKILL.md") {
		t.Fatal("resumed turn must retain a compact file index")
	}
}

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
