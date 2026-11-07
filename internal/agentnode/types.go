package agentnode

import (
	"context"
	"time"

	agentexec "github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

// DefaultShutdownTimeout bounds stopping the local helper and public A2A
// listeners after the Runtime Worker has returned.
const DefaultShutdownTimeout = 10 * time.Second

// DefaultDrainTimeout bounds how long a signal-initiated shutdown lets
// in-flight Attempts finish while admission is fenced. It equals the in-flight
// budget the previous hard stop granted. If the SDK drain does not complete, the
// SDK Worker still applies its own shutdown grace before canceling handlers.
const DefaultDrainTimeout = 10 * time.Second

// processShutdownMargin covers the SDK's bounded force-cancel wait after its
// grace (2s), the Runtime session close, store close and local listener stop.
const processShutdownMargin = 5 * time.Second

// ProcessShutdownTimeout is the whole-process bound for a signal-initiated
// shutdown. It is strictly larger than the drain budget plus the SDK Worker's
// internal shutdown grace so that a normal shutdown always finishes closing the
// store and exits successfully instead of racing an equal outer timer.
const ProcessShutdownTimeout = DefaultDrainTimeout + openlinker.RuntimeWorkerDefaultShutdownTimeout + processShutdownMargin

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
	MCPSnapshot       any
	PackageSnapshot   any
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
