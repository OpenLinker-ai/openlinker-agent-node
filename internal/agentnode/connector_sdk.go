package agentnode

import (
	"context"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func sdkRuntimeHandlers(handlers ConnectorHandlers) openlinker.RuntimeHandlers {
	return openlinker.RuntimeHandlers{
		OnReady: func(message openlinker.RuntimeWSServerMessage) {
			if handlers.OnReady != nil {
				handlers.OnReady(runtimeReadyMap(message))
			}
		},
		OnAssigned: func(assignment openlinker.RuntimeAssignment) {
			if handlers.OnAssigned != nil {
				handlers.OnAssigned(assignmentFromSDK(assignment))
			}
		},
		OnError: handlers.OnError,
	}
}

func runtimeReadyMap(message openlinker.RuntimeWSServerMessage) JSONMap {
	out := JSONMap{"type": message.Type}
	if message.AgentID != "" {
		out["agent_id"] = message.AgentID
	}
	if message.Heartbeat != nil {
		out["heartbeat"] = message.Heartbeat
	}
	return out
}

func assignmentFromSDK(assignment openlinker.RuntimeAssignment) Assignment {
	return Assignment{
		Type:           assignment.Type,
		RunID:          assignment.RunID,
		AgentID:        assignment.AgentID,
		Input:          assignment.Input,
		Metadata:       jsonMapFromAny(assignment.Metadata),
		Source:         assignment.Source,
		ResultEndpoint: assignment.ResultEndpoint,
		ResultMethod:   assignment.ResultMethod,
		ResultRequired: assignment.ResultRequired,
		A2A:            jsonMapFromA2A(assignment.A2A),
		Conversation:   conversationFromSDK(assignment.Conversation),
	}
}

func conversationFromSDK(conversation *openlinker.ConversationContext) *ConversationContext {
	if conversation == nil {
		return nil
	}
	out := &ConversationContext{
		ID:                  conversation.ID,
		SessionKey:          conversation.SessionKey,
		ProtocolContextID:   conversation.ProtocolContextID,
		RootContextID:       conversation.RootContextID,
		CurrentRunID:        conversation.CurrentRunID,
		CurrentProtocolTask: conversation.CurrentProtocolTask,
		Truncated:           conversation.Truncated,
		Source:              conversation.Source,
	}
	if len(conversation.HistoryBeforeCurrent) > 0 {
		out.HistoryBeforeCurrent = make([]ConversationMessage, 0, len(conversation.HistoryBeforeCurrent))
		for _, message := range conversation.HistoryBeforeCurrent {
			out.HistoryBeforeCurrent = append(out.HistoryBeforeCurrent, ConversationMessage{
				RunID:         message.RunID,
				EventSequence: message.EventSequence,
				Role:          message.Role,
				Content:       message.Content,
				Payload:       message.Payload,
				CreatedAt:     message.CreatedAt,
			})
		}
	}
	return out
}

func jsonMapFromA2A(a2a *openlinker.AgentA2AContext) JSONMap {
	if a2a == nil {
		return nil
	}
	out := JSONMap{}
	if a2a.CurrentRunID != "" {
		out["current_run_id"] = a2a.CurrentRunID
	}
	if a2a.ParentRunID != "" {
		out["parent_run_id"] = a2a.ParentRunID
	}
	if a2a.CallerAgentID != "" {
		out["caller_agent_id"] = a2a.CallerAgentID
	}
	if a2a.ProtocolContextID != "" {
		out["protocol_context_id"] = a2a.ProtocolContextID
	}
	if a2a.ProtocolTaskID != "" {
		out["protocol_task_id"] = a2a.ProtocolTaskID
	}
	if a2a.RootContextID != "" {
		out["root_context_id"] = a2a.RootContextID
	}
	if a2a.ParentContextID != "" {
		out["parent_context_id"] = a2a.ParentContextID
	}
	if a2a.ParentTaskID != "" {
		out["parent_task_id"] = a2a.ParentTaskID
	}
	if a2a.TraceID != "" {
		out["trace_id"] = a2a.TraceID
	}
	if len(a2a.ReferenceTaskIDs) > 0 {
		out["reference_task_ids"] = a2a.ReferenceTaskIDs
	}
	if a2a.CallAgentEndpoint != "" {
		out["call_agent_endpoint"] = a2a.CallAgentEndpoint
	}
	if a2a.CallAgentMethod != "" {
		out["call_agent_method"] = a2a.CallAgentMethod
	}
	if a2a.AgentTokenType != "" {
		out["agent_token_type"] = a2a.AgentTokenType
	}
	if len(a2a.AgentScopes) > 0 {
		out["agent_scopes"] = a2a.AgentScopes
	}
	return out
}

func jsonMapFromAny(value any) JSONMap {
	switch typed := value.(type) {
	case nil:
		return nil
	case JSONMap:
		return typed
	case map[string]any:
		return JSONMap(typed)
	case openlinker.JSON:
		return JSONMap(typed)
	default:
		return nil
	}
}

func sdkRunResult(result RunResult) openlinker.RuntimePullResultRequest {
	return openlinker.RuntimePullResultRequest{
		Status:     result.Status,
		Output:     result.Output,
		Events:     sdkEventsFromRunEvents(result.Events),
		Error:      sdkErrorFromAgentError(result.Error),
		DurationMS: clampInt64ToInt32(result.DurationMS),
	}
}

func sdkEventsFromRunEvents(events []RunEvent) []openlinker.AgentEvent {
	if len(events) == 0 {
		return nil
	}
	out := make([]openlinker.AgentEvent, 0, len(events))
	for _, event := range events {
		out = append(out, openlinker.AgentEvent{
			EventType: event.EventType,
			Payload:   event.Payload,
		})
	}
	return out
}

func sdkErrorFromAgentError(agentErr *AgentError) *openlinker.AgentError {
	if agentErr == nil {
		return nil
	}
	return &openlinker.AgentError{
		Code:    agentErr.Code,
		Message: agentErr.Message,
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
