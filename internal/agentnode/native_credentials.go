package agentnode

import (
	"errors"
	"os"
	"runtime"
	"strings"

	agentexec "github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/appfiles"
)

// Resolve once for both startup and execution. --bare deliberately ignores
// Claude's OAuth/Keychain login; a successful version probe cannot prove auth.
// This is Node product policy, not a requirement on all shared protocol users.
func prepareNativeCredentials(config agentexec.ProviderConfig) (agentexec.ProviderConfig, error) {
	if !strings.EqualFold(strings.TrimSpace(config.Provider), "claude") {
		return config, nil
	}
	environment := config.Env
	if environment == nil {
		environment = os.Environ()
	}
	var key, path string
	for _, item := range environment {
		name, value, _ := strings.Cut(item, "=")
		switch name {
		case "ANTHROPIC_API_KEY":
			key = strings.TrimSpace(value)
		case "ANTHROPIC_API_KEY_FILE":
			path = strings.TrimSpace(value)
		}
	}
	required := len(config.DelegationTargets) > 0
	if !required && path == "" {
		// Preserve the existing ordinary Claude authentication/environment path.
		return config, nil
	}
	if key != "" && path != "" {
		return config, errors.New("ANTHROPIC_API_KEY and ANTHROPIC_API_KEY_FILE are mutually exclusive")
	}
	if path != "" {
		if runtime.GOOS == "windows" {
			return config, errors.New("ANTHROPIC_API_KEY_FILE requires POSIX private-file checks; use ANTHROPIC_API_KEY on Windows (DACL validation is not implemented)")
		}
		var err error
		key, err = appfiles.ReadPrivateSecret(path)
		if err != nil {
			// File errors may contain private paths. Report the policy, not the
			// path or secret bytes, in startup diagnostics and Run errors.
			return config, errors.New("ANTHROPIC_API_KEY_FILE must be a readable, nonempty private regular file owned by the current user (no symlinks, at most 64 KiB)")
		}
	}
	if key == "" && required {
		return config, errors.New("Claude delegation uses --bare and requires ANTHROPIC_API_KEY or ANTHROPIC_API_KEY_FILE; OAuth/Keychain login is not used")
	}
	// Pin the same effective environment for the probe and all Runs. Do not
	// forward the secret-file location, even if the caller allowlisted it.
	config.Env = make([]string, 0, len(environment)+1)
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		if name != "ANTHROPIC_API_KEY" && name != "ANTHROPIC_API_KEY_FILE" {
			config.Env = append(config.Env, item)
		}
	}
	if key != "" {
		config.Env = append(config.Env, "ANTHROPIC_API_KEY="+key)
	}
	return config, nil
}
