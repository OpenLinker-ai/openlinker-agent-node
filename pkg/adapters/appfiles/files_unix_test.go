//go:build unix

package appfiles

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providersession"
)

type ownerFileInfo struct {
	os.FileInfo
	stat *syscall.Stat_t
}

func (info ownerFileInfo) Sys() any { return info.stat }

func TestAppFileOwnerPrimitiveUsesEffectiveUser(t *testing.T) {
	if !providersession.OwnedByCurrentUser(ownerFileInfo{stat: &syscall.Stat_t{Uid: uint32(os.Geteuid())}}) {
		t.Fatal("effective owner was rejected")
	}
	if providersession.OwnedByCurrentUser(ownerFileInfo{stat: &syscall.Stat_t{Uid: uint32(os.Geteuid() + 1)}}) {
		t.Fatal("a different owner was accepted")
	}
}

func TestAppFilesRejectSymlinksWithoutChangingTarget(t *testing.T) {
	dir := t.TempDir()
	target, link := filepath.Join(dir, "target"), filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivateFile(link, []byte("replacement")); err == nil || err.Error() != "destination is not a regular file" {
		t.Fatalf("symlink write was not rejected: %v", err)
	}
	if _, err := ReadPrivateSecret(link); err == nil || err.Error() != "secret path must be a regular file and not a symlink" {
		t.Fatalf("symlink secret was not rejected: %v", err)
	}
	if _, err := AcquireLock(link); err == nil || !strings.Contains(err.Error(), "open Agent mode ownership lock") {
		t.Fatalf("symlink lock was not rejected: %v", err)
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "unchanged" {
		t.Fatal("symlink target changed")
	}
	if matches, err := filepath.Glob(filepath.Join(dir, ".openlinker-*.tmp")); err != nil || len(matches) != 0 {
		t.Fatalf("failure left temporary files: %v, %v", matches, err)
	}
}

func TestLockRejectsLooseModeWithoutKeepingHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caller-selected.lock")
	if err := os.WriteFile(path, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(path); err == nil || err.Error() != "Agent mode ownership lock must be an owner-only regular file" {
		t.Fatalf("loose lock permissions were not rejected: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}
