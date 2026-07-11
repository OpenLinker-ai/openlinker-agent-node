package agentnode

import (
	"context"
	"time"
)

const DefaultShutdownTimeout = 10 * time.Second

type JSONMap map[string]any

type RunEvent struct {
	EventType string `json:"event_type"`
	Payload   any    `json:"payload,omitempty"`
}

type AgentError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type RunResult struct {
	Status     string      `json:"status"`
	Output     any         `json:"output,omitempty"`
	Events     []RunEvent  `json:"events,omitempty"`
	Error      *AgentError `json:"error,omitempty"`
	DurationMS int64       `json:"duration_ms,omitempty"`
}

type AdapterResult struct {
	Status string
	Output any
	Events []RunEvent
	Error  *AgentError
}

type HelperInfo struct {
	BaseURL   string            `json:"base_url"`
	Token     string            `json:"token"`
	Headers   map[string]string `json:"headers"`
	Endpoints HelperEndpoints   `json:"endpoints"`
}

type HelperEndpoints struct {
	CallAgent string `json:"call_agent"`
	Events    string `json:"events"`
}

type RunContext struct {
	RunID        string
	AgentID      string
	Input        any
	Metadata     JSONMap
	Source       string
	A2A          JSONMap
	Conversation *ConversationContext
	Helper       *HelperInfo

	Emit      func(eventType string, payload any)
	CallAgent func(ctx context.Context, targetAgentID string, input any, options CallAgentOptions) (any, error)

	emitChecked func(eventType string, payload any) error
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

type CallAgentOptions struct {
	IdempotencyKey string
	Reason         string
	Metadata       any
}

type Adapter interface {
	Run(ctx context.Context, input any, runCtx RunContext) (any, error)
}

type AdapterFunc func(ctx context.Context, input any, runCtx RunContext) (any, error)

func (f AdapterFunc) Run(ctx context.Context, input any, runCtx RunContext) (any, error) {
	return f(ctx, input, runCtx)
}
