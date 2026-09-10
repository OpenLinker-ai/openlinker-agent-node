// Package providersession owns the shared private session-file format and cursor
// mechanics. Products retain mode, generation, continuation and history policy.
package providersession

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Store struct {
	Sessions map[string]Record `json:"sessions"`
}

type Record struct {
	SessionID            string   `json:"session_id"`
	SessionKeyHash       string   `json:"session_key_hash"`
	Workspace            string   `json:"workspace"`
	ClientMode           string   `json:"client_mode,omitempty"`
	ClientModeGeneration uint64   `json:"client_mode_generation,omitempty"`
	HistorySeen          []string `json:"history_seen,omitempty"`
	UpdatedAt            string   `json:"updated_at"`
}

var storeMu sync.Mutex

func Path(configured, provider, workspace string) string {
	if value := strings.TrimSpace(configured); value != "" {
		return value
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(cacheDir) == "" {
		cacheDir = os.TempDir()
	}
	sum := sha256.Sum256([]byte(filepath.Clean(workspace)))
	return filepath.Join(cacheDir, "openlinker", "session-map", provider+"-"+hex.EncodeToString(sum[:8])+".json")
}

func Key(provider, workspace, sessionKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(provider) + "\x00" + filepath.Clean(workspace) + "\x00" + strings.TrimSpace(sessionKey)))
	return hex.EncodeToString(sum[:])
}

func KeyHash(provider, workspace, sessionKey string) string {
	return Key(provider, workspace, sessionKey)[:24]
}

func readStore(path string) Store {
	store := Store{Sessions: map[string]Record{}}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !OwnedByCurrentUser(info) || info.Size() > MaxStoreBytes {
		return store
	}
	file, err := os.Open(path)
	if err != nil {
		return store
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm()&0o077 != 0 || !OwnedByCurrentUser(opened) {
		return store
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxStoreBytes+1))
	if err != nil || len(raw) > MaxStoreBytes {
		return store
	}
	if err := json.Unmarshal(raw, &store); err != nil || store.Sessions == nil {
		return Store{Sessions: map[string]Record{}}
	}
	return store
}

func writeStore(path string, store Store) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !OwnedByCurrentUser(info)) {
		return errors.New("provider session store must be an owner-only regular non-symlink file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	if len(raw) > MaxStoreBytes {
		return errors.New("provider session store exceeds size limit")
	}
	temporary, err := os.CreateTemp(dir, ".sessions-*.tmp")
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
	if err := ReplaceAtomic(temporaryPath, path); err != nil {
		return err
	}
	keep = true
	return nil
}

const MaxStoreBytes = 8 << 20

type sessionLockEntry struct {
	mu   sync.Mutex
	refs int
}

var sessionLocks = struct {
	sync.Mutex
	entries map[string]*sessionLockEntry
}{entries: map[string]*sessionLockEntry{}}

func Lock(provider, workspace, sessionKey string) func() {
	key := Key(provider, workspace, sessionKey)
	sessionLocks.Lock()
	entry := sessionLocks.entries[key]
	if entry == nil {
		entry = &sessionLockEntry{}
		sessionLocks.entries[key] = entry
	}
	entry.refs++
	sessionLocks.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		sessionLocks.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(sessionLocks.entries, key)
		}
		sessionLocks.Unlock()
	}
}

// Read returns an isolated snapshot. Invalid, legacy-incompatible or unsafe files
// retain the existing fail-closed empty-store behavior without rewriting them.
func Read(path string) Store {
	storeMu.Lock()
	defer storeMu.Unlock()
	return readStore(path)
}

// Update serializes the existing read/modify/atomic-replace transaction. edit
// operates on a private snapshot and returns false to leave the file untouched.
// Callers decide product policy; edit must not call Read or Update recursively.
func Update(path string, edit func(*Store) bool) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	current := readStore(path)
	if !edit(&current) {
		return nil
	}
	return writeStore(path, current)
}
