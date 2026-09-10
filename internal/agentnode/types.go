package agentnode

import (
	"context"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
	agentexec "github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters"
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
	AttemptDeadlineAt time.Time
	RunDeadlineAt     time.Time
	Authority         *openlinker.RuntimeAuthorityContext
	ReadDelegatedRun  func(context.Context, string) (*openlinker.RuntimeDelegatedRun, error)
	RunID             string
	AgentID           string
	Input             any
	Metadata          JSONMap
	Source            string
	A2A               JSONMap
	Conversation      *ConversationContext
	Helper            *HelperInfo

	Emit      func(eventType string, payload any)
	CallAgent func(ctx context.Context, targetAgentID string, input any, options CallAgentOptions) (any, error)

	emitChecked func(eventType string, payload any) error
}

type ConversationContext = agentexec.ConversationContext
type ConversationMessage = agentexec.ConversationMessage

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
