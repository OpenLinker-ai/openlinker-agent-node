package agentnode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
	result, err := client.CallAgent(context.Background(), "run-parent", "target-agent", JSONMap{"q": "hello"}, CallAgentOptions{Reason: "delegate"})
	if err != nil {
		t.Fatal(err)
	}
	if received["current_run_id"] != "run-parent" || received["target_agent_id"] != "target-agent" {
		t.Fatalf("unexpected body: %#v", received)
	}
	body := result.(map[string]any)
	if body["run_id"] != "child-run" {
		t.Fatalf("run_id = %v", body["run_id"])
	}
}
