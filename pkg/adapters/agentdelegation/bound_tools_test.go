package agentdelegation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func boundToolFixture() BoundTool {
	return BoundTool{BindingID: parentID, TargetID: targetID, Digest: strings.Repeat("a", 64), Name: "mcp_22222222222242228222222222222222", Title: "Synthetic lookup", Description: "Return a marker", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}}, "required": []string{"key"}}}
}
func TestBoundMCPToolsKeepAuthorityOutOfModelArguments(t *testing.T) {
	tool := boundToolFixture()
	calls, ready, used := 0, 0, 0
	b, err := StartBound(context.Background(), "", parentID, nil, []BoundTool{tool}, Callbacks{
		CallAgent: func(_ context.Context, target string, input any, options openlinker.RuntimeCallOptions) (any, error) {
			calls++
			if target != targetID || input.(map[string]any)["key"] != "marker" {
				t.Fatal("model changed target/input")
			}
			ref := options.Metadata.(map[string]any)[CallMetadataKey].(map[string]any)
			if ref["binding_id"] != tool.BindingID || ref["digest"] != tool.Digest {
				t.Fatal("missing trusted grant")
			}
			if options.IdempotencyKey == "" {
				t.Fatal("missing idempotency key")
			}
			return openlinker.RuntimeRunSummary{RunID: childID, Status: openlinker.RuntimeRunRunning}, nil
		},
		ReadRun: func(context.Context, string) (*openlinker.RuntimeDelegatedRun, error) {
			return &openlinker.RuntimeDelegatedRun{RuntimeRunSummary: openlinker.RuntimeRunSummary{RunID: childID, Status: openlinker.RuntimeRunSuccess}, Output: map[string]any{"marker": "ACTUAL-TOOL-RESULT"}}, nil
		},
		BoundToolUsed: func(BoundTool) error { used++; return nil },
	}, func() error { ready++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	listed, err := b.dispatch(rpcRequest{JSONRPC: "2.0", Method: "tools/list"})
	if err != nil {
		t.Fatal(err)
	}
	tools := listed.(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != tool.Name || ready != 1 {
		t.Fatal(listed)
	}
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
		v, e := b.dispatch(rpcRequest{JSONRPC: "2.0", Method: "tools/call", Params: raw})
		if e != nil {
			t.Fatal(e)
		}
		return v.(map[string]any)
	}
	args := map[string]any{"arguments": map[string]any{"key": "marker"}, "request_key": "intent-one"}
	for i := 0; i < 2; i++ {
		result := call(tool.Name, args)
		raw, _ := json.Marshal(result)
		if result["isError"] == true || !strings.Contains(string(raw), "ACTUAL-TOOL-RESULT") {
			t.Fatal(result)
		}
	}
	if calls != 1 || used != 2 {
		t.Fatalf("duplicate tool execution: calls=%d used=%d", calls, used)
	}
	args["target_agent_id"] = childID
	if call(tool.Name, args)["isError"] != true {
		t.Fatal("accepted target injection")
	}
	delete(args, "target_agent_id")
	args["arguments"] = map[string]any{"key": "changed"}
	if call(tool.Name, args)["isError"] != true {
		t.Fatal("accepted changed request with reused key")
	}
	if call("delegate_agent", map[string]any{"target_agent_id": targetID, "input": map[string]any{}, "request_key": "bypass"})["isError"] != true {
		t.Fatal("exposed generic delegation")
	}
	if calls != 1 {
		t.Fatal("rejected requests reached the Worker")
	}
	if e := b.EnsureComplete(); e != nil {
		t.Fatal(e)
	}
}
func TestBoundMCPSnapshotRequiresTrustedAuthorityAndStableSelection(t *testing.T) {
	one := BoundSnapshot{Schema: 1, Tools: []BoundTool{boundToolFixture()}}
	if _, _, err := DecodeBound(one, false); err == nil {
		t.Fatal("untrusted snapshot accepted")
	}
	_, first, err := DecodeBound(one, true)
	if err != nil {
		t.Fatal(err)
	}
	one.Tools[0].BindingID = childID
	_, second, err := DecodeBound(one, true)
	if err != nil || first == second {
		t.Fatal("changed grant retained session fingerprint")
	}
	one.Tools = append(one.Tools, one.Tools[0])
	if _, _, err := DecodeBound(one, true); err == nil {
		t.Fatal("duplicate tool accepted")
	}
}

func TestBoundToolsMustBeDiscoveredBeforeSuccessfulCompletion(t *testing.T) {
	b, err := StartBound(context.Background(), "", parentID, nil, []BoundTool{boundToolFixture()}, Callbacks{
		CallAgent: func(context.Context, string, any, openlinker.RuntimeCallOptions) (any, error) { return nil, nil },
		ReadRun:   func(context.Context, string) (*openlinker.RuntimeDelegatedRun, error) { return nil, nil },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !errors.Is(b.EnsureComplete(), ErrBoundToolsUnavailable) {
		t.Fatal("unavailable MCP silently succeeded")
	}
	if _, err := b.dispatch(rpcRequest{JSONRPC: "2.0", Method: "tools/list"}); err != nil {
		t.Fatal(err)
	}
	if err := b.EnsureComplete(); err != nil {
		t.Fatal(err)
	}
}
