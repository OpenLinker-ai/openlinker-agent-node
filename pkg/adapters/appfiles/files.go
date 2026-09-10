// Package appfiles preserves the shared CLI/Plugin app-file mechanisms. Callers
// own configuration types, defaults, paths, identity decisions, and lock lifetime.
// These helpers do not start a Worker or read product/global configuration.
package appfiles

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providersession"
)

// DecodeStrictJSON rejects unknown fields and trailing values. It intentionally
// preserves encoding/json's existing null and duplicate-key behavior.
func DecodeStrictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

// WritePrivateJSON retains the app format: two-space indentation and a newline.
func WritePrivateJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return WritePrivateFile(path, append(raw, '\n'))
}

// WritePrivateFile preserves private temporary files and atomic replacement.
// It does not choose a path or add new parent-directory/ACL safety guarantees.
func WritePrivateFile(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return errors.New("destination is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".openlinker-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	// Only this proven-equivalent primitive is shared with Provider sessions.
	// Their forgiving Read/Update and different file policy are not used here.
	if err := providersession.ReplaceAtomic(temporaryPath, path); err != nil {
		return err
	}
	keep = true
	return nil
}

// ReadPrivateSecret retains the app's existing Lstat/permission/owner/size checks
// and errors. This refactor does not strengthen the existing Lstat/open race
// behavior or introduce Windows DACL checks.
func ReadPrivateSecret(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("secret path must be a regular file and not a symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("secret file must not be accessible by group or other users")
	}
	if !providersession.OwnedByCurrentUser(info) {
		return "", errors.New("secret file must be owned by the current user")
	}
	if info.Size() <= 0 || info.Size() > 64<<10 {
		return "", errors.New("secret file size is invalid")
	}
	file, err := os.Open(path) // #nosec G304 -- operator-selected secret path is validated above.
	if err != nil {
		return "", err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", errors.New("secret file is empty")
	}
	return value, nil
}
