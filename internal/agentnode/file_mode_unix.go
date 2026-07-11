//go:build unix

package agentnode

import "os"

func runtimeFileModeIsPrivate(mode os.FileMode) bool {
	return mode.Perm()&0o077 == 0
}
