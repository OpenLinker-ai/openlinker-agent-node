package agentnode

import (
	"errors"
	"reflect"
	"testing"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func TestSDKRuntimeHandlersBridgeCallbacks(t *testing.T) {
	heartbeat := &openlinker.AgentHeartbeatResponse{AgentID: "agent-1", PendingRunCount: 2}
	var ready JSONMap
	var assigned Assignment
	wantErr := errors.New("runtime failed")
	var gotErr error

	handlers := sdkRuntimeHandlers(ConnectorHandlers{
		OnReady: func(message JSONMap) {
			ready = message
		},
		OnAssigned: func(a Assignment) {
			assigned = a
		},
		OnError: func(err error) {
			gotErr = err
		},
	})

	handlers.OnReady(openlinker.RuntimeWSServerMessage{
		Type:      "ready",
		AgentID:   "agent-1",
		Heartbeat: heartbeat,
	})
	if ready["type"] != "ready" || ready["agent_id"] != "agent-1" || ready["heartbeat"] != heartbeat {
		t.Fatalf("ready map = %#v", ready)
	}

	handlers.OnAssigned(openlinker.RuntimeAssignment{
		Type:           "run.assigned",
		RunID:          "run-1",
		AgentID:        "agent-1",
		Input:          map[string]any{"prompt": "hi"},
		Metadata:       openlinker.JSON{"priority": "high"},
		Source:         "runtime",
		ResultEndpoint: "/runs/run-1/result",
		ResultMethod:   "POST",
		ResultRequired: true,
		A2A: &openlinker.AgentA2AContext{
			CurrentRunID:      "run-1",
			ParentRunID:       "parent-1",
			CallerAgentID:     "caller-1",
			CallAgentEndpoint: "/a2a/call",
			CallAgentMethod:   "POST",
			RuntimeTokenType:  "bearer",
			RuntimeScopes:     []string{"agents:run", "runs:read"},
		},
	})
	if assigned.RunID != "run-1" || assigned.Metadata["priority"] != "high" || assigned.A2A["caller_agent_id"] != "caller-1" {
		t.Fatalf("assigned = %#v", assigned)
	}

	handlers.OnError(wantErr)
	if gotErr != wantErr {
		t.Fatalf("OnError = %v, want %v", gotErr, wantErr)
	}

	empty := sdkRuntimeHandlers(ConnectorHandlers{})
	empty.OnReady(openlinker.RuntimeWSServerMessage{Type: "ready"})
	empty.OnAssigned(openlinker.RuntimeAssignment{RunID: "run-ignored"})
}

func TestRuntimeReadyMapOmitsEmptyOptionalFields(t *testing.T) {
	got := runtimeReadyMap(openlinker.RuntimeWSServerMessage{Type: "ready"})
	if !reflect.DeepEqual(got, JSONMap{"type": "ready"}) {
		t.Fatalf("runtimeReadyMap = %#v", got)
	}
}

func TestJSONMapFromA2AAndMetadataVariants(t *testing.T) {
	if got := jsonMapFromA2A(nil); got != nil {
		t.Fatalf("jsonMapFromA2A(nil) = %#v", got)
	}

	a2a := jsonMapFromA2A(&openlinker.AgentA2AContext{
		CurrentRunID:      "run-1",
		ParentRunID:       "parent-1",
		CallerAgentID:     "caller-1",
		ProtocolContextID: "ctx-1",
		ProtocolTaskID:    "task-1",
		RootContextID:     "ctx-root",
		ParentContextID:   "ctx-parent",
		ParentTaskID:      "task-parent",
		TraceID:           "trace-1",
		ReferenceTaskIDs:  []string{"task-parent"},
		CallAgentEndpoint: "/a2a/call",
		CallAgentMethod:   "POST",
		RuntimeTokenType:  "bearer",
		RuntimeScopes:     []string{"agents:run"},
	})
	wantA2A := JSONMap{
		"current_run_id":      "run-1",
		"parent_run_id":       "parent-1",
		"caller_agent_id":     "caller-1",
		"protocol_context_id": "ctx-1",
		"protocol_task_id":    "task-1",
		"root_context_id":     "ctx-root",
		"parent_context_id":   "ctx-parent",
		"parent_task_id":      "task-parent",
		"trace_id":            "trace-1",
		"reference_task_ids":  []string{"task-parent"},
		"call_agent_endpoint": "/a2a/call",
		"call_agent_method":   "POST",
		"runtime_token_type":  "bearer",
		"runtime_scopes":      []string{"agents:run"},
	}
	if !reflect.DeepEqual(a2a, wantA2A) {
		t.Fatalf("jsonMapFromA2A = %#v", a2a)
	}

	local := JSONMap{"local": true}
	if got := jsonMapFromAny(local); !reflect.DeepEqual(got, local) {
		t.Fatalf("jsonMapFromAny(JSONMap) = %#v", got)
	}
	plain := map[string]any{"plain": true}
	if got := jsonMapFromAny(plain); !reflect.DeepEqual(got, JSONMap(plain)) {
		t.Fatalf("jsonMapFromAny(map) = %#v", got)
	}
	sdkJSON := openlinker.JSON{"sdk": true}
	if got := jsonMapFromAny(sdkJSON); !reflect.DeepEqual(got, JSONMap(sdkJSON)) {
		t.Fatalf("jsonMapFromAny(openlinker.JSON) = %#v", got)
	}
	if got := jsonMapFromAny(nil); got != nil {
		t.Fatalf("jsonMapFromAny(nil) = %#v", got)
	}
	if got := jsonMapFromAny("ignored"); got != nil {
		t.Fatalf("jsonMapFromAny(string) = %#v", got)
	}
}

func TestSDKRunResultMapsEventsAndErrors(t *testing.T) {
	got := sdkRunResult(RunResult{
		Status: "failed",
		Output: JSONMap{"partial": true},
		Events: []RunEvent{{
			EventType: "progress",
			Payload:   JSONMap{"pct": 50},
		}},
		Error:      &AgentError{Code: "adapter_failed", Message: "adapter exploded"},
		DurationMS: 1234,
	})

	if got.Status != "failed" || got.DurationMS != 1234 || len(got.Events) != 1 {
		t.Fatalf("sdkRunResult = %#v", got)
	}
	if got.Events[0].EventType != "progress" || got.Error == nil || got.Error.Code != "adapter_failed" {
		t.Fatalf("sdkRunResult events/error = %#v", got)
	}

	empty := sdkRunResult(RunResult{Status: "succeeded"})
	if empty.Events != nil || empty.Error != nil {
		t.Fatalf("sdkRunResult empty optionals = %#v", empty)
	}
}
