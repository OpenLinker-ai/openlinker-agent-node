package providerstream

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
)

// Observer accepts JSONL or individual JSON events and emits only normalized
// progress fields. It never emits raw provider payloads.
type Observer struct {
	mu       sync.Mutex
	pending  []byte
	emit     func(string, any) error
	progress func(map[string]any) []map[string]any
}

// NewCodexObserver normalizes Codex progress. suppressItem lets the embedding
// product omit progress for its own tools without putting product policy in
// the parser. A nil callback reports every supported tool kind.
func NewCodexObserver(
	emit func(string, any) error,
	suppressItem func(item map[string]any) bool,
) *Observer {
	return &Observer{emit: emit, progress: func(event map[string]any) []map[string]any {
		if item, ok := event["item"].(map[string]any); ok && suppressItem != nil {
			if suppressItem(item) {
				return nil
			}
		}
		if payload, ok := codexProgressPayload(event); ok {
			return []map[string]any{payload}
		}
		return nil
	}}
}

func (observer *Observer) Write(value []byte) (int, error) {
	if observer == nil || len(value) == 0 {
		return len(value), nil
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.pending = append(observer.pending, value...)
	observer.drainLines(false)
	return len(value), nil
}

func (observer *Observer) Flush() {
	if observer == nil {
		return
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.drainLines(true)
}

func (observer *Observer) drainLines(flush bool) {
	for {
		index := bytes.IndexByte(observer.pending, '\n')
		if index < 0 {
			if flush && len(observer.pending) > 0 {
				observer.observeLine(observer.pending)
				observer.pending = nil
			}
			return
		}
		line := observer.pending[:index]
		observer.pending = observer.pending[index+1:]
		observer.observeLine(line)
	}
}

// ObserveLine processes one complete event. It shares the same serialization
// as Write and Flush; callers must not include multiple JSONL records.
func (observer *Observer) ObserveLine(line []byte) {
	if observer == nil {
		return
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.observeLine(line)
}

func (observer *Observer) observeLine(line []byte) {
	if observer.emit == nil {
		return
	}
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return
	}
	var event map[string]any
	if json.Unmarshal(line, &event) != nil {
		return
	}
	// Progress is best-effort and only contains normalized, non-sensitive fields.
	for _, payload := range observer.progress(event) {
		_ = observer.emit("run.status.changed", payload)
	}
}

func codexProgressPayload(event map[string]any) (map[string]any, bool) {
	eventType, _ := event["type"].(string)
	if eventType != "item.started" && eventType != "item.completed" {
		return nil, false
	}
	item, ok := event["item"].(map[string]any)
	if !ok {
		return nil, false
	}
	toolKind := normalizedCodexToolKind(item["type"])
	if toolKind == "" {
		return nil, false
	}
	phase := "started"
	if eventType == "item.completed" {
		phase = "completed"
		if status, _ := item["status"].(string); strings.EqualFold(strings.TrimSpace(status), "failed") {
			phase = "failed"
		}
	}
	return map[string]any{
		"status":    "provider_tool_" + phase,
		"provider":  "codex",
		"phase":     phase,
		"tool_kind": toolKind,
	}, true
}

func normalizedCodexToolKind(value any) string {
	switch strings.ToLower(strings.TrimSpace(stringValue(value))) {
	case "web_search":
		return "web_search"
	case "command_execution":
		return "command"
	case "mcp_tool_call":
		return "mcp_tool"
	default:
		return ""
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
