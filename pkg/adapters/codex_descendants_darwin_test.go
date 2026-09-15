package adapters

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providertest"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/sessionsandbox"
)

func TestCodexRPCCancellationStopsDetachedDescendants(t *testing.T) {
	providertest.CodexRPCCancellationStopsDetachedDescendants(t, runCodexFixture)
}

func TestCodexHostAuthFactoryStopsDetachedDescendants(t *testing.T) {
	providertest.CodexRPCCancellationStopsDetachedDescendants(t, func(ctx context.Context, c providertest.CodexConfig, _ any, _ string) error {
		// Exercise the real nativeHostCommand factory with private session
		// storage. This is process cleanup, not a model/OS tool-policy test.
		session, err := sessionsandbox.OpenClient(context.Background(), sessionsandbox.Config{
			Mode: "native", Root: filepath.Join(t.TempDir(), "sessions"), Namespace: "https://fixture.invalid",
		}, "fixture")
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		config := ProviderConfig{Provider: "codex", Workspace: c.Workspace, Env: c.Env, sandbox: session}
		_, _, err = runCodexRPC(ctx, c.Bin, c.Workspace, "read-only", "", "fixture", false, config, nil)
		return err
	})
}
