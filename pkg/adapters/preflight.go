package adapters

import (
	"context"
	"os"
	"os/exec"
	"strings"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerpreflight"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/sessionsandbox"
)

const MinimumCodexVersion = providerpreflight.MinimumCodexVersion
const MinimumClaudeVersion = providerpreflight.MinimumClaudeVersion

// CheckProviderCLI probes compatibility before this product accepts leases.
func CheckProviderCLI(ctx context.Context, config ProviderConfig) (string, error) {
	config.Provider = strings.ToLower(strings.TrimSpace(config.Provider))
	probe := providerpreflight.Config{
		Provider: config.Provider, Bin: config.Bin, Env: config.Env,
	}
	if config.SessionIsolation.Enabled() {
		s, err := sessionsandbox.Open(ctx, config.SessionIsolation, sessionsandbox.Scope("provider-preflight", config.Provider))
		if err != nil {
			return "", err
		}
		defer s.Close()
		probe.Prepare = func(ctx context.Context, bin string, args []string) (*exec.Cmd, error) {
			if config.Provider == "claude" {
				args = append([]string{"--bare"}, args...)
			}
			return s.Command(ctx, bin, args, []string{"PATH=" + os.Getenv("PATH")})
		}
	}
	return providerpreflight.Check(ctx, probe)
}
