package agentnode

import (
	"context"
	"errors"
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
