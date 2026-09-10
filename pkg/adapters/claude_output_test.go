package adapters

import (
	"context"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providertest"
)

func TestClaudeLargeTranscriptKeepsOnlyResult(t *testing.T) {
	providertest.ClaudeLargeTranscriptKeepsOnlyResult(t, func(ctx context.Context, bin, dir string) (string, error) {
		result, err := (ClaudeProvider{Config: ProviderConfig{Bin: bin, Workspace: dir, Timeout: 15 * time.Second}}).Run(ctx, RunContext{Input: "long task"})
		if err != nil {
			return "", err
		}
		return result.Output.(map[string]any)["summary"].(string), nil
	})
}

func TestClaudeOutputFixtureProcess(t *testing.T) {
	providertest.ClaudeOutputFixtureProcess()
}

func TestClaudeStreamBoundsRecordsAndRejectsMalformedOrMissingResult(t *testing.T) {
	providertest.ClaudeStreamBoundsRecordsAndRejectsMalformedOrMissingResult(t, maxProviderOutputBytes, func(cancel func()) providertest.ClaudeStream {
		stream := newClaudeResultStream(cancel, nil)
		return providertest.ClaudeStream{
			Writer: stream,
			Result: func() (string, error) {
				result, err := stream.Result()
				return result.Result, err
			},
			PendingBytes: func() int { return len(stream.pending) },
		}
	})
}
