package adapters

import (
	"path/filepath"
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providersession"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providertest"
)

func TestProductSessionHistoryKeepsExactIdentityAndFullMessageHash(t *testing.T) {
	sequence := int32(7)
	first := ConversationMessage{RunID: "run-1", EventSequence: &sequence, Role: "user", Content: "query",
		Payload: map[string]any{"b": true, "a": "value"}, CreatedAt: "2026-09-10T00:00:00Z"}
	corrected := first
	corrected.Content = "corrected query"
	path := filepath.Join(t.TempDir(), "sessions.json")
	providertest.SessionHistory(t, first, corrected,
		func(message ConversationMessage) string { return providersession.HistoryKey(message) },
		func(id string, history []ConversationMessage) error {
			return saveSessionForClientMode(path, "claude", "workspace", "same-key", id, "standard", 3,
				RunContext{Conversation: &ConversationContext{HistoryBeforeCurrent: history}})
		},
		func(id string, history []ConversationMessage) []ConversationMessage {
			conversation := &ConversationContext{ID: "conversation", HistoryBeforeCurrent: history}
			run := RunContext{Conversation: conversation}
			filtered := runWithSessionHistory(run, path, "claude", "workspace", "same-key", id)
			if len(conversation.HistoryBeforeCurrent) != len(history) || filtered.Conversation.ID != conversation.ID {
				t.Fatal("history binding mutated source or lost conversation metadata")
			}
			return filtered.Conversation.HistoryBeforeCurrent
		})
}
