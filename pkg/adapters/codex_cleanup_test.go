package adapters

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/codexturn"
)

func TestCodexCanceledResultDoesNotHideCleanupFailure(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		err := codexCanceledResult(cause, errors.Join(cause, codexturn.ErrProcessCleanup), time.Second)
		if !errors.Is(err, cause) || !errors.Is(err, codexturn.ErrProcessCleanup) {
			t.Fatalf("request outcome hid incomplete process cleanup: %v", err)
		}
	}
	if err := codexCanceledResult(context.Canceled, context.Canceled, time.Second); err != context.Canceled {
		t.Fatal("ordinary cancellation semantics changed")
	}
	if err := codexCanceledResult(context.DeadlineExceeded, context.DeadlineExceeded, time.Second); err.Error() != "Codex timed out after 1s" {
		t.Fatal("ordinary timeout diagnostic changed")
	}
}

// A missing-session retry must not replace an incomplete cleanup of the first
// attempt with a later success.
func TestCodexMissingSessionRecoveryKeepsCleanupFailure(t *testing.T) {
	both := errors.Join(codexturn.ErrSessionMissing, codexturn.ErrProcessCleanup)
	for _, tc := range []struct {
		name      string
		sessionID string
		attempt   int
		err       error
		want      bool
	}{
		{"missing session only", "old", 0, codexturn.ErrSessionMissing, true},
		{"wrapped missing session", "old", 0, fmt.Errorf("resume: %w", codexturn.ErrSessionMissing), true},
		{"missing session with cleanup failure", "old", 0, both, false},
		{"wrapped missing session with cleanup failure", "old", 0, fmt.Errorf("turn: %w", both), false},
		{"cleanup failure only", "old", 0, codexturn.ErrProcessCleanup, false},
		{"second attempt", "old", 1, codexturn.ErrSessionMissing, false},
		{"no saved session", "", 0, codexturn.ErrSessionMissing, false},
		{"unrelated failure", "old", 0, errors.New("other"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexRecoversMissingSession(tc.sessionID, tc.attempt, tc.err); got != tc.want {
				t.Fatalf("recover=%v, want %v for %v", got, tc.want, tc.err)
			}
		})
	}
}
