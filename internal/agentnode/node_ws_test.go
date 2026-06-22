package agentnode

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestNodeRuntimeWSHTTPBackendHelperDelegation(t *testing.T) {
	var callAgentBody map[string]any
	wsMessages := make(chan map[string]any, 8)
	upgrader := websocket.Upgrader{}

	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent-runtime/call-agent":
			if got := r.Header.Get("authorization"); got != "Bearer ol_live_ws" {
				t.Fatalf("authorization = %q", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&callAgentBody); err != nil {
				t.Fatal(err)
			}
			writeJSON(w, http.StatusOK, JSONMap{"run_id": "child-run-ws", "status": "success", "output": JSONMap{"answer": "child"}})
		case r.URL.Path == "/api/v1/agent-runtime/ws":
			if got := r.Header.Get("authorization"); got != "Bearer ol_live_ws" {
				t.Fatalf("authorization = %q", got)
			}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Fatal(err)
			}
			go func() {
				defer conn.Close()
				_ = conn.WriteJSON(JSONMap{"type": "runtime.ready", "agent_id": "agent-ws"})
				_ = conn.WriteJSON(JSONMap{
					"type":     "run.assigned",
					"run_id":   "run-ws",
					"agent_id": "agent-ws",
					"input":    JSONMap{"task": "openclaw"},
					"metadata": JSONMap{"source": "test"},
					"a2a": JSONMap{
						"current_run_id":      "run-ws",
						"call_agent_endpoint": "/api/v1/agent-runtime/call-agent",
					},
				})
				for {
					var msg map[string]any
					if err := conn.ReadJSON(&msg); err != nil {
						return
					}
					wsMessages <- msg
				}
			}()
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
			"payload":    JSONMap{"text": "backend handling openclaw"},
		})
		child := postHelper(t, helperInfo.Endpoints.CallAgent, helperInfo.Token, JSONMap{
			"target_agent_id": "target-helper",
			"reason":          "backend delegation",
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
		RuntimeToken: "ol_live_ws",
		Connector: &RuntimeWSConnector{
			APIBase:      platform.URL,
			RuntimeToken: "ol_live_ws",
			Reconnect:    false,
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

	event := waitForMessage(t, wsMessages, "run.event")
	result := waitForMessage(t, wsMessages, "run.result")
	if callAgentBody["current_run_id"] != "run-ws" || callAgentBody["target_agent_id"] != "target-helper" {
		t.Fatalf("call agent body = %#v", callAgentBody)
	}
	if payload := event["payload"].(map[string]any); payload["text"] != "backend handling openclaw" {
		t.Fatalf("event = %#v", event)
	}
	output := result["output"].(map[string]any)
	if output["child_run_id"] != "child-run-ws" {
		t.Fatalf("result = %#v", result)
	}

	status := postHelperStatus(t, helperInfo.Endpoints.Events, helperInfo.Token, JSONMap{
		"event_type": "run.message.delta",
		"payload":    JSONMap{"text": "late"},
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("expired helper status = %d", status)
	}
}

func TestNodeRuntimeWSReconnectsAndProcessesAssignment(t *testing.T) {
	var connections int32
	wsMessages := make(chan map[string]any, 8)
	upgrader := websocket.Upgrader{}

	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent-runtime/ws" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("authorization"); got != "Bearer ol_live_reconnect" {
			t.Errorf("authorization = %q", got)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		connectionID := atomic.AddInt32(&connections, 1)
		go func() {
			defer conn.Close()
			if connectionID == 1 {
				_ = conn.WriteJSON(JSONMap{"type": "runtime.ready", "agent_id": "agent-reconnect"})
				return
			}
			_ = conn.WriteJSON(JSONMap{
				"type":     "run.assigned",
				"run_id":   "run-reconnect",
				"agent_id": "agent-reconnect",
				"input":    JSONMap{"task": "after reconnect"},
				"a2a": JSONMap{
					"current_run_id": "run-reconnect",
				},
			})
			for {
				var msg map[string]any
				if err := conn.ReadJSON(&msg); err != nil {
					return
				}
				wsMessages <- msg
				if msg["type"] == "run.result" {
					return
				}
			}
		}()
	}))
	defer platform.Close()

	node := &Node{
		APIBase:      platform.URL,
		RuntimeToken: "ol_live_reconnect",
		Connector: &RuntimeWSConnector{
			APIBase:      platform.URL,
			RuntimeToken: "ol_live_reconnect",
			Reconnect:    true,
			ReconnectMin: time.Millisecond,
			ReconnectMax: 5 * time.Millisecond,
		},
		Adapter: AdapterFunc(func(ctx context.Context, input any, runCtx RunContext) (any, error) {
			runCtx.Emit("run.message.delta", JSONMap{"text": "handled after reconnect"})
			payload := input.(map[string]any)
			return JSONMap{"handled": payload["task"]}, nil
		}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := node.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer node.Stop(context.Background())

	event := waitForMessage(t, wsMessages, "run.event")
	result := waitForMessage(t, wsMessages, "run.result")
	if atomic.LoadInt32(&connections) < 2 {
		t.Fatalf("connections = %d", connections)
	}
	if payload := event["payload"].(map[string]any); payload["text"] != "handled after reconnect" {
		t.Fatalf("event = %#v", event)
	}
	output := result["output"].(map[string]any)
	if output["handled"] != "after reconnect" {
		t.Fatalf("result = %#v", result)
	}
}

func postHelper(t *testing.T, endpoint, token string, body any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var value map[string]any
	if err := json.NewDecoder(res.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("helper returned %d: %#v", res.StatusCode, value)
	}
	return value
}

func postHelperStatus(t *testing.T, endpoint, token string, body any) int {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	return res.StatusCode
}

func waitForMessage(t *testing.T, messages <-chan map[string]any, messageType string) map[string]any {
	t.Helper()
	deadline := time.After(testTimeout)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", messageType)
		case msg := <-messages:
			if msg["type"] == messageType {
				return msg
			}
		}
	}
}
