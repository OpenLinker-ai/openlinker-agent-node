package agentnode

import (
	"context"
	"errors"
	"strings"
	"testing"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func TestRuntimeAdapterHandlerPreservesAdapterErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{name: "adapter failure", err: errors.New("backend unavailable"), code: "AGENT_NODE_ERROR"},
		{name: "adapter canceled", err: context.Canceled, code: "ADAPTER_CANCELED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := runtimeAdapterHandler{node: &Node{Adapter: AdapterFunc(func(context.Context, any, RunContext) (any, error) {
				return nil, test.err
			})}}
			result, err := handler.Handle(context.Background(), openlinker.RuntimeContext{})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "failed" || result.Error == nil || result.Error.Code != test.code {
				t.Fatalf("adapter result = %#v", result)
			}
		})
	}
}

func TestRuntimeAdapterHandlerRecoversAdapterPanic(t *testing.T) {
	handler := runtimeAdapterHandler{node: &Node{Adapter: AdapterFunc(func(context.Context, any, RunContext) (any, error) {
		panic("secret panic value")
	})}}
	result, err := handler.Handle(context.Background(), openlinker.RuntimeContext{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || result.Error == nil || result.Error.Code != "ADAPTER_PANIC" || result.Error.Message != "adapter panicked" {
		t.Fatalf("panic result = %#v", result)
	}
}

func TestRuntimeAdapterHandlerProjectsCoreConversationExactlyOnce(t *testing.T) {
	var captured RunContext
	var prompt string
	handler := runtimeAdapterHandler{node: &Node{Adapter: AdapterFunc(func(
		_ context.Context,
		input any,
		runCtx RunContext,
	) (any, error) {
		captured = runCtx
		prompt = BuildCodexPrompt(input, runCtx)
		return JSONMap{"ok": true}, nil
	})}}
	assignment := openlinker.RuntimeContext{
		RunID:   "run-current",
		AgentID: "agent-one",
		Input:   JSONMap{"task": "recall the answer"},
		Metadata: openlinker.RuntimeJSONMap{
			"tenant": "seller-research",
			"a2a": map[string]any{
				"current_run_id":      "run-current",
				"root_context_id":     "conversation-one",
				"protocol_context_id": "conversation-one",
			},
			"conversation": map[string]any{
				"id":             "conversation-one",
				"session_key":    "conversation-one",
				"current_run_id": "run-current",
				"source":         "core",
				"history_before_current": []any{
					map[string]any{
						"run_id":  "run-previous",
						"role":    "user",
						"content": "first question",
					},
					map[string]any{
						"run_id":  "run-previous",
						"role":    "agent",
						"content": "first answer",
					},
				},
			},
		},
	}
	result, err := handler.Handle(context.Background(), assignment)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "success" {
		t.Fatalf("adapter result = %#v", result)
	}
	if captured.Conversation == nil ||
		captured.Conversation.SessionKey != "conversation-one" ||
		len(captured.Conversation.HistoryBeforeCurrent) != 2 {
		t.Fatalf("projected conversation = %#v", captured.Conversation)
	}
	if captured.A2A["root_context_id"] != "conversation-one" {
		t.Fatalf("projected A2A = %#v", captured.A2A)
	}
	if captured.Metadata["tenant"] != "seller-research" ||
		captured.Metadata["a2a"] != nil ||
		captured.Metadata["conversation"] != nil {
		t.Fatalf("adapter metadata = %#v", captured.Metadata)
	}
	if _, exists := assignment.Metadata["conversation"]; !exists {
		t.Fatal("assignment metadata was mutated")
	}
	for _, expected := range []string{
		"conversation.history_before_current",
		"first question",
		"first answer",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt missing %q:\n%s", expected, prompt)
		}
	}
}

func TestRuntimeAdapterHandlerRejectsCallerConversation(t *testing.T) {
	for _, test := range []struct {
		name         string
		conversation map[string]any
	}{
		{
			name: "caller-owned",
			conversation: map[string]any{
				"session_key": "spoofed", "current_run_id": "run-current", "source": "caller",
			},
		},
		{
			name: "malformed-core",
			conversation: map[string]any{
				"session_key": "conversation-one", "source": "core",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var captured RunContext
			handler := runtimeAdapterHandler{node: &Node{Adapter: AdapterFunc(func(
				_ context.Context,
				_ any,
				runCtx RunContext,
			) (any, error) {
				captured = runCtx
				return JSONMap{"ok": true}, nil
			})}}
			assignment := openlinker.RuntimeContext{
				Metadata: openlinker.RuntimeJSONMap{"conversation": test.conversation},
			}
			if _, err := handler.Handle(context.Background(), assignment); err != nil {
				t.Fatal(err)
			}
			if captured.Conversation != nil {
				t.Fatalf("untrusted conversation was projected: %#v", captured.Conversation)
			}
			if _, exists := captured.Metadata["conversation"]; exists {
				t.Fatalf("untrusted conversation leaked through generic metadata: %#v", captured.Metadata)
			}
		})
	}
}
