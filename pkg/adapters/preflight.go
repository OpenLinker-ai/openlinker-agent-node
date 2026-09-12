package adapters

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/provideroutput"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerpreflight"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/sessionsandbox"
)

const MinimumCodexVersion = providerpreflight.MinimumCodexVersion
const MinimumClaudeVersion = providerpreflight.MinimumClaudeVersion

// CheckProviderCLI probes compatibility before this product accepts leases.
func CheckProviderCLI(ctx context.Context, config ProviderConfig) (version string, resultErr error) {
	if err := validateSessionIsolation(config); err != nil {
		return "", err
	}
	if config.SessionIsolation.Enabled() {
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		provider := strings.ToLower(strings.TrimSpace(config.Provider))
		environment, err := isolatedEnvironment(config, provider, true)
		if err != nil {
			return "", err
		}
		session, err := sessionsandbox.Open(ctx, config.SessionIsolation, sessionsandbox.Scope("preflight", provider, config.SessionIsolation.Image))
		if err != nil {
			return "", err
		}
		defer func() { resultErr = errors.Join(resultErr, session.Close()) }()
		bin := config.Bin
		if bin == "" {
			bin = provider
		}
		return providerpreflight.CheckProbe(ctx, provider, func(ctx context.Context, args ...string) (string, error) {
			command, err := session.Command(ctx, bin, args, environment)
			if err != nil {
				return "", err
			}
			stdout, stderr := provideroutput.NewLimitedBuffer(cancel), provideroutput.NewLimitedBuffer(cancel)
			command.Stdout, command.Stderr = stdout, stderr
			if err := command.Run(); err != nil {
				return "", errors.New("isolated provider compatibility probe failed inside the selected image")
			}
			if err := provideroutput.LimitError(provider, stdout, stderr); err != nil {
				return "", err
			}
			return strings.TrimSpace(stdout.String()), nil
		})
	}
	return providerpreflight.Check(ctx, providerpreflight.Config{
		Provider: config.Provider, Bin: config.Bin, Env: config.Env,
	})
}
