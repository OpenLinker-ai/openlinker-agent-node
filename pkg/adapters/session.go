package adapters

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providersession"
)

// These adapters preserve each product's existing callers and public evidence.
// File format, hash domains and locking are implemented only by the leaf.
func sessionStorePath(configured, provider, workspace string) string {
	return providersession.Path(configured, provider, workspace)
}
func sessionStoreKey(provider, workspace, sessionKey string) string {
	return providersession.Key(provider, workspace, sessionKey)
}
func sessionKeyHash(provider, workspace, sessionKey string) string {
	return providersession.KeyHash(provider, workspace, sessionKey)
}
func readSessionStore(path string) providersession.Store { return providersession.Read(path) }
func lockSession(provider, workspace, sessionKey string) func() {
	return providersession.Lock(provider, workspace, sessionKey)
}

// The product binds the persisted cursor to the selected private session before
// invoking shared filtering. A different/legacy session must replay all history.
func runWithSessionHistory(run RunContext, path, provider, workspace, key, id string) RunContext {
	if id == "" || run.Conversation == nil {
		return run
	}
	record := readSessionStore(path).Sessions[sessionStoreKey(provider, workspace, key)]
	if record.SessionID != id {
		return run
	}
	conversation := *run.Conversation
	conversation.HistoryBeforeCurrent = providersession.UnseenHistory(run.Conversation.HistoryBeforeCurrent, record.HistorySeen)
	run.Conversation = &conversation
	return run
}

func loadSessionForClientMode(
	path,
	provider,
	workspace,
	sessionKey,
	clientMode string,
) (sessionID string, generation uint64, modeChanged bool) {
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
	return providersession.Update(path, func(store *providersession.Store) bool {
		key := sessionStoreKey(provider, workspace, sessionKey)
		previous := store.Sessions[key]
		record := providersession.Record{
			SessionID: sessionID, SessionKeyHash: key[:24], Workspace: filepath.Clean(workspace),
			ClientMode: strings.TrimSpace(clientMode), ClientModeGeneration: generation,
			UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		if previous.SessionID == sessionID {
			record.HistorySeen = previous.HistorySeen
		}
		for _, run := range runs {
			if run.Conversation != nil {
				record.HistorySeen = providersession.RememberHistory(record.HistorySeen, run.Conversation.HistoryBeforeCurrent, 512)
			}
		}
		store.Sessions[key] = record
		return true
	})
}

func providerSessionClientMode(config ProviderConfig) string {
	return delegationSessionMode(config, "standard")
}

func deleteSessionID(path, provider, workspace, sessionKey string) error {
	return providersession.Update(path, func(store *providersession.Store) bool {
		key := sessionStoreKey(provider, workspace, sessionKey)
		if _, ok := store.Sessions[key]; !ok {
			return false
		}
		delete(store.Sessions, key)
		return true
	})
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
