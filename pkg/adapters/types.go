package adapters

import (
	"context"
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

func failedResult(code, message string) openlinker.RuntimeResult {
	return openlinker.RuntimeResult{
		Status: "failed",
		Error:  &openlinker.RuntimeHandlerError{Code: code, Message: message},
	}
}
