//go:build !windows

package agentdelegation

import (
	"errors"
	"os"
	"syscall"
)

func delegationSocketMode(root, directory string) (os.FileMode, error) {
	if root == "" {
		return 0o600, nil
	}
	info, err := os.Lstat(root)
	if err != nil {
		return 0, err
	}
	if info.Mode()&os.ModeSetgid == 0 {
		return 0o600, nil
	}
	// Official containers supply a Runtime-owned 2710 tmpfs whose group is
	// the isolated Provider identity. Only that explicit boundary permits
	// cross-UID access; ordinary same-UID installations stay 0700/0600.
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || int(stat.Uid) != os.Geteuid() || info.Mode().Perm() != 0o710 {
		return 0, errors.New("delegation shared root must be owner-controlled with mode 2710")
	}
	child, err := os.Lstat(directory)
	if err != nil {
		return 0, err
	}
	childStat, ok := child.Sys().(*syscall.Stat_t)
	if !ok || !child.IsDir() || childStat.Uid != stat.Uid || childStat.Gid != stat.Gid {
		return 0, errors.New("delegation directory did not inherit the trusted Provider group")
	}
	return 0o660, nil
}
