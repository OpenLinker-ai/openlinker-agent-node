//go:build !windows

package adapters

import "os"

func replaceFileAtomic(source, destination string) error { return os.Rename(source, destination) }
