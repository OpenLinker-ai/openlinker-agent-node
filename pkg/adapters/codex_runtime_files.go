package adapters

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Codex creates locked argv0 helper symlinks before initialize. Linux restricted
// mounts need each validated alias directory and its executable target, not the
// auth home or tmp tree. Binding individual symlinks fails in bubblewrap.
func codexRuntimeFileGrants(env []string, base map[string]string, overrides map[string]json.RawMessage) error {
	filesystem := make(map[string]string, len(base))
	for path, access := range base {
		filesystem[path] = access
	}
	home, configHome := "", ""
	for _, entry := range env {
		k, v, _ := strings.Cut(entry, "=")
		if k == "HOME" {
			home = v
		}
		if k == "CODEX_HOME" {
			configHome = v
		}
	}
	if configHome == "" {
		configHome = filepath.Join(home, ".codex")
	}
	root := filepath.Join(configHome, "tmp", "arg0")
	dirs, err := os.ReadDir(root)
	if err != nil || len(dirs) > 1024 {
		return errors.New("cannot resolve Codex sandbox helper aliases")
	}
	found := false
	for _, dir := range dirs {
		if !dir.IsDir() || !strings.HasPrefix(dir.Name(), "codex-arg0") {
			continue
		}
		aliasDir := filepath.Join(root, dir.Name())
		entries, err := os.ReadDir(aliasDir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return errors.New("cannot inspect Codex sandbox helper directory")
		}
		for _, entry := range entries {
			switch entry.Name() {
			case "codex-linux-sandbox", "codex-execve-wrapper", "apply_patch", "applypatch":
			case ".lock":
				info, err := entry.Info()
				if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
					return errors.New("unsafe Codex sandbox helper lock")
				}
			default:
				return errors.New("Codex helper directory contains unexpected data")
			}
		}
		active := false
		for _, name := range []string{"codex-linux-sandbox", "codex-execve-wrapper", "apply_patch", "applypatch"} {
			path := filepath.Join(root, dir.Name(), name)
			info, err := os.Lstat(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				return errors.New("unsafe Codex runtime helper")
			}
			target, err := os.Stat(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil || !target.Mode().IsRegular() || target.Mode().Perm()&0111 == 0 {
				return errors.New("Codex runtime helper is not executable code")
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return errors.New("cannot resolve Codex helper executable")
			}
			filesystem[resolved] = "read"
			active = true
			if name == "codex-linux-sandbox" {
				found = true
			}
		}
		if active {
			filesystem[aliasDir] = "read"
		}
	}
	if !found {
		return errors.New("Codex did not create its required Linux sandbox helper")
	}
	// App-server splits dotted override keys without TOML quote handling.
	// Put literal paths (including .codex) inside the JSON table value.
	raw, err := json.Marshal(filesystem)
	if err != nil {
		return err
	}
	overrides["permissions.openlinker_session.filesystem"] = raw
	return nil
}
