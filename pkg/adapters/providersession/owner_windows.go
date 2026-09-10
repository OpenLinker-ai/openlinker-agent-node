//go:build windows

package providersession

import "os"

// OwnedByCurrentUser retains the existing Windows behavior; this is not a new
// DACL guarantee. Isolated Codex remains unsupported on Windows.
func OwnedByCurrentUser(os.FileInfo) bool { return true }
