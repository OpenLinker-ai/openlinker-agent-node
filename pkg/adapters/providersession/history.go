package providersession

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
)

// HistoryKey preserves the original JSON-byte hash domain, including optional
// fields. Callers supply only messages from their trusted conversation context.
func HistoryKey(message any) string {
	encoded, _ := json.Marshal(message)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

// RememberHistory retains the original bounded insertion-order cursor. Eviction
// can replay old messages but must never omit unseen or corrected content.
func RememberHistory[T any](keys []string, history []T, limit int) []string {
	for _, message := range history {
		key := HistoryKey(message)
		if !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
		if len(keys) > limit {
			keys = keys[len(keys)-limit:]
		}
	}
	return keys
}

// UnseenHistory filters only content known to the exact resumed session.
// Product code must establish that identity before invoking this mechanism.
func UnseenHistory[T any](history []T, seen []string) []T {
	var result []T
	for _, message := range history {
		if !slices.Contains(seen, HistoryKey(message)) {
			result = append(result, message)
		}
	}
	return result
}
