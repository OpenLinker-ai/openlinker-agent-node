//go:build unix

package appfiles

import (
	"errors"
	"fmt"
	"os"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providersession"
	"golang.org/x/sys/unix"
)

// Lock is an OS-backed, cross-process exclusive app lock. It is not a Provider
// session mutex or an SDK data-directory lock. Callers own Release sequencing.
type Lock struct{ file *os.File }

// AcquireLock opens the exact caller-selected path without creating its parent.
// Legacy app errors are retained so existing CLI/Plugin consumers stay compatible.
func AcquireLock(path string) (*Lock, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open Agent mode ownership lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	closeOnError := func(err error) (*Lock, error) {
		_ = file.Close()
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !providersession.OwnedByCurrentUser(info) {
		return closeOnError(errors.New("Agent mode ownership lock must be an owner-only regular file"))
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return closeOnError(errors.New("Agent state is already serving another Runtime Worker"))
	}
	return &Lock{file: file}, nil
}

// Release is idempotent for a nil or already-released lock.
func (lock *Lock) Release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	closeErr := lock.file.Close()
	lock.file = nil
	if err != nil {
		return err
	}
	return closeErr
}
