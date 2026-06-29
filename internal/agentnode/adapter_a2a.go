package agentnode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const defaultA2AMessageMethod = "message/send"

type A2AAdapter struct {
	BaseURL             string
	Headers             map[string]string
	Method              string
	AcceptedOutputModes []string
	HTTPClient          *http.Client
	Timeout             time.Duration
}

func (a A2AAdapter) Run(ctx context.Context, input any, runCtx RunContext) (any, error) {
	if a.BaseURL == "" {
		return nil, fmt.Errorf("OPENLINKER_AGENT_NODE_A2A_BASE_URL is required")
	}
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(a.jsonRPCRequest(input, runCtx))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, a.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	req.Header.Set("a2a-version", "1.0")
	for key, value := range a.Headers {
		req.Header.Set(key, value)
	}

	client := a.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	jsonBody, _ := readJSONResponse(res)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("A2A adapter returned %d: %v", res.StatusCode, jsonBody)
	}

	bodyMap, ok := jsonBody.(map[string]any)
	if !ok {
		return AdapterResult{Status: "success", Output: JSONMap{"a2a": jsonBody}}, nil
	}
	if bodyMap["error"] != nil {
		return nil, fmt.Errorf("A2A adapter returned error: %v", bodyMap["error"])
	}

	result := bodyMap["result"]
	if result == nil {
		result = bodyMap
	}
	status, agentErr := a2aRunStatus(result)
	output := JSONMap{"a2a": result}
	if text := strings.TrimSpace(a2aText(result)); text != "" {
		output["text"] = text
	}
	return AdapterResult{
		Status: status,
		Output: output,
		Events: a2aRunEvents(result),
		Error:  agentErr,
	}, nil
}

func (a A2AAdapter) jsonRPCRequest(input any, runCtx RunContext) JSONMap {
	method := strings.TrimSpace(a.Method)
	if method == "" {
		method = defaultA2AMessageMethod
	}
	return JSONMap{
		"jsonrpc": "2.0",
		"id":      a2aMessageID(runCtx),
		"method":  method,
		"params":  a2aParams(input, runCtx, a.AcceptedOutputModes),
	}
}

func a2aParams(input any, runCtx RunContext, acceptedOutputModes []string) JSONMap {
	if params, ok := explicitA2AParams(input); ok {
		return params
	}
	message := JSONMap{
		"messageId": a2aMessageID(runCtx),
		"role":      "user",
		"parts": []JSONMap{{
			"kind": "text",
			"text": a2aInputText(input),
		}},
	}
	if mappedInput, ok := mapFromInput(input); ok {
		if rawMessage, ok := mappedInput["message"]; ok {
			if messageMap, ok := rawMessage.(map[string]any); ok {
				message = JSONMap(messageMap)
			} else if messageMap, ok := rawMessage.(JSONMap); ok {
				message = messageMap
			}
		}
	}
	if len(acceptedOutputModes) == 0 {
		acceptedOutputModes = []string{"application/json", "text/plain", "text/markdown"}
	}
	return JSONMap{
		"message": message,
		"configuration": JSONMap{
			"blocking":            true,
			"acceptedOutputModes": acceptedOutputModes,
		},
		"metadata": JSONMap{
			"openlinker_run_id": runCtx.RunID,
			"openlinker_source": runCtx.Source,
		},
	}
}

func explicitA2AParams(input any) (JSONMap, bool) {
	mappedInput, ok := mapFromInput(input)
	if !ok {
		return nil, false
	}
	for _, key := range []string{"a2a_params", "params"} {
		raw := mappedInput[key]
		switch typed := raw.(type) {
		case JSONMap:
			return typed, true
		case map[string]any:
			return JSONMap(typed), true
		}
	}
	return nil, false
}

func mapFromInput(input any) (map[string]any, bool) {
	switch typed := input.(type) {
	case JSONMap:
		return map[string]any(typed), true
	case map[string]any:
		return typed, true
	default:
		return nil, false
	}
}

func a2aInputText(input any) string {
	mappedInput, ok := mapFromInput(input)
	if ok {
		for _, key := range []string{"text", "query", "task", "prompt"} {
			if value := strings.TrimSpace(fmt.Sprint(mappedInput[key])); value != "" && value != "<nil>" {
				return value
			}
		}
	}
	switch typed := input.(type) {
	case string:
		return typed
	default:
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Sprint(input)
		}
		return string(encoded)
	}
}

func a2aMessageID(runCtx RunContext) string {
	if runCtx.RunID != "" {
		return "msg-" + runCtx.RunID
	}
	return "msg-openlinker-agent-node"
}

func a2aRunStatus(result any) (string, *AgentError) {
	status := strings.ToLower(strings.TrimSpace(a2aStatus(result)))
	switch status {
	case "", "completed", "complete", "success", "succeeded":
		return "success", nil
	case "failed", "failure", "error", "rejected", "canceled", "cancelled":
		return "failed", &AgentError{Code: "A2A_TASK_FAILED", Message: defaultString(a2aText(result), "A2A task failed")}
	default:
		return "success", nil
	}
}

func a2aStatus(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		if status, ok := typed["status"]; ok {
			return a2aStatus(status)
		}
		if state, ok := typed["state"]; ok {
			return fmt.Sprint(state)
		}
	case JSONMap:
		return a2aStatus(map[string]any(typed))
	case string:
		return typed
	}
	return ""
}

func a2aRunEvents(result any) []RunEvent {
	text := strings.TrimSpace(a2aText(result))
	if text == "" {
		return nil
	}
	return []RunEvent{{
		EventType: "run.message.delta",
		Payload:   JSONMap{"text": text},
	}}
}

func a2aText(value any) string {
	parts := make([]string, 0)
	collectA2AText(value, &parts)
	return strings.Join(parts, "\n")
}

func collectA2AText(value any, parts *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		if text, ok := typed["text"].(string); ok && strings.TrimSpace(text) != "" {
			*parts = append(*parts, strings.TrimSpace(text))
		}
		for _, key := range []string{"parts", "artifacts", "history", "message", "messages", "result"} {
			collectA2AText(typed[key], parts)
		}
	case JSONMap:
		collectA2AText(map[string]any(typed), parts)
	case []any:
		for _, item := range typed {
			collectA2AText(item, parts)
		}
	case []JSONMap:
		for _, item := range typed {
			collectA2AText(item, parts)
		}
	}
}
