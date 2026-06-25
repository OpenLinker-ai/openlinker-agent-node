package agentnode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func TestAgentA2AClientCallAgent(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent-runtime/call-agent" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("authorization"); got != "Bearer ol_live_test" {
			t.Fatalf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, JSONMap{"run_id": "child-run", "status": "success", "output": JSONMap{"answer": "ok"}})
	}))
	defer server.Close()

	client := AgentA2AClient{APIBase: server.URL, RuntimeToken: "ol_live_test"}
	result, err := client.CallAgent(context.Background(), "run-parent", "target-agent", JSONMap{"q": "hello"}, CallAgentOptions{
		Reason: "delegate",
		TaskCallback: &TaskCallbackConfig{
			URL:        "https://caller.example.com/a2a/events",
			Token:      "caller-token",
			Secret:     "caller-secret",
			EventTypes: []string{"run.completed", "run.failed"},
			Metadata:   JSONMap{"client": "agent-node"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if received["current_run_id"] != "run-parent" || received["target_agent_id"] != "target-agent" {
		t.Fatalf("unexpected body: %#v", received)
	}
	push, ok := received["task_callback"].(map[string]any)
	if !ok || push["url"] != "https://caller.example.com/a2a/events" || push["token"] != "caller-token" {
		t.Fatalf("task_callback = %#v", received["task_callback"])
	}
	if push["secret"] != "caller-secret" {
		t.Fatalf("task_callback secret = %#v", push["secret"])
	}
	body := result.(map[string]any)
	if body["run_id"] != "child-run" {
		t.Fatalf("run_id = %v", body["run_id"])
	}
}

func TestAgentA2AClientCallAgentValidationAndErrors(t *testing.T) {
	client := AgentA2AClient{APIBase: "http://127.0.0.1:1", RuntimeToken: "ol_live_test"}
	if _, err := client.CallAgent(context.Background(), "", "target-agent", JSONMap{}, CallAgentOptions{}); err == nil || !strings.Contains(err.Error(), "currentRunID") {
		t.Fatalf("empty currentRunID error = %v", err)
	}
	if _, err := client.CallAgent(context.Background(), "run-parent", "", JSONMap{}, CallAgentOptions{}); err == nil || !strings.Contains(err.Error(), "targetAgentID") {
		t.Fatalf("empty targetAgentID error = %v", err)
	}
	if _, err := (AgentA2AClient{}).CallAgent(context.Background(), "run-parent", "target-agent", JSONMap{}, CallAgentOptions{}); err == nil {
		t.Fatalf("missing API base should fail")
	}

	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/custom/a2a/call" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusBadGateway, JSONMap{"error": JSONMap{"code": "A2A_DOWNSTREAM", "message": "downstream failed"}})
	}))
	defer server.Close()

	client = AgentA2AClient{APIBase: server.URL, RuntimeToken: "ol_live_test"}
	_, err := client.CallAgent(context.Background(), "run-parent", "target-agent", JSONMap{"q": "hello"}, CallAgentOptions{
		Endpoint: "/custom/a2a/call",
		Reason:   "delegate",
		Metadata: JSONMap{"trace": "edge"},
	})
	if err == nil || !strings.Contains(err.Error(), "A2A_DOWNSTREAM") {
		t.Fatalf("expected downstream error, got %v", err)
	}
	if received["reason"] != "delegate" || received["metadata"].(map[string]any)["trace"] != "edge" {
		t.Fatalf("request body = %#v", received)
	}

	if _, err := runResponseToJSONMap(&openlinker.RunResponse{RunID: "run-bad", Output: make(chan int)}); err == nil {
		t.Fatalf("unmarshalable run response should fail")
	}
}
