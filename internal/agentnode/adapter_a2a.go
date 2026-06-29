package agentnode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type A2AAdapter struct {
	BaseURL             string
	Token               string
	Headers             map[string]string
	Method              string
	AcceptedOutputModes []string
	ProtocolVersion     string
	Dialect             string
	HTTPClient          *http.Client
	Timeout             time.Duration
}

func (a A2AAdapter) Run(ctx context.Context, input any, runCtx RunContext) (any, error) {
	if strings.TrimSpace(a.BaseURL) == "" {
		return nil, fmt.Errorf("OPENLINKER_AGENT_NODE_A2A_BASE_URL is required")
	}
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client, err := openlinker.NewA2AClient(
		a.BaseURL,
		openlinker.WithA2AToken(a.Token),
		openlinker.WithA2AHeaders(a.Headers),
		openlinker.WithA2AHTTPClient(a.HTTPClient),
		openlinker.WithA2AProtocolVersion(defaultString(a.ProtocolVersion, "1.0")),
		openlinker.WithA2ADialect(defaultString(a.Dialect, openlinker.A2ADialectCurrent)),
		openlinker.WithA2ASDKAgent("openlinker-agent-node/0.1.0"),
	)
	if err != nil {
		return nil, err
	}
	method := openlinker.NormalizeA2AJSONRPCMethodForDialect(defaultString(a.Method, openlinker.A2AMethodMessageSend), a.Dialect)
	result, err := client.Call(reqCtx, method, a2aAdapterParams(input, runCtx, a.AcceptedOutputModes, a.Dialect))
	if err != nil {
		return nil, err
	}
	status, agentErr := a2aAdapterRunStatus(result)
	output := JSONMap{"a2a": result}
	if text := strings.TrimSpace(openlinker.ExtractA2AText(result)); text != "" {
		output["text"] = text
	}
	return AdapterResult{
		Status: status,
		Output: output,
		Events: a2aAdapterRunEvents(result),
		Error:  agentErr,
	}, nil
}

func a2aAdapterParams(input any, runCtx RunContext, acceptedOutputModes []string, dialect string) any {
	if params, ok := explicitA2AAdapterParams(input); ok {
		return params
	}
	params := openlinker.NewA2ATextMessageParamsForDialect(a2aMessageID(runCtx), a2aInputText(input), acceptedOutputModes, dialect)
	if mappedInput := jsonMapFromAny(input); mappedInput != nil {
		if rawMessage, ok := mappedInput["message"]; ok {
			if message, ok := a2aMessageFromAny(rawMessage); ok {
				params.Message = message
			}
		}
	}
	params.Metadata = map[string]any{
		"openlinker_run_id": runCtx.RunID,
		"openlinker_source": runCtx.Source,
	}
	return params
}

func explicitA2AAdapterParams(input any) (JSONMap, bool) {
	mappedInput := jsonMapFromAny(input)
	if mappedInput == nil {
		return nil, false
	}
	for _, key := range []string{"a2a_params", "params"} {
		if params := jsonMapFromAny(mappedInput[key]); params != nil {
			return params, true
		}
	}
	return nil, false
}

func a2aMessageFromAny(value any) (openlinker.A2AMessage, bool) {
	switch typed := value.(type) {
	case openlinker.A2AMessage:
		return typed, true
	case *openlinker.A2AMessage:
		if typed != nil {
			return *typed, true
		}
	case JSONMap, map[string]any, openlinker.JSON:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return openlinker.A2AMessage{}, false
		}
		var message openlinker.A2AMessage
		if err := json.Unmarshal(encoded, &message); err != nil {
			return openlinker.A2AMessage{}, false
		}
		return message, true
	}
	return openlinker.A2AMessage{}, false
}

func a2aInputText(input any) string {
	if mappedInput := jsonMapFromAny(input); mappedInput != nil {
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
	if strings.TrimSpace(runCtx.RunID) != "" {
		return "msg-" + runCtx.RunID
	}
	return "msg-openlinker-agent-node"
}

func a2aAdapterRunStatus(result any) (string, *AgentError) {
	state := a2aAdapterTaskState(result)
	status := openlinker.A2ATaskStateRunStatus(state)
	if status == "failed" {
		return status, &AgentError{Code: "A2A_TASK_FAILED", Message: defaultString(openlinker.ExtractA2AText(result), "A2A task failed")}
	}
	return status, nil
}

func a2aAdapterTaskState(value any) string {
	switch typed := value.(type) {
	case openlinker.A2ATask:
		return typed.Status.State
	case *openlinker.A2ATask:
		if typed != nil {
			return typed.Status.State
		}
	case map[string]any:
		for _, key := range []string{"task", "statusUpdate", "artifactUpdate"} {
			if nested, ok := typed[key]; ok {
				if state := a2aAdapterTaskState(nested); state != "" {
					return state
				}
			}
		}
		if status, ok := typed["status"]; ok {
			return a2aAdapterTaskState(status)
		}
		if state, ok := typed["state"]; ok {
			return fmt.Sprint(state)
		}
	case JSONMap:
		return a2aAdapterTaskState(map[string]any(typed))
	case openlinker.JSON:
		return a2aAdapterTaskState(map[string]any(typed))
	}
	return ""
}

func a2aAdapterRunEvents(result any) []RunEvent {
	text := strings.TrimSpace(openlinker.ExtractA2AText(result))
	if text == "" {
		return nil
	}
	return []RunEvent{{
		EventType: "run.message.delta",
		Payload:   JSONMap{"text": text},
	}}
}
