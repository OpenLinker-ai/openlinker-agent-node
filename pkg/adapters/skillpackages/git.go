package skillpackages

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Only the repository rooted at workspace may be changed. Nested workspaces and
// external worktree gitdirs rely on the generated cache's own .gitignore.
func protectSkillPackageCache(ctx context.Context, workspace string) error {
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return err
	}
	work, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer work.Close()
	info, err := work.Lstat(".git")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	gitdir := filepath.Join(workspace, ".git")
	if info.Mode().IsRegular() {
		if info.Size() > 4096 {
			return nil
		}
		raw, err := work.ReadFile(".git")
		if err != nil {
			return err
		}
		target, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir: ")
		if !ok {
			return nil
		}
		gitdir = target
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(workspace, gitdir)
		}
	} else if !info.IsDir() {
		return nil
	}
	gitdir, err = filepath.EvalSymlinks(gitdir)
	if err != nil {
		return nil
	}
	if _, ok := workspaceRelative(workspace, gitdir); !ok {
		return nil
	}
	environment := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(entry), "GIT_") {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_OPTIONAL_LOCKS=0")
	git := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.fsmonitor=false", "-C", workspace, "--git-dir=" + gitdir, "--work-tree=" + workspace}, args...)...)
		cmd.Env = environment
		return cmd.Output()
	}
	excluded, err := git("rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return err
	}
	name := strings.TrimSpace(string(excluded))
	if name == "" {
		return errors.New("missing Git exclude path")
	}
	if !filepath.IsAbs(name) {
		name = filepath.Join(workspace, name)
	}
	relative, inside := workspaceRelative(workspace, name)
	if !inside {
		return nil
	}
	// os.Root confines directory creation and the final append even if a model
	// changes .git or info between Git's answer and our filesystem operations.
	tracked, err := git("ls-files", "--", ".openlinker-skills")
	if err != nil {
		return err
	}
	if len(tracked) > 0 {
		return errors.New("skill cache contains tracked files; remove them from version control before loading packages")
	}
	if err := work.MkdirAll(filepath.Dir(relative), 0700); err != nil {
		return err
	}
	if info, err := work.Lstat(relative); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("Git exclude must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	previous, err := work.ReadFile(relative)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	const rule = ".openlinker-skills/"
	if !strings.Contains("\n"+string(previous)+"\n", "\n"+rule+"\n") {
		f, err := work.OpenFile(relative, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		_, writeErr := f.WriteString("\n# OpenLinker generated skill package cache\n" + rule + "\n")
		if err := errors.Join(writeErr, f.Close()); err != nil {
			return err
		}
	}
	if _, err := git("check-ignore", "--quiet", ".openlinker-skills/probe"); err != nil {
		return fmt.Errorf("skill cache is not excluded by Git: %w", err)
	}
	return nil
}

func workspaceRelative(workspace, name string) (string, bool) {
	rel, err := filepath.Rel(workspace, name)
	return rel, err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
