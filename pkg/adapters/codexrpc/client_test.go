package codexrpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func pair(t *testing.T) (*Client, net.Conn, *json.Decoder, *json.Encoder) {
	t.Helper()
	left, right := net.Pipe()
	c := New(left, left, func() { _ = left.Close() })
	t.Cleanup(func() { _ = right.Close(); c.Close() })
	return c, right, json.NewDecoder(right), json.NewEncoder(right)
}
func TestClientHandlesServerRequestsWhileCallIsPending(t *testing.T) {
	c, _, read, write := pair(t)
	done := make(chan error, 1)
	go func() {
		var result map[string]any
		done <- c.Call(context.Background(), "turn/start", map[string]any{}, &result)
	}()
	var request Message
	if err := read.Decode(&request); err != nil {
		t.Fatal(err)
	}
	methods := []string{"item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "mcpServer/elicitation/request", "item/tool/requestUserInput", "item/tool/call", "account/chatgptAuthTokens/refresh", "unknown"}
	for _, method := range methods {
		if err := write.Encode(Message{ID: json.RawMessage(`"server-id"`), Method: method, Params: json.RawMessage(`{"threadId":"untrusted","secret":"private"}`)}); err != nil {
			t.Fatal(err)
		}
		var reply Message
		if err := read.Decode(&reply); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(reply)
		if string(reply.ID) != `"server-id"` || strings.Contains(string(raw), "private") || strings.Contains(string(raw), "accept") {
			t.Fatal(string(raw))
		}
		if reply.Error == nil && len(reply.Result) == 0 {
			t.Fatal("unanswered server request")
		}
	}
	_ = write.Encode(Message{ID: request.ID, Result: json.RawMessage(`{"turn":{"id":"current"}}`)})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("request stalled")
	}
}
func TestClientCancellationUnblocksStalledWriter(t *testing.T) {
	c, _, _, _ := pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.Call(ctx, "turn/start", map[string]any{"prompt": strings.Repeat("x", 1<<16)}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { c.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked writer leaked")
	}
}
func TestClientBoundsInvalidAndOversizedStreams(t *testing.T) {
	for _, payload := range []string{"oops\n", `{"result":{}}` + "\n", strings.Repeat("x", MaxOutputBytes+1) + "\n"} {
		c, peer, _, _ := pair(t)
		go func() { _, _ = io.WriteString(peer, payload) }()
		select {
		case <-c.Done():
			if c.Err() == nil {
				t.Fatal("missing error")
			}
		case <-time.After(time.Second):
			t.Fatal("bad stream did not stop")
		}
	}
}
func TestGeneratedTaggedUnionsDecodeDifferentPayloadShapes(t *testing.T) {
	for _, raw := range []string{
		`{"type":"userMessage","id":"1","content":[{"type":"text","text":"input"}]}`,
		`{"type":"reasoning","id":"2","content":["reason"],"summary":[]}`,
		`{"type":"mcpToolCall","result":{"content":[{"type":"text","text":"tool result"}]},"arguments":{"text":"nested"}}`,
		`{"type":"agentMessage","id":"3","phase":"final_answer","text":"answer"}`,
	} {
		var item ThreadItem
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(item)
		if err != nil || strings.Contains(string(got), "eyJ") {
			t.Fatalf("union encoded as base64: %s %v", got, err)
		}
	}
	params := ThreadStartParams{ApprovalPolicy: func() *AskForApproval { p := AskForApproval(`"never"`); return &p }()}
	raw, _ := json.Marshal(params)
	if string(raw) != `{"approvalPolicy":"never"}` {
		t.Fatal(string(raw))
	}
}

func TestClientLifetimeOutputMayExceedMessageLimit(t *testing.T) {
	c, _, read, write := pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		for i := 0; i < 16; i++ {
			var request Message
			if read.Decode(&request) != nil {
				return
			}
			_ = write.Encode(Message{ID: request.ID, Result: json.RawMessage(`{"data":"` + strings.Repeat("x", 512<<10) + `"}`)})
		}
	}()
	for i := 0; i < 16; i++ { // 8 MiB total, each response within the record limit.
		if err := c.Call(ctx, "read", struct{}{}, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNotificationOverflowFailsPendingCallPromptly(t *testing.T) {
	c, _, read, write := pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Call(ctx, "turn/start", struct{}{}, nil) }()
	var request Message
	if err := read.Decode(&request); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= maxQueuedMessages; i++ {
		_ = write.Encode(Message{Method: "item/started", Params: json.RawMessage(`{}`)})
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "notification queue") {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("notification backlog stalled RPC response routing")
	}
}
