package adapters

import (
	"context"
	"encoding/json"
	"runtime"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/codexrpc"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/codexturn"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerstream"
)

var errCodexSessionMissing = codexturn.ErrSessionMissing

// Node supplies its launch/tool policy; the shared leaf owns the entire turn.
func runCodexRPC(ctx context.Context, bin, workspace, sandbox, sessionID, prompt string, persistent bool, config ProviderConfig, emit func(string, any) error) (string, string, error) {
	overrides := map[string]json.RawMessage{}
	var before func(context.Context, *codexrpc.Client) error
	if config.toolPolicy != nil && runtime.GOOS == "linux" {
		before = func(_ context.Context, _ *codexrpc.Client) error {
			return codexRuntimeFileGrants(config.Env, config.toolPolicy.codexFilesystem, overrides)
		}
	}
	return codexturn.Run(ctx, codexturn.Config{
		BeforeThread: before, ThreadConfig: overrides,
		Prepare: func(processCtx context.Context) (codexturn.PreparedCommand, error) {
			if config.sandbox != nil {
				command := nativeHostCommand(processCtx, config, bin, codexAppServerArguments(config, workspace, sandbox))
				return codexturn.PreparedCommand{Command: command, Workspace: workspace, TrackNativeDescendants: true}, nil
			}
			return codexturn.PrepareNative(processCtx, codexturn.NativeCommand{
				Bin: bin, Workspace: workspace, Env: config.Env, EnvAllowlist: config.EnvAllowlist,
				Arguments: func(cwd string) []string { return codexAppServerArguments(config, cwd, sandbox) },
			})
		},
		UseConfiguredPermissions: config.toolPolicy != nil,
		Sandbox:                  sandbox, SessionID: sessionID, Prompt: prompt, Persistent: persistent,
		Model: config.Model, Approval: config.CodexApproval,
		Observer: providerstream.NewCodexObserver(emit, nil), Emit: emit,
	})
}

func codexAppServerArguments(config ProviderConfig, workspace, sandbox string) []string {
	args := codexLaunchConfiguration(config, sandbox)
	// Do not hydrate account-installed plugins or start remote-control/hooks
	// from a native login. Deep plugin installation belongs to Plugin.
	args = append(args, "--disable", "remote_plugin", "--disable", "remote_control", "--disable", "hooks", "--disable", "apps")
	args = append(args, "--disable", "plugins")
	if config.toolPolicy != nil {
		args = append(args, config.toolPolicy.codex...)
	} else {
		args = append(args, "-c", `cli_auth_credentials_store="file"`)
	}
	args = append(args, "-c", "projects={"+jsonString(workspace)+"={trust_level=\"untrusted\"}}")
	return append(args, "app-server", "--listen", "stdio://")
}

func jsonString(value string) string { raw, _ := json.Marshal(value); return string(raw) }
