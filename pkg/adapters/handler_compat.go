package adapters

import (
	"context"
	"encoding/json"
	"errors"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
	"strings"
)

// Handler preserves the published SDK handler adapter for existing consumers.
// Deprecated: use NewProvider and compose the SDK RuntimeHandler in the host.
type Handler struct{ Provider Provider }

// NewHandler preserves the published constructor.
// Deprecated: use NewProvider and compose the SDK RuntimeHandler in the host.
func NewHandler(config ProviderConfig) (Handler, error) {
	provider, err := NewProvider(config)
	if err != nil {
		return Handler{}, err
	}
	return Handler{Provider: provider}, nil
}

func (handler Handler) Handle(ctx context.Context, assignment openlinker.RuntimeContext) (result openlinker.RuntimeResult, resultErr error) {
	if handler.Provider == nil {
		return failedResult("PROVIDER_NOT_CONFIGURED", "provider is not configured"), nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			result = failedResult("PROVIDER_PANIC", "provider execution panicked")
			resultErr = nil
		}
	}()
	assignmentMetadata := map[string]any(assignment.Metadata)
	metadata := make(map[string]any, len(assignmentMetadata))
	for key, value := range assignmentMetadata {
		if key != "a2a" && key != "conversation" {
			metadata[key] = value
		}
	}
	run := RunContext{
		RunID:             assignment.RunID,
		AgentID:           assignment.AgentID,
		AttemptDeadlineAt: assignment.AttemptDeadlineAt,
		RunDeadlineAt:     assignment.RunDeadlineAt,
		Authority:         assignment.Authority,
		Input:             assignment.Input,
		Metadata:          metadata,
		A2A:               mapValue(assignmentMetadata["a2a"]),
		Emit:              assignment.Emit,
		RuntimeExtensions: assignment.Extensions,
		CallAgent: func(callCtx context.Context, target string, input any, options openlinker.RuntimeCallOptions) (any, error) {
			return assignment.CallAgent(callCtx, target, input, options)
		},
	}
	if assignment.CanReadDelegatedRuns() {
		run.ReadDelegatedRun = assignment.ReadDelegatedRun
	}
	if conversation := conversationValue(assignmentMetadata["conversation"]); conversation != nil && conversation.Source == "core" {
		run.Conversation = conversation
	}
	result, err := handler.Provider.Run(ctx, run)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return failedResult("PROVIDER_CANCELED", "provider execution was canceled"), nil
		}
		return failedResult("PROVIDER_ERROR", boundedText(err.Error(), 500, "provider failed")), nil
	}
	return normalizeResult(result), nil
}

func conversationValue(value any) *ConversationContext {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var conversation ConversationContext
	if err := json.Unmarshal(raw, &conversation); err != nil {
		return nil
	}
	if strings.TrimSpace(conversation.SessionKey) == "" || strings.TrimSpace(conversation.CurrentRunID) == "" {
		return nil
	}
	return &conversation
}

func mapValue(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case openlinker.RuntimeJSONMap:
		return map[string]any(typed)
	default:
		return map[string]any{}
	}
}

func normalizeResult(result openlinker.RuntimeResult) openlinker.RuntimeResult {
	if result.Error != nil {
		result.Status = "failed"
		return result
	}
	if result.Status == "" {
		result.Status = "success"
	}
	if result.Status != "success" {
		return failedResult("PROVIDER_INVALID_RESULT", "provider returned an invalid result")
	}
	return result
}
