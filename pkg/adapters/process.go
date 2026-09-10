package adapters

import (
	"os/exec"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerprocess"
)

func configureProviderProcess(command *exec.Cmd) { providerprocess.Configure(command) }
func sanitizedEnvironment(environment, allowlist []string) []string {
	return providerprocess.Environment(environment, allowlist)
}
