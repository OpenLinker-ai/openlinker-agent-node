//go:build unix

package providersession

import (
	"os"
	"syscall"
)

// OwnedByCurrentUser checks the effective UID, not caller-provided identity.
func OwnedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}
