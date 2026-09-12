package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/agentdelegation"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/agenthost"
)

// "plugin" is the frozen agent-host v1 wire contract, not a dependency on
// Plugin. Node owns its delegation transport and advertises no Browser service.
func runTransportCommand(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if len(args) == 2 && args[0] == "plugin" && args[1] == "capabilities" {
		capabilities := agenthost.SupportedCapabilities()
		capabilities.BrowserProxy = false
		if err := json.NewEncoder(output).Encode(capabilities); err != nil {
			return errors.New("agent host capability output failed")
		}
		return nil
	}
	if len(args) == 4 && args[0] == "plugin" && args[1] == "delegation-proxy" && args[2] == "--host" &&
		(args[3] == "codex" || args[3] == "claude") {
		socket := os.Getenv(agentdelegation.SocketEnvironment)
		// The broker owns authority. This stdio process only needs its socket;
		// never use inherited provider or platform credentials as an alternative.
		for _, key := range []string{
			"CODEX_API_KEY", "CODEX_API_KEY_FILE", "ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY_FILE",
			"OPENLINKER_AGENT_TOKEN", "OPENLINKER_USER_TOKEN",
		} {
			if err := os.Unsetenv(key); err != nil {
				return errors.New("agent host credential cleanup failed")
			}
		}
		return agentdelegation.Proxy(ctx, input, output, socket)
	}
	return errors.New("usage: openlinker-agent-node [--version | plugin capabilities | plugin delegation-proxy --host codex|claude]; configure serving through environment variables")
}
