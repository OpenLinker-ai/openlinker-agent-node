//go:build windows

package codexhome

import "os"

// Fail closed until this boundary has a Windows DACL implementation.
func owned(info os.FileInfo) bool { return false }
