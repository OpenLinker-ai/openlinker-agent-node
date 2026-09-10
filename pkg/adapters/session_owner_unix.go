//go:build unix

package adapters

import (
	"os"
	"syscall"
)

func sessionFileOwnedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}
