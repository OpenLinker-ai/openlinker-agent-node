package adapters

import (
	"context"
	"errors"
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
