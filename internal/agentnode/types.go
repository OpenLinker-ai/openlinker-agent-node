package agentnode

import (
	"context"
	"time"
)

const DefaultShutdownTimeout = 10 * time.Second

type JSONMap map[string]any

type Assignment struct {
	Type           string  `json:"type,omitempty"`
	RunID          string  `json:"run_id"`
	AgentID        string  `json:"agent_id,omitempty"`
	Input          any     `json:"input,omitempty"`
	Metadata       JSONMap `json:"metadata,omitempty"`
	Source         string  `json:"source,omitempty"`
	ResultEndpoint string  `json:"result_endpoint,omitempty"`
	ResultMethod   string  `json:"result_method,omitempty"`
	ResultRequired bool    `json:"result_required,omitempty"`
	A2A            JSONMap `json:"a2a,omitempty"`
}

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
	RunID    string
	AgentID  string
	Input    any
	Metadata JSONMap
	Source   string
	A2A      JSONMap
	Helper   *HelperInfo

	Emit      func(eventType string, payload any)
	CallAgent func(ctx context.Context, targetAgentID string, input any, options CallAgentOptions) (any, error)
}

type CallAgentOptions struct {
	CurrentRunID string
	Reason       string
	Metadata     any
	Endpoint     string
	TaskCallback *TaskCallbackConfig
}

type TaskCallbackAuthentication struct {
	Scheme      string `json:"scheme,omitempty"`
	Credentials string `json:"credentials,omitempty"`
}

type TaskCallbackConfig struct {
	URL            string                      `json:"url,omitempty"`
	Token          string                      `json:"token,omitempty"`
	Secret         string                      `json:"secret,omitempty"`
	Authentication *TaskCallbackAuthentication `json:"authentication,omitempty"`
	Metadata       any                         `json:"metadata,omitempty"`
	EventTypes     []string                    `json:"event_types,omitempty"`
}

type Adapter interface {
	Run(ctx context.Context, input any, runCtx RunContext) (any, error)
}

type AdapterFunc func(ctx context.Context, input any, runCtx RunContext) (any, error)

func (f AdapterFunc) Run(ctx context.Context, input any, runCtx RunContext) (any, error) {
	return f(ctx, input, runCtx)
}

type Connector interface {
	Start(ctx context.Context, handlers ConnectorHandlers) error
	Stop(ctx context.Context) error
	SupportsLiveEvents() bool
	SendRunEvent(ctx context.Context, runID string, event RunEvent) error
	CompleteRun(ctx context.Context, runID string, result RunResult) error
}

type ConnectorHandlers struct {
	OnReady    func(JSONMap)
	OnAssigned func(Assignment)
	OnError    func(error)
}
