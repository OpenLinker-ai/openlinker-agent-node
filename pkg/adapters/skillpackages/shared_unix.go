//go:build !windows

package skillpackages

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// CheckSharedCache rejects unsafe or unprovisioned cross-UID caches. Provisioning
// and choosing the Provider's read-only group belong to the execution host.
func CheckSharedCache(directory string, groupID int) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != 0750 || info.Mode()&os.ModeSetgid == 0 ||
		int(stat.Uid) != os.Geteuid() || groupID <= 0 || int(stat.Gid) != groupID {
		return errors.New("shared skill cache requires host ownership, the configured group and mode 02750")
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != filepath.Clean(directory) {
		return errors.New("shared skill cache must not have symlink ancestors")
	}
	return nil
}
