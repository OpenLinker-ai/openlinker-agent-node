//go:build windows

package appfiles

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// Lock preserves the app's Windows share-denial handle, without adding a DACL
// guarantee. The caller supplies the path and owns the handle lifetime.
type Lock struct{ file *os.File }

// AcquireLock opens the exact caller-selected path without creating its parent.
func AcquireLock(path string) (*Lock, error) {
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pathUTF16, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, errors.New("Agent state is already serving another Runtime Worker")
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil || info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("Agent mode ownership lock must be a regular non-reparse file")
	}
	return &Lock{file: os.NewFile(uintptr(handle), path)}, nil
}

// Release is idempotent for a nil or already-released lock.
func (lock *Lock) Release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := lock.file.Close()
	lock.file = nil
	return err
}
