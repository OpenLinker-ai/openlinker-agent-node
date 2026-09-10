//go:build windows

package adapters

import "os"

func sessionFileOwnedByCurrentUser(os.FileInfo) bool { return true }
