package agentnode

import (
	"context"
	"sync"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/agent-adapters/agentexec"
)

// NativeAdapter embeds the canonical Codex/Claude execution backend. Agent Node
// owns hosting and SDK lifecycle, and never reimplements a provider protocol.
type NativeAdapter struct {
	Config      agentexec.ProviderConfig
	once        sync.Once
	provider    agentexec.Provider
	providerErr error
}

// Configuration is fixed for the adapter lifetime. Reuse the provider (and its
// verified transport host) across Attempts; per-Attempt state stays in Run.
func (adapter *NativeAdapter) backend() (agentexec.Provider, error) {
	adapter.once.Do(func() { adapter.provider, adapter.providerErr = agentexec.NewProvider(adapter.Config) })
	return adapter.provider, adapter.providerErr
}

func (adapter *NativeAdapter) Preflight(ctx context.Context) error {
	if _, err := agentexec.CheckProviderCLI(ctx, adapter.Config); err != nil {
		return err
	}
	_, err := adapter.backend()
	return err
}

func (adapter *NativeAdapter) RuntimeFeatures() []string {
	if len(adapter.Config.DelegationTargets) > 0 {
		return []string{openlinker.RuntimeDelegatedRunReadFeature}
	}
	return nil
}

func (adapter *NativeAdapter) Run(ctx context.Context, input any, run RunContext) (any, error) {
	provider, err := adapter.backend()
	if err != nil {
		return nil, err
	}
	native := agentexec.RunContext{
		RunID: run.RunID, AgentID: run.AgentID, Input: input,
		Metadata: map[string]any(run.Metadata), A2A: map[string]any(run.A2A),
		AttemptDeadlineAt: run.AttemptDeadlineAt, RunDeadlineAt: run.RunDeadlineAt, Authority: run.Authority,
		ReadDelegatedRun: run.ReadDelegatedRun,
		Emit: func(eventType string, payload any) error {
			if run.emitChecked != nil {
				return run.emitChecked(eventType, payload)
			}
			if run.Emit != nil {
				run.Emit(eventType, payload)
			}
			return nil
		},
	}
	if run.Conversation != nil && run.Conversation.Source == "core" {
		native.Conversation = run.Conversation
	}
	if run.CallAgent != nil {
		native.CallAgent = func(ctx context.Context, target string, input any, options openlinker.RuntimeCallOptions) (any, error) {
			return run.CallAgent(ctx, target, input, CallAgentOptions{
				IdempotencyKey: options.IdempotencyKey, Reason: options.Reason, Metadata: options.Metadata,
			})
		}
	}
	result, err := provider.Run(ctx, native)
	if err != nil {
		return nil, err
	}
	output := AdapterResult{Status: result.Status, Output: result.Output}
	if value, ok := output.Output.(map[string]any); ok {
		output.Output = JSONMap(value)
	}
	for _, event := range result.Events {
		output.Events = append(output.Events, RunEvent{EventType: event.EventType, Payload: event.Payload})
	}
	if result.Error != nil {
		output.Error = &AgentError{Code: result.Error.Code, Message: result.Error.Message}
	}
	return output, nil
}
