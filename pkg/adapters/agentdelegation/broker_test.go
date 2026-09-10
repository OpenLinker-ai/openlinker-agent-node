package agentdelegation

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

const parentID = "11111111-1111-4111-8111-111111111111"
const targetID = "22222222-2222-4222-8222-222222222222"
const childID = "33333333-3333-4333-8333-333333333333"

func TestBrokerDelegatesRetriesAndReadsResults(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	key := ""
	broker, err := Start(ctx, "", parentID, []string{targetID}, Callbacks{
		CallAgent: func(ctx context.Context, target string, input any, options openlinker.RuntimeCallOptions) (any, error) {
			calls++
			if target != targetID || input.(map[string]any)["prompt"] != "review" {
				t.Error("task changed")
			}
			if key != "" && key != options.IdempotencyKey {
				t.Error("retry changed idempotency key")
			}
			key = options.IdempotencyKey
			if calls == 1 {
				return nil, errors.New("lost response with SECRET_TOKEN")
			}
			return openlinker.RuntimeRunSummary{RunID: childID, Status: openlinker.RuntimeRunRunning, DispatchState: openlinker.RuntimeDispatchPending}, nil
		},
		ReadRun: func(context.Context, string) (*openlinker.RuntimeDelegatedRun, error) {
			return &openlinker.RuntimeDelegatedRun{RuntimeRunSummary: openlinker.RuntimeRunSummary{RunID: childID, Status: openlinker.RuntimeRunSuccess, DispatchState: openlinker.RuntimeDispatchTerminal}, Output: map[string]any{"summary": "review completed"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	info, err := os.Stat(broker.directory)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode: %v %v", info, err)
	}
	connection, err := net.Dial("unix", broker.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	scanner := bufio.NewScanner(connection)
	request := func(method string, params any) map[string]any {
		t.Helper()
		if err := json.NewEncoder(connection).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		if !scanner.Scan() {
			t.Fatalf("missing MCP response: %v", scanner.Err())
		}
		var result map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	init := request("initialize", map[string]any{})
	if init["error"] != nil {
		t.Fatal(init)
	}
	unknown := request("unknown", map[string]any{})
	if unknown["error"].(map[string]any)["code"] != float64(-32601) {
		t.Fatal(unknown)
	}
	listed := request("tools/list", map[string]any{})
	if len(listed["result"].(map[string]any)["tools"].([]any)) != 3 {
		t.Fatal(listed)
	}
	args := map[string]any{"target_agent_id": targetID, "input": map[string]any{"prompt": "review"}, "request_key": "review-1"}
	first := request("tools/call", map[string]any{"name": "delegate_agent", "arguments": args})
	encoded, _ := json.Marshal(first)
	if strings.Contains(string(encoded), "SECRET_TOKEN") || !strings.Contains(string(encoded), "isError") {
		t.Fatal(string(encoded))
	}
	second := request("tools/call", map[string]any{"name": "delegate_agent", "arguments": args})
	if second["result"].(map[string]any)["isError"] == true || calls != 2 {
		t.Fatal(second)
	}
	args["input"] = map[string]any{"prompt": "changed"}
	conflict := request("tools/call", map[string]any{"name": "delegate_agent", "arguments": args})
	if conflict["result"].(map[string]any)["isError"] != true || calls != 2 {
		t.Fatal(conflict)
	}
	result := request("tools/call", map[string]any{"name": "wait_delegated_run", "arguments": map[string]any{"child_run_id": childID, "wait_ms": 100}})
	encoded, _ = json.Marshal(result)
	if !strings.Contains(string(encoded), "review completed") {
		t.Fatal(string(encoded))
	}
	if err := broker.EnsureComplete(); err != nil {
		t.Fatal(err)
	}
	socket := broker.Socket
	broker.Close()
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("socket not removed: %v", err)
	}
}

func TestDefiniteRejectionsDoNotPoisonParent(t *testing.T) {
	for _, test := range []struct {
		status int
		code   string
	}{
		{403, "PERMISSION_DENIED"}, {403, "FORBIDDEN"}, {401, "UNAUTHORIZED"},
		{422, "VALIDATION_FAILED"}, {400, "BAD_REQUEST"}, {429, "RATE_LIMITED"},
	} {
		t.Run(test.code, func(t *testing.T) {
			calls := 0
			b := &Broker{ctx: context.Background(), runID: parentID, targets: []string{targetID}, requests: map[string]delegationRequest{}}
			b.callbacks.CallAgent = func(context.Context, string, any, openlinker.RuntimeCallOptions) (any, error) {
				calls++
				if calls == 1 {
					return nil, fmt.Errorf("wrapped: %w", &openlinker.Error{StatusCode: test.status, Code: test.code, Message: "SECRET", ResponseBody: []byte("SECRET")})
				}
				return openlinker.RuntimeRunSummary{RunID: childID, Status: openlinker.RuntimeRunRunning}, nil
			}
			b.callbacks.ReadRun = func(context.Context, string) (*openlinker.RuntimeDelegatedRun, error) {
				return &openlinker.RuntimeDelegatedRun{RuntimeRunSummary: openlinker.RuntimeRunSummary{Status: openlinker.RuntimeRunSuccess}}, nil
			}
			params, _ := json.Marshal(map[string]any{"name": "delegate_agent", "arguments": map[string]any{"target_agent_id": targetID, "input": map[string]any{}, "request_key": "rejected"}})
			result, err := b.dispatch(rpcRequest{JSONRPC: "2.0", Method: "tools/call", Params: params})
			raw, _ := json.Marshal(result)
			if err != nil || !strings.Contains(string(raw), "No child was created") || strings.Contains(string(raw), "SECRET") {
				t.Fatalf("unsafe rejection: %s %v", raw, err)
			}
			if err := b.EnsureComplete(); err != nil {
				t.Fatal("definite rejection poisoned completion", err)
			}
			if _, err := b.call("delegate_agent", delegationArgs("other")); err != nil {
				t.Fatal("other key was blocked", err)
			}
			if _, err := b.call("delegate_agent", delegationArgs("other")); err != nil || calls != 2 {
				t.Fatal("known child was recreated", err, calls)
			}
			if err := b.EnsureComplete(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func delegationArgs(key string) []byte {
	raw, _ := json.Marshal(map[string]any{"target_agent_id": targetID, "input": map[string]any{}, "request_key": key})
	return raw
}

func TestUnknownCreationRemainsUnresolvedEvenWhenRetryIsRejected(t *testing.T) {
	for _, first := range []error{context.DeadlineExceeded, errors.New("lost response"), &openlinker.Error{StatusCode: 503, Code: "SERVICE_UNAVAILABLE"}, &openlinker.Error{StatusCode: 403, Code: "UNKNOWN"}, &openlinker.Error{StatusCode: 404, Code: "NOT_FOUND"}} {
		calls := 0
		b := &Broker{ctx: context.Background(), runID: parentID, targets: []string{targetID}, requests: map[string]delegationRequest{}}
		b.callbacks.CallAgent = func(context.Context, string, any, openlinker.RuntimeCallOptions) (any, error) {
			calls++
			if calls == 1 {
				return nil, first
			}
			return nil, &openlinker.Error{StatusCode: 403, Code: "PERMISSION_DENIED"}
		}
		for i := 0; i < 2; i++ {
			if _, err := b.call("delegate_agent", delegationArgs("unknown")); err == nil {
				t.Fatal("accepted unknown outcome")
			}
			if _, err := b.call("delegate_agent", delegationArgs("different")); err == nil {
				t.Fatal("allowed another child before resolution")
			}
			if err := b.EnsureComplete(); err == nil {
				t.Fatal("unknown child hidden by successful parent")
			}
		}
		if calls != 2 {
			t.Fatal("different key reached Core")
		}
	}
}

func TestTerminalHistoryIsReadOnce(t *testing.T) {
	b := &Broker{ctx: context.Background(), runID: parentID, targets: []string{targetID}, requests: map[string]delegationRequest{}}
	created, reads := 0, 0
	b.callbacks.CallAgent = func(context.Context, string, any, openlinker.RuntimeCallOptions) (any, error) {
		created++
		return openlinker.RuntimeRunSummary{RunID: fmt.Sprintf("33333333-3333-4333-8333-%012d", created), Status: openlinker.RuntimeRunRunning}, nil
	}
	b.callbacks.ReadRun = func(_ context.Context, id string) (*openlinker.RuntimeDelegatedRun, error) {
		reads++
		return &openlinker.RuntimeDelegatedRun{RuntimeRunSummary: openlinker.RuntimeRunSummary{RunID: id, Status: openlinker.RuntimeRunTimeout}}, nil
	}
	for i := 0; i < 64; i++ {
		if _, err := b.call("delegate_agent", delegationArgs(fmt.Sprint(i))); err != nil {
			t.Fatal(err)
		}
	}
	if reads != 63 {
		t.Fatalf("historical records re-read: %d calls", reads)
	}
	if err := b.EnsureComplete(); err != nil {
		t.Fatal(err)
	}
	if err := b.EnsureComplete(); err != nil {
		t.Fatal(err)
	}
	if reads != 64 {
		t.Fatalf("completion re-read terminal history: %d calls", reads)
	}
}

func TestBrokerRejectsAuthorityInjectionAndStopsWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := 0
	reading := make(chan struct{}, 1)
	broker, err := Start(ctx, "", parentID, []string{targetID}, Callbacks{
		CallAgent: func(context.Context, string, any, openlinker.RuntimeCallOptions) (any, error) {
			called++
			return nil, nil
		},
		ReadRun: func(ctx context.Context, _ string) (*openlinker.RuntimeDelegatedRun, error) {
			reading <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	for _, raw := range []string{
		`{"target_agent_id":"` + targetID + `","input":{},"request_key":"x","parent_run_id":"` + parentID + `"}`,
		`{"target_agent_id":"` + parentID + `","input":{},"request_key":"x"}`,
	} {
		if _, err := broker.call("delegate_agent", []byte(raw)); err == nil {
			t.Fatal("accepted invalid authority")
		}
	}
	if called != 0 {
		t.Fatal("invalid request called Worker")
	}
	done := make(chan error, 1)
	go func() {
		_, err := broker.call("wait_delegated_run", []byte(`{"child_run_id":"`+childID+`"}`))
		done <- err
	}()
	<-reading
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait survived Attempt cancellation")
	}
}
