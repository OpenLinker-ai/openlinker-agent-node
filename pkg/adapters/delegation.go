package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/agentdelegation"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/agenthost"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type delegationProvider struct {
	provider Provider
	config   ProviderConfig
	host     *agenthost.Resolver
}

func withDelegation(provider Provider, config ProviderConfig) (Provider, error) {
	host := agenthost.NewResolver(config.DelegationProxyBin, "delegation_proxy")
	if len(config.DelegationTargets) == 0 {
		return delegationProvider{provider: provider, config: config, host: host}, nil
	}
	if err := agentdelegation.ValidateTargets(config.DelegationTargets); err != nil {
		return nil, err
	}
	binary, err := host.Resolve(context.Background())
	if err != nil {
		return nil, err
	}
	config.DelegationProxyBin = binary
	return delegationProvider{provider: provider, config: config, host: host}, nil
}

func (provider delegationProvider) Run(ctx context.Context, run RunContext) (openlinker.RuntimeResult, error) {
	tools, digest, err := agentdelegation.DecodeBound(run.MCPSnapshot, run.Authority != nil)
	if err != nil {
		return openlinker.RuntimeResult{}, err
	}
	if len(tools) == 0 && len(provider.config.DelegationTargets) == 0 {
		return provider.provider.Run(ctx, run)
	}
	unavailable := func(err error) (openlinker.RuntimeResult, error) {
		if len(tools) == 0 {
			return openlinker.RuntimeResult{}, err
		}
		if run.Emit != nil {
			if emitErr := run.Emit("run.mcp_bindings.failed", agentdelegation.BoundReceipt(tools)); emitErr != nil {
				return openlinker.RuntimeResult{}, emitErr
			}
		}
		return failedResult("MCP_TOOLS_UNAVAILABLE", "Selected MCP tools could not be loaded. Review the Host and tool configuration."), nil
	}
	binary, err := provider.host.Resolve(ctx)
	if err != nil {
		return unavailable(err)
	}
	run.BoundMCPTools, run.BoundMCPDigest = tools, digest

	if run.ReadDelegatedRun == nil {
		return unavailable(openlinker.ErrRuntimeDelegationUnsupported)
	}
	broker, err := agentdelegation.StartBound(ctx, provider.config.DelegationBrokerRoot, run.RunID, provider.config.DelegationTargets, tools, agentdelegation.Callbacks{
		CallAgent: run.CallAgent, ReadRun: run.ReadDelegatedRun,
		BoundToolUsed: func(tool agentdelegation.BoundTool) error {
			if run.Emit == nil {
				return nil
			}
			return run.Emit("run.mcp_bindings.used", agentdelegation.BoundReceipt([]agentdelegation.BoundTool{tool}))
		},
	}, func() error {
		if len(tools) == 0 || run.Emit == nil {
			return nil
		}
		return run.Emit("run.mcp_bindings.ready", agentdelegation.BoundReceipt(tools))
	})
	if err != nil {
		return unavailable(err)
	}
	defer broker.Close()
	run.DelegationSocket = broker.Socket
	run.DelegationProxyBin = binary
	result, err := provider.provider.Run(ctx, run)
	if len(tools) > 0 && !broker.ToolsDiscovered() && (err != nil || result.Error != nil || (result.Status != "" && result.Status != "success")) && run.Emit != nil {
		// Record the observed stage without relabeling authentication/network/
		// cancellation as an MCP failure or exposing raw provider diagnostics.
		receipt := agentdelegation.BoundReceipt(tools)
		receipt["stage"] = "provider_ended_before_discovery"
		if emitErr := run.Emit("run.mcp_bindings.failed", receipt); emitErr != nil && err == nil {
			return result, emitErr
		}
	}
	if err == nil && result.Error == nil && (result.Status == "" || result.Status == "success") {
		if completionErr := broker.EnsureComplete(); completionErr != nil {
			if errors.Is(completionErr, agentdelegation.ErrBoundToolsUnavailable) {
				return unavailable(completionErr)
			}
			return failedResult("DELEGATION_UNFINISHED", "A delegated Run is unfinished or its result could not be verified."), nil
		}
	}
	return result, err
}

func providerConfigForDelegationRun(config ProviderConfig, run RunContext) ProviderConfig {
	if run.DelegationSocket == "" {
		return config
	}
	config.boundMCPTools, config.boundMCPDigest = run.BoundMCPTools, run.BoundMCPDigest
	config.DelegationSocket = run.DelegationSocket
	config.DelegationProxyBin = run.DelegationProxyBin
	environment := config.Env
	if environment == nil {
		environment = os.Environ()
	}
	config.Env = append(removeDelegationSocket(environment), agentdelegation.SocketEnvironment+"="+run.DelegationSocket)
	config.EnvAllowlist = appendUniqueString(config.EnvAllowlist, agentdelegation.SocketEnvironment)
	return config
}

func removeDelegationSocket(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, value := range environment {
		if !strings.HasPrefix(value, agentdelegation.SocketEnvironment+"=") {
			result = append(result, value)
		}
	}
	return result
}

func codexDelegationMCPArguments(config ProviderConfig) []string {
	if config.DelegationSocket == "" {
		return nil
	}
	command, _ := json.Marshal(config.DelegationProxyBin)
	arguments, _ := json.Marshal(agenthost.DelegationProxyArguments("codex"))
	names, _ := json.Marshal(agentdelegation.BoundToolNames(config.DelegationTargets, config.boundMCPTools))
	return []string{
		"-c", "mcp_servers.openlinker_delegation.command=" + string(command),
		"-c", "mcp_servers.openlinker_delegation.args=" + string(arguments),
		"-c", `mcp_servers.openlinker_delegation.env_vars=["` + agentdelegation.SocketEnvironment + `"]`,
		"-c", "mcp_servers.openlinker_delegation.required=true",
		"-c", "mcp_servers.openlinker_delegation.enabled_tools=" + string(names),
		"-c", `mcp_servers.openlinker_delegation.default_tools_approval_mode="approve"`,
	}
}

func claudeRunMCPConfig(config ProviderConfig) string {
	payload := map[string]any{"mcpServers": map[string]any{}}
	if config.DelegationSocket != "" {
		payload["mcpServers"].(map[string]any)["openlinker_delegation"] = map[string]any{
			"type": "stdio", "command": config.DelegationProxyBin,
			"args": agenthost.DelegationProxyArguments("claude"),
			"env":  map[string]string{agentdelegation.SocketEnvironment: config.DelegationSocket},
		}
	}
	raw, _ := json.Marshal(payload)
	return string(raw)
}

func delegationSessionMode(config ProviderConfig, mode string) string {
	if config.boundMCPDigest != "" {
		mode += "_mcp_v1_" + config.boundMCPDigest
	}
	if len(config.DelegationTargets) == 0 {
		return mode
	}
	targets := append([]string(nil), config.DelegationTargets...)
	sort.Strings(targets)
	// Permission/tool changes must not revive a native session whose MCP tools
	// were configured under a different allowlist. Socket paths rotate per Run
	// and are deliberately absent from this stable fingerprint.
	raw, _ := json.Marshal(struct {
		Targets, Tools []string
		Bin            string
	}{targets, agentdelegation.ToolNames, config.DelegationProxyBin})
	digest := sha256.Sum256(raw)
	return mode + "_delegation_v1_" + hex.EncodeToString(digest[:12])
}

func appendUniqueString(values []string, candidate string) []string {
	for _, value := range values {
		if value == candidate {
			return values
		}
	}
	return append(values, candidate)
}
