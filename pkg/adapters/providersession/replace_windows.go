//go:build windows

package providersession

import "golang.org/x/sys/windows"

// ReplaceAtomic preserves replace-existing and write-through on Windows.
func ReplaceAtomic(source, destination string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
