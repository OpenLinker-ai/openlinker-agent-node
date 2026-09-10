package adapters

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type sessionStore struct {
	Sessions map[string]sessionRecord `json:"sessions"`
}

type sessionRecord struct {
	SessionID            string   `json:"session_id"`
	SessionKeyHash       string   `json:"session_key_hash"`
	Workspace            string   `json:"workspace"`
	ClientMode           string   `json:"client_mode,omitempty"`
	ClientModeGeneration uint64   `json:"client_mode_generation,omitempty"`
	HistorySeen          []string `json:"history_seen,omitempty"`
	UpdatedAt            string   `json:"updated_at"`
}

var sessionStoreMu sync.Mutex

func sessionStorePath(configured, provider, workspace string) string {
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

func sessionStoreKey(provider, workspace, sessionKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(provider) + "\x00" + filepath.Clean(workspace) + "\x00" + strings.TrimSpace(sessionKey)))
	return hex.EncodeToString(sum[:])
}

func sessionKeyHash(provider, workspace, sessionKey string) string {
	return sessionStoreKey(provider, workspace, sessionKey)[:24]
}

func loadSessionForClientMode(
	path,
	provider,
	workspace,
	sessionKey,
	clientMode string,
) (sessionID string, generation uint64, modeChanged bool) {
	sessionStoreMu.Lock()
	defer sessionStoreMu.Unlock()
	store := readSessionStore(path)
	record, ok := store.Sessions[sessionStoreKey(provider, workspace, sessionKey)]
	if !ok {
		return "", 1, false
	}
	generation = record.ClientModeGeneration
	if generation == 0 {
		generation = 1
	}
	storedMode := strings.TrimSpace(record.ClientMode)
	if storedMode == "" {
		storedMode = "standard"
	}
	if storedMode != clientMode {
		return "", generation + 1, true
	}
	return strings.TrimSpace(record.SessionID), generation, false
}

func saveSessionForClientMode(
	path,
	provider,
	workspace,
	sessionKey,
	sessionID,
	clientMode string,
	generation uint64,
	runs ...RunContext,
) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	if generation == 0 {
		generation = 1
	}
	sessionStoreMu.Lock()
	defer sessionStoreMu.Unlock()
	store := readSessionStore(path)
	key := sessionStoreKey(provider, workspace, sessionKey)
	previous := store.Sessions[key]
	record := sessionRecord{
		SessionID: sessionID, SessionKeyHash: key[:24], Workspace: filepath.Clean(workspace),
		ClientMode: strings.TrimSpace(clientMode), ClientModeGeneration: generation,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if previous.SessionID == sessionID {
		record.HistorySeen = previous.HistorySeen
	}
	for _, run := range runs {
		if run.Conversation != nil {
			for _, message := range run.Conversation.HistoryBeforeCurrent {
				record.HistorySeen = rememberHistoryKey(record.HistorySeen, historyMessageKey(message), 512)
			}
		}
	}
	store.Sessions[key] = record
	return writeSessionStore(path, store)
}

func providerSessionClientMode(config ProviderConfig) string {
	return delegationSessionMode(config, "standard")
}

func deleteSessionID(path, provider, workspace, sessionKey string) error {
	sessionStoreMu.Lock()
	defer sessionStoreMu.Unlock()
	store := readSessionStore(path)
	key := sessionStoreKey(provider, workspace, sessionKey)
	if _, ok := store.Sessions[key]; !ok {
		return nil
	}
	delete(store.Sessions, key)
	return writeSessionStore(path, store)
}

func readSessionStore(path string) sessionStore {
	store := sessionStore{Sessions: map[string]sessionRecord{}}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !sessionFileOwnedByCurrentUser(info) || info.Size() > maxSessionStoreBytes {
		return store
	}
	file, err := os.Open(path)
	if err != nil {
		return store
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm()&0o077 != 0 || !sessionFileOwnedByCurrentUser(opened) {
		return store
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxSessionStoreBytes+1))
	if err != nil || len(raw) > maxSessionStoreBytes {
		return store
	}
	if err := json.Unmarshal(raw, &store); err != nil || store.Sessions == nil {
		return sessionStore{Sessions: map[string]sessionRecord{}}
	}
	return store
}

func writeSessionStore(path string, store sessionStore) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !sessionFileOwnedByCurrentUser(info)) {
		return errors.New("provider session store must be an owner-only regular non-symlink file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	if len(raw) > maxSessionStoreBytes {
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
	if err := replaceFileAtomic(temporaryPath, path); err != nil {
		return err
	}
	keep = true
	return nil
}

const maxSessionStoreBytes = 8 << 20

type sessionLockEntry struct {
	mu   sync.Mutex
	refs int
}

var sessionLocks = struct {
	sync.Mutex
	entries map[string]*sessionLockEntry
}{entries: map[string]*sessionLockEntry{}}

func lockSession(provider, workspace, sessionKey string) func() {
	key := sessionStoreKey(provider, workspace, sessionKey)
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

func conversationSessionKey(run RunContext) string {
	if run.Conversation != nil {
		for _, value := range []string{run.Conversation.SessionKey, run.Conversation.RootContextID, run.Conversation.ProtocolContextID, run.Conversation.ID} {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}

// The documented event schemas expose human-readable failure messages, not a
// portable session-not-found code. Inspect the known structured error fields
// first; keep this narrowly scoped English diagnostic fallback with LC_ALL=C.
// Never classify arbitrary tool results or every failed turn as a missing session.
func missingProviderSession(diagnostic string) bool {
	lower := strings.ToLower(diagnostic)
	for _, fragment := range []string{"session not found", "unknown session", "no conversation found", "invalid session id", "no rollout found", "thread not found"} {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

func sessionPersistenceError(provider string, err error) error {
	return fmt.Errorf("persist %s session: %w", provider, err)
}
