package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type ProviderConfig struct {
	DelegationTargets    []string
	DelegationProxyBin   string
	DelegationBrokerRoot string
	DelegationSocket     string
	// Version is supplied by the embedding executable for provider metadata.
	Version          string
	Provider         string
	Bin              string
	Workspace        string
	Model            string
	Sandbox          string
	Permission       string
	AllowedTools     []string
	Timeout          time.Duration
	SessionReuse     bool
	SessionStore     string
	WebSearch        bool
	CodexApproval    string
	CodexBaseURL     string
	Env              []string
	EnvAllowlist     []string
	ExecutionProfile string
}

type ConversationContext struct {
	ID                   string                `json:"id"`
	SessionKey           string                `json:"session_key"`
	ProtocolContextID    string                `json:"protocol_context_id,omitempty"`
	RootContextID        string                `json:"root_context_id,omitempty"`
	CurrentRunID         string                `json:"current_run_id"`
	CurrentProtocolTask  string                `json:"current_protocol_task_id,omitempty"`
	HistoryBeforeCurrent []ConversationMessage `json:"history_before_current,omitempty"`
	Truncated            bool                  `json:"truncated"`
	Source               string                `json:"source"`
}

type ConversationMessage struct {
	RunID         string         `json:"run_id"`
	EventSequence *int32         `json:"event_sequence,omitempty"`
	Role          string         `json:"role"`
	Content       string         `json:"content"`
	Payload       map[string]any `json:"payload,omitempty"`
	CreatedAt     string         `json:"created_at,omitempty"`
}

type RunContext struct {
	ReadDelegatedRun   func(context.Context, string) (*openlinker.RuntimeDelegatedRun, error)
	DelegationSocket   string
	DelegationProxyBin string
	RunID              string
	AgentID            string
	AttemptDeadlineAt  time.Time
	RunDeadlineAt      time.Time
	Authority          *openlinker.RuntimeAuthorityContext
	Input              any
	Metadata           map[string]any
	A2A                map[string]any
	Conversation       *ConversationContext
	RuntimeExtensions  *openlinker.RuntimeExtensions
	Emit               func(string, any) error
	CallAgent          func(context.Context, string, any, openlinker.RuntimeCallOptions) (any, error)
}

type Provider interface {
	Run(context.Context, RunContext) (openlinker.RuntimeResult, error)
}

type Handler struct{ Provider Provider }

func NewHandler(config ProviderConfig) (Handler, error) {
	provider, err := NewProvider(config)
	if err != nil {
		return Handler{}, err
	}
	return Handler{Provider: provider}, nil
}

func NewProvider(config ProviderConfig) (Provider, error) {
	var provider Provider
	switch strings.ToLower(strings.TrimSpace(config.Provider)) {
	case "codex":
		provider = CodexProvider{Config: config}
	case "claude":
		provider = ClaudeProvider{Config: config}
	default:
		return nil, fmt.Errorf("provider must be codex or claude")
	}
	switch strings.ToLower(strings.TrimSpace(config.ExecutionProfile)) {
	case "", "standard":
		return withDelegation(provider, config)
	default:
		return nil, fmt.Errorf("bridge execution profile must be standard; deep execution requires Plugin")
	}
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

func failedResult(code, message string) openlinker.RuntimeResult {
	return openlinker.RuntimeResult{
		Status: "failed",
		Error:  &openlinker.RuntimeHandlerError{Code: code, Message: message},
	}
}
