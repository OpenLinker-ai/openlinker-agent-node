//go:build !windows

package providersession

import "os"

// ReplaceAtomic retains the platform's existing replace semantics.
func ReplaceAtomic(source, destination string) error { return os.Rename(source, destination) }
