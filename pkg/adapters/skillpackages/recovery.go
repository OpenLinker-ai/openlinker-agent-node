package skillpackages

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Recovery copies are direct children: damage to an old Agent/version parent
// must not poison every later Run. Bound discovery work in hostile workspaces.
func recoverPrivatePackage(root *os.Root, agentID, digest string, files map[string]string) (string, error) {
	prefix := agentID + "-" + digest + "-recovered-"
	dir, err := root.Open(".")
	if err != nil {
		return "", err
	}
	entries, readErr := dir.ReadDir(1024)
	dir.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", readErr
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+32 {
			continue
		}
		if err := materialize(root, name, files, false); err == nil {
			return name, nil
		}
	}
	name := prefix + randomPackageSuffix()
	if err := root.Mkdir(name, 0700); err != nil {
		return "", err
	}
	return name, materialize(root, name, files, false)
}

func writeCacheIgnore(root *os.Root) error {
	const name = ".gitignore"
	const contents = "# OpenLinker generated cache; never add package contents\n*\n"
	if data, err := root.ReadFile(name); err == nil && string(data) == contents {
		return nil
	}
	// Only replace this generated marker. Never follow an existing link or
	// rewrite a package payload, even if it was changed by a same-UID process.
	temporary := ".gitignore-" + randomPackageSuffix()
	f, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	_, writeErr := f.WriteString(contents)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	if info, err := root.Lstat(name); err == nil {
		if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return errors.New("cache ignore marker must be a file")
		}
		if info.Mode().IsRegular() {
			if err := root.Chmod(name, 0600); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Link is exclusive on all supported platforms; another concurrent loader
	// may already have installed the identical marker.
	if err := root.Link(temporary, filepath.Clean(name)); err != nil {
		if data, readErr := root.ReadFile(name); readErr == nil && string(data) == contents {
			return nil
		}
		return err
	}
	return nil
}
