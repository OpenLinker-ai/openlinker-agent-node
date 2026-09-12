package codexturn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/codexhome"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerprocess"
)

// NativeCommand describes the common native launch mechanism. Callers choose
// model/tool/approval flags; Arguments receives the canonical workspace for
// both the trust key and protocol cwd. Nil Env retains native OS inheritance.
// Container callers supply a different Config.Prepare and never use host cwd
// resolution or native CODEX_HOME preparation for container-owned paths.
type NativeCommand struct {
	Bin, Workspace string
	Arguments      func(canonicalWorkspace string) []string
	Env            []string
	EnvAllowlist   []string
}

func PrepareNative(ctx context.Context, config NativeCommand) (PreparedCommand, error) {
	if config.Arguments == nil {
		return PreparedCommand{}, errors.New("Codex native launch arguments are required")
	}
	absolute, err := filepath.Abs(config.Workspace)
	if err != nil {
		return PreparedCommand{}, fmt.Errorf("resolve Codex workspace: %w", err)
	}
	workspace, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return PreparedCommand{}, fmt.Errorf("resolve Codex workspace: %w", err)
	}
	environment := config.Env
	if environment == nil {
		environment = os.Environ()
	}
	launcher := false
	for _, entry := range environment {
		if entry == codexhome.LauncherEnvironment+"=1" {
			launcher = true
		}
	}
	allowed := append([]string{"CODEX_API_KEY", "CODEX_HOME", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "CODEX_CA_CERTIFICATE", "SSL_CERT_FILE"}, config.EnvAllowlist...)
	environment = append(providerprocess.Environment(environment, allowed), "LC_ALL=C", "LANG=C")
	var cleanup func()
	if launcher {
		environment = append(environment, codexhome.PrepareEnvironment+"=1")
	} else {
		environment, cleanup, err = codexhome.Prepare(environment)
		if err != nil {
			return PreparedCommand{Cleanup: cleanup}, err
		}
	}
	command := exec.CommandContext(ctx, config.Bin, config.Arguments(workspace)...)
	providerprocess.Configure(command)
	command.Dir, command.Env = workspace, environment
	return PreparedCommand{Command: command, Workspace: workspace, Cleanup: cleanup}, nil
}
