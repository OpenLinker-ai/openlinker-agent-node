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

// Keep the cache readable within the existing provider sandbox without changing
// tracked project configuration. Git's local exclude also applies to add -A.
func protectSkillPackageCache(ctx context.Context, workspace string) error {
	current, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	found := false
	for {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			found = true
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	if !found {
		return nil
	}
	git := func(args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "git", append([]string{"-C", workspace}, args...)...).Output()
	}
	tracked, err := git("ls-files", "--", ".openlinker-skills")
	if err != nil {
		return err
	}
	if len(tracked) > 0 {
		return errors.New("skill cache contains tracked files; remove them from version control before loading packages")
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
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	root, err := os.OpenRoot(filepath.Dir(name))
	if err != nil {
		return err
	}
	defer root.Close()
	name = filepath.Base(name)
	if info, err := root.Lstat(name); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("Git exclude must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	previous, err := root.ReadFile(name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	const rule = ".openlinker-skills/"
	if !strings.Contains("\n"+string(previous)+"\n", "\n"+rule+"\n") {
		file, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		_, err = file.WriteString("\n# OpenLinker private skill package cache\n" + rule + "\n")
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if _, err := git("check-ignore", "--quiet", ".openlinker-skills/probe"); err != nil {
		return fmt.Errorf("skill cache is not excluded by Git: %w", err)
	}
	return nil
}
