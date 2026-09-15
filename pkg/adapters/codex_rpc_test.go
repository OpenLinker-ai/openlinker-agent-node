package adapters

import (
	"context"
	"errors"
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/codexturn"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providertest"
)

const fixtureThread = providertest.FixtureThread
const fixtureTurn = providertest.FixtureTurn

func writeCodexRPCFixture(t *testing.T, path, scenario string) {
	t.Helper()
	providertest.WriteCodexRPCFixture(t, path, scenario)
}

// Keep the fake-process seam available to this package's delegation regressions.
type rpcFixture = providertest.RPCFixture

func TestCodexRPCFixtureProcess(t *testing.T) {
	providertest.CodexRPCFixtureProcess()
}

// Only test setup is shared. This callback always runs this package's provider.
func runCodexFixture(ctx context.Context, c providertest.CodexConfig, input any, sessionKey string) error {
	config := ProviderConfig{
		Bin: c.Bin, Workspace: c.Workspace, SessionStore: c.SessionStore,
		SessionReuse: c.SessionReuse, Timeout: c.Timeout,
		Env: c.Env, EnvAllowlist: c.EnvAllowlist,
	}
	run := RunContext{Input: input}
	if sessionKey != "" {
		run.Conversation = &ConversationContext{SessionKey: sessionKey}
	}
	_, err := (CodexProvider{Config: config}).Run(ctx, run)
	return err
}

func TestCodexRPCDrainsShutdownBeforeWaiting(t *testing.T) {
	providertest.CodexRPCDrainsShutdownBeforeWaiting(t, func(ctx context.Context, bin, dir string) (string, error) {
		_, answer, err := runCodexRPC(ctx, bin, dir, "read-only", "", "test shutdown", false, ProviderConfig{}, nil)
		return answer, err
	})
}

func TestCodexRPCAlreadyCanceledDoesNotLaunch(t *testing.T) {
	providertest.CodexRPCAlreadyCanceledDoesNotLaunch(t, func(ctx context.Context, bin, dir string) (string, error) {
		_, answer, err := runCodexRPC(ctx, bin, dir, "read-only", "", "canceled", false, ProviderConfig{}, nil)
		return answer, err
	})
}

func TestCodexRPCRetryDiagnostics(t *testing.T) {
	providertest.CodexRPCRetryDiagnostics(t, func(ctx context.Context, bin, dir string, emit func(string, any) error) (string, error) {
		_, answer, err := runCodexRPC(ctx, bin, dir, "read-only", "", "retry diagnostics", false, ProviderConfig{}, emit)
		return answer, err
	})
}

func TestCodexRPCCancellationInterruptsScopedTurn(t *testing.T) {
	providertest.CodexRPCCancellationInterruptsScopedTurn(t, runCodexFixture)
}

func TestCodexRPCFailureDoesNotPersistSession(t *testing.T) {
	providertest.CodexRPCFailureDoesNotPersistSession(t, runCodexFixture)
}

func TestCodexRPCDoesNotRecoverUnrelatedResumeErrors(t *testing.T) {
	providertest.CodexRPCDoesNotRecoverUnrelatedResumeErrors(t, runCodexFixture, func(store, workspace string) error {
		return saveSessionForClientMode(store, "codex", workspace, "conversation", fixtureThread, "codex_rpc_v1:standard", 1)
	})
}

func TestCodexRPCResolvesRelativeWorkspaceAndDottedTrustKey(t *testing.T) {
	providertest.CodexRPCResolvesRelativeWorkspaceAndDottedTrustKey(t, runCodexFixture)
}

func TestCodexRPCMessageLimitStops(t *testing.T) {
	providertest.CodexRPCMessageLimitStops(t, func(ctx context.Context, bin, dir string, emit func(string, any) error) (string, error) {
		_, answer, err := runCodexRPC(ctx, bin, dir, "read-only", "", "message limit", false, ProviderConfig{}, emit)
		return answer, err
	}, func(err error) bool { return errors.Is(err, codexturn.ErrResponseMessageLimit) })
}
func TestCodexRPCMessageLimitPreservesSession(t *testing.T) {
	providertest.CodexRPCMessageLimitPreservesSession(t, runCodexFixture, func(err error) bool { return errors.Is(err, codexturn.ErrResponseMessageLimit) })
}
