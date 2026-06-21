package agentnode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNodeRuntimePullFallbackBuffersEvents(t *testing.T) {
	resultCh := make(chan map[string]any, 1)
	claimed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("authorization"); got != "Bearer ol_live_pull" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent-runtime/heartbeat":
			writeJSON(w, http.StatusOK, JSONMap{"ok": true})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agent-runtime/runs/claim":
			if claimed {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			claimed = true
			writeJSON(w, http.StatusOK, JSONMap{
				"run_id":   "run-pull",
				"agent_id": "agent-pull",
				"input":    JSONMap{"task": "pull"},
				"metadata": JSONMap{"source": "test"},
				"a2a":      JSONMap{"current_run_id": "run-pull"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent-runtime/runs/run-pull/result":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			resultCh <- body
			writeJSON(w, http.StatusOK, JSONMap{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	node := &Node{
		APIBase:      server.URL,
		RuntimeToken: "ol_live_pull",
		Connector: &RuntimePullConnector{
			APIBase:      server.URL,
			RuntimeToken: "ol_live_pull",
			Wait:         time.Millisecond,
			Heartbeat:    time.Millisecond,
			EmptyRetry:   time.Millisecond,
			MaxRuns:      1,
		},
		Adapter: AdapterFunc(func(ctx context.Context, input any, runCtx RunContext) (any, error) {
			runCtx.Emit("run.message.delta", JSONMap{"text": "pull started"})
			return JSONMap{"answer": "done over pull"}, nil
		}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := node.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer node.Stop(context.Background())

	select {
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for pull result")
	case result := <-resultCh:
		if result["status"] != "success" {
			t.Fatalf("result = %#v", result)
		}
		output := result["output"].(map[string]any)
		if output["answer"] != "done over pull" {
			t.Fatalf("output = %#v", output)
		}
		events := result["events"].([]any)
		if len(events) != 1 {
			t.Fatalf("events = %#v", events)
		}
	}
}
