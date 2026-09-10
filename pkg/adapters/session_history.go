package adapters

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
)

func historyMessageKey(message ConversationMessage) string {
	encoded, _ := json.Marshal(message)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func rememberHistoryKey(keys []string, key string, limit int) []string {
	if !slices.Contains(keys, key) {
		keys = append(keys, key)
	}
	if len(keys) > limit {
		keys = keys[len(keys)-limit:]
	}
	return keys
}

// Only messages actually supplied from Core can be omitted on resume. A Run
// ID alone does not cover later corrections or messages from another provider.
// Missing/legacy cursors replay
// history conservatively; bounded cursor eviction can duplicate, never omit,
// unseen context. Cursor and session ID are committed together after success.
func runWithSessionHistory(run RunContext, path, provider, workspace, key, id string) RunContext {
	if id == "" || run.Conversation == nil {
		return run
	}
	sessionStoreMu.Lock()
	record := readSessionStore(path).Sessions[sessionStoreKey(provider, workspace, key)]
	sessionStoreMu.Unlock()
	if record.SessionID != id {
		return run
	}
	conversation := *run.Conversation
	conversation.HistoryBeforeCurrent = nil
	for _, message := range run.Conversation.HistoryBeforeCurrent {
		if slices.Contains(record.HistorySeen, historyMessageKey(message)) {
			continue
		}
		conversation.HistoryBeforeCurrent = append(conversation.HistoryBeforeCurrent, message)
	}
	run.Conversation = &conversation
	return run
}
