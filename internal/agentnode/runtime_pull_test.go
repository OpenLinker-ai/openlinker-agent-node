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

func TestNodeRuntimePullHTTPBackendHelperDelegationBuffersEvents(t *testing.T) {
	var callAgentBody map[string]any
	resultCh := make(chan map[string]any, 1)
	claimed := false
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("authorization"); got != "Bearer ol_live_pull_helper" {
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
				"run_id":   "run-pull-helper",
				"agent_id": "agent-pull",
				"input":    JSONMap{"task": "openclaw"},
				"metadata": JSONMap{"source": "pull-test"},
				"a2a": JSONMap{
					"current_run_id":      "run-pull-helper",
					"call_agent_endpoint": "/api/v1/agent-runtime/call-agent",
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent-runtime/call-agent":
			if err := json.NewDecoder(r.Body).Decode(&callAgentBody); err != nil {
				t.Fatal(err)
			}
			writeJSON(w, http.StatusOK, JSONMap{"run_id": "child-run-pull", "status": "success", "output": JSONMap{"answer": "child"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent-runtime/runs/run-pull-helper/result":
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
	defer platform.Close()

	var helperInfo *HelperInfo
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope AdapterEnvelope
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		helperInfo = envelope.AgentNode.Helper
		postHelper(t, helperInfo.Endpoints.Events, helperInfo.Token, JSONMap{
			"run_id":     envelope.RunID,
			"event_type": "run.message.delta",
			"payload":    JSONMap{"text": "pull backend progress"},
		})
		child := postHelper(t, helperInfo.Endpoints.CallAgent, helperInfo.Token, JSONMap{
			"target_agent_id": "target-pull-helper",
			"reason":          "pull delegation",
			"input":           JSONMap{"q": "openclaw"},
		})
		writeJSON(w, http.StatusOK, JSONMap{"output": JSONMap{
			"handled_by":   "openclaw-http",
			"child_run_id": child["run_id"],
		}})
	}))
	defer backend.Close()

	node := &Node{
		APIBase:      platform.URL,
		RuntimeToken: "ol_live_pull_helper",
		Connector: &RuntimePullConnector{
			APIBase:      platform.URL,
			RuntimeToken: "ol_live_pull_helper",
			Wait:         time.Millisecond,
			Heartbeat:    time.Millisecond,
			EmptyRetry:   time.Millisecond,
			MaxRuns:      1,
		},
		Adapter: HTTPAdapter{URL: backend.URL + "/run", Timeout: testTimeout},
		Helper:  &LocalHelperServer{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := node.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer node.Stop(context.Background())

	select {
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for pull helper result")
	case result := <-resultCh:
		if callAgentBody["current_run_id"] != "run-pull-helper" || callAgentBody["target_agent_id"] != "target-pull-helper" {
			t.Fatalf("call agent body = %#v", callAgentBody)
		}
		if result["status"] != "success" {
			t.Fatalf("result = %#v", result)
		}
		output := result["output"].(map[string]any)
		if output["child_run_id"] != "child-run-pull" {
			t.Fatalf("output = %#v", output)
		}
		events := result["events"].([]any)
		if len(events) != 1 || events[0].(map[string]any)["event_type"] != "run.message.delta" {
			t.Fatalf("events = %#v", events)
		}
	}

	expiredBody := JSONMap{
		"event_type": "run.message.delta",
		"payload":    JSONMap{"text": "late"},
	}
	deadline := time.After(testTimeout)
	for {
		status := postHelperStatus(t, helperInfo.Endpoints.Events, helperInfo.Token, expiredBody)
		if status == http.StatusUnauthorized {
			break
		}
		if status != http.StatusOK {
			t.Fatalf("expired helper status = %d", status)
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for helper session to expire")
		case <-time.After(time.Millisecond):
		}
	}
}
