package adapters

import (
	"context"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerpreflight"
)

const MinimumCodexVersion = providerpreflight.MinimumCodexVersion
const MinimumClaudeVersion = providerpreflight.MinimumClaudeVersion

// CheckProviderCLI probes compatibility before this product accepts leases.
func CheckProviderCLI(ctx context.Context, config ProviderConfig) (string, error) {
	return providerpreflight.Check(ctx, providerpreflight.Config{
		Provider: config.Provider, Bin: config.Bin, Env: config.Env,
	})
}
