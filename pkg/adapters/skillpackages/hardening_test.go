package skillpackages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func loadRequest(t *testing.T, commands ...string) Request {
	t.Helper()
	payload, err := json.Marshal(Contents{Name: "review", Providers: []string{"codex"}, RequiredCommands: commands, Files: map[string]string{"SKILL.md": "ORIGINAL-INSTRUCTION", "refs/input.txt": "original data"}})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	return Request{AgentID: "55555555-5555-4555-8555-555555555555", Trusted: true, Emit: func(string, any) error { return nil }, Snapshot: Snapshot{Schema: 1, Bundles: []Version{{BindingID: "11111111-1111-4111-8111-111111111111", PackageID: "22222222-2222-4222-8222-222222222222", VersionID: "33333333-3333-4333-8333-333333333333", Payload: string(payload), Digest: hex.EncodeToString(digest[:])}}}}
}

func TestDamagedPrivateCacheRecoversAndReusesVerifiedCopy(t *testing.T) {
	for _, mode := range []string{"changed-file", "version-symlink", "agent-parent-symlink"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			workspace := t.TempDir()
			req := loadRequest(t)
			first, err := Load(ctx, req, "codex", workspace, Cache{})
			if err != nil {
				t.Fatal(err)
			}
			original := first.Packages[0].Directory
			outside := t.TempDir()
			if mode == "changed-file" {
				if err := os.Chmod(filepath.Join(original, "SKILL.md"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(original, "SKILL.md"), []byte("TAMPERED"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if runtime.GOOS == "windows" {
					t.Skip("symlinks require developer mode")
				}
				target := original
				if mode == "agent-parent-symlink" {
					target = filepath.Dir(original)
				}
				if err := os.Rename(target, target+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, target); err != nil {
					t.Fatal(err)
				}
			}
			second, err := Load(ctx, req, "codex", workspace, Cache{})
			if err != nil {
				t.Fatal(err)
			}
			third, err := Load(ctx, req, "codex", workspace, Cache{})
			if err != nil {
				t.Fatal(err)
			}
			if second.Packages[0].Directory == original || second.Packages[0].Directory != third.Packages[0].Directory || first.Digest == second.Digest || second.Digest != third.Digest {
				t.Fatal("recovery must rotate the session once and reuse the clean copy")
			}
			data, err := os.ReadFile(filepath.Join(second.Packages[0].Directory, "SKILL.md"))
			if err != nil || string(data) != "ORIGINAL-INSTRUCTION" {
				t.Fatal("did not recover pinned contents", err)
			}
			if mode == "changed-file" {
				data, _ := os.ReadFile(filepath.Join(original, "SKILL.md"))
				if string(data) != "TAMPERED" {
					t.Fatal("overwrote damaged tree")
				}
			}
			entries, _ := os.ReadDir(outside)
			if len(entries) != 0 {
				t.Fatal("wrote through hostile symlink")
			}
		})
	}
}

func TestGitProtectionNeverChangesEnclosingOrExternalRepository(t *testing.T) {
	for _, mode := range []string{"nested", "external-gitdir", "external-info-link"} {
		t.Run(mode, func(t *testing.T) {
			outer := t.TempDir()
			runGit := func(dir string, args ...string) string {
				t.Helper()
				out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("git: %s: %v", out, err)
				}
				return string(out)
			}
			runGit(outer, "init", "-q")
			exclude := filepath.Join(outer, ".git", "info", "exclude")
			before, err := os.ReadFile(exclude)
			if err != nil {
				t.Fatal(err)
			}
			workspace := filepath.Join(outer, "nested")
			if err := os.Mkdir(workspace, 0700); err != nil {
				t.Fatal(err)
			}
			if mode == "external-gitdir" {
				if err := os.WriteFile(filepath.Join(workspace, ".git"), []byte("gitdir: "+filepath.Join(outer, ".git")+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "external-info-link" {
				if runtime.GOOS == "windows" {
					t.Skip("symlinks require developer mode")
				}
				workspace = t.TempDir()
				runGit(workspace, "init", "-q")
				if err := os.RemoveAll(filepath.Join(workspace, ".git", "info")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outer, ".git", "info"), filepath.Join(workspace, ".git", "info")); err != nil {
					t.Fatal(err)
				}
			}
			_, loadErr := Load(context.Background(), loadRequest(t), "codex", workspace, Cache{})
			if loadErr != nil && mode != "external-info-link" {
				t.Fatal(loadErr)
			}
			after, _ := os.ReadFile(exclude)
			if string(before) != string(after) {
				t.Fatal("modified a repository outside the workspace")
			}
			if mode == "nested" {
				runGit(outer, "add", "-A")
				if got := runGit(outer, "ls-files"); got != "" {
					t.Fatalf("nested cache escaped local ignore: %s", got)
				}
			}
		})
	}
}

func TestGitProtectionIgnoresInheritedGitConfiguration(t *testing.T) {
	workspace := t.TempDir()
	external := t.TempDir()
	for _, dir := range []string{workspace, external} {
		if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	outside := filepath.Join(external, ".git", "info", "exclude")
	before, _ := os.ReadFile(outside)
	t.Setenv("GIT_DIR", filepath.Join(external, ".git"))
	t.Setenv("GIT_WORK_TREE", external)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.fsmonitor")
	t.Setenv("GIT_CONFIG_VALUE_0", "must-not-execute")
	if _, err := Load(context.Background(), loadRequest(t), "codex", workspace, Cache{}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(outside)
	if string(before) != string(after) {
		t.Fatal("inherited GIT_DIR redirected writes")
	}
	own, _ := os.ReadFile(filepath.Join(workspace, ".git", "info", "exclude"))
	if !strings.Contains(string(own), ".openlinker-skills/") {
		t.Fatal("own repository was not protected")
	}
}

func TestCommandsUseProviderEnvironmentWithoutExecutingPrograms(t *testing.T) {
	host, provider := t.TempDir(), t.TempDir()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".cmd"
	}
	for dir, name := range map[string]string{host: "host-only", provider: "provider-only"} {
		if err := os.WriteFile(filepath.Join(dir, name+suffix), []byte("MUST NOT EXECUTE"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", host)
	req := loadRequest(t, "provider-only")
	req.CheckCommands = func(ctx context.Context, names []string) error {
		return CheckCommands(ctx, names, []string{"PATH=" + provider}, provider, nil)
	}
	if _, err := Load(context.Background(), req, "codex", t.TempDir(), Cache{}); err != nil {
		t.Fatal("used Worker PATH", err)
	}
	if err := CheckCommands(context.Background(), []string{"host-only"}, []string{"PATH=" + provider}, provider, nil); err == nil {
		t.Fatal("used Worker PATH")
	}
	if err := CheckCommands(context.Background(), []string{"provider-only"}, []string{"PATH=" + provider}, provider, func(string) bool { return false }); err == nil {
		t.Fatal("ignored tool filesystem boundary")
	}
	if err := CheckCommands(context.Background(), []string{"provider-only"}, nil, provider, nil); err == nil {
		t.Fatal("empty Provider environment inherited Worker PATH")
	}
}
