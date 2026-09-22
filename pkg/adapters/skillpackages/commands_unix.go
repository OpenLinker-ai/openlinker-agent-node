//go:build !windows

package skillpackages

import (
	"os"
	"syscall"
)

func commandExecutable(path string, _ os.FileInfo) bool {
	return syscall.Access(path, 1) == nil
}
