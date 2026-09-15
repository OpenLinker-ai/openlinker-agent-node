package codexturn

import (
	"context"
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providertest"
)

func TestNativeCancellationStopsDetachedDescendants(t *testing.T) {
	providertest.CodexRPCCancellationStopsDetachedDescendants(t, func(ctx context.Context, c providertest.CodexConfig, _ any, _ string) error {
		_, _, err := Run(ctx, Config{Prompt: "fixture", Sandbox: "read-only", Prepare: func(ctx context.Context) (PreparedCommand, error) {
			return PrepareNative(ctx, NativeCommand{Bin: c.Bin, Workspace: c.Workspace, Env: c.Env, EnvAllowlist: c.EnvAllowlist, Arguments: func(string) []string { return nil }})
		}})
		return err
	})
}
