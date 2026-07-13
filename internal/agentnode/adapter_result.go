package agentnode

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

func normalizeAdapterResult(raw any) AdapterResult {
	switch typed := raw.(type) {
	case AdapterResult:
		return fillAdapterDefaults(typed)
	case *AdapterResult:
		if typed == nil {
			return AdapterResult{Status: "success", Output: JSONMap{}}
		}
		return fillAdapterDefaults(*typed)
	case map[string]any:
		if _, hasStatus := typed["status"]; hasStatus {
			return adapterResultFromMap(typed)
		}
		if _, hasOutput := typed["output"]; hasOutput {
			return adapterResultFromMap(typed)
		}
		if _, hasEvents := typed["events"]; hasEvents {
			return adapterResultFromMap(typed)
		}
	}
	return AdapterResult{Status: "success", Output: raw}
}

func adapterResultFromMap(value map[string]any) AdapterResult {
	result := AdapterResult{
		Status: fmt.Sprint(value["status"]),
		Output: value["output"],
		Events: eventsFromAny(value["events"]),
	}
	if result.Status == "" || result.Status == "<nil>" {
		result.Status = "success"
	}
	if result.Output == nil {
		copyValue := JSONMap{}
		for key, raw := range value {
			if key != "status" && key != "output" && key != "events" && key != "error" {
				copyValue[key] = raw
			}
		}
		result.Output = copyValue
	}
	return result
}

func fillAdapterDefaults(result AdapterResult) AdapterResult {
	if result.Status == "" {
		result.Status = "success"
	}
	if result.Output == nil && result.Error == nil {
		result.Output = JSONMap{}
	}
	return result
}

func eventsFromAny(value any) []RunEvent {
	switch typed := value.(type) {
	case []RunEvent:
		return typed
	case []any:
		events := make([]RunEvent, 0, len(typed))
		for _, item := range typed {
			if itemMap, ok := item.(map[string]any); ok {
				events = append(events, RunEvent{
					EventType: fmt.Sprint(itemMap["event_type"]),
					Payload:   itemMap["payload"],
				})
			}
		}
		return events
	default:
		return nil
	}
}

func normalizeAgentError(err error) *AgentError {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return &AgentError{Code: "ADAPTER_CANCELED", Message: "adapter execution was canceled"}
	}
	return &AgentError{Code: "AGENT_NODE_ERROR", Message: boundedRuntimeText(err.Error(), 500, "adapter failed")}
}

func boundedRuntimeText(value string, maximum int, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) {
		value = fallback
	}
	runes := []rune(value)
	if len(runes) > maximum {
		value = string(runes[:maximum])
	}
	return value
}
