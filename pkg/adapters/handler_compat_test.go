package adapters_test

import (
	"context"
	"errors"
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type compatibilityProvider func(context.Context, adapters.RunContext) (openlinker.RuntimeResult, error)

func (f compatibilityProvider) Run(ctx context.Context, run adapters.RunContext) (openlinker.RuntimeResult, error) {
	return f(ctx, run)
}

func TestPublishedHandlerConstructorCompatibility(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		handler, err := adapters.NewHandler(adapters.ProviderConfig{Provider: provider})
		if err != nil || handler.Provider == nil {
			t.Fatalf("existing %s consumer cannot construct a handler: %v", provider, err)
		}
	}
	if _, err := adapters.NewHandler(adapters.ProviderConfig{Provider: "unknown"}); err == nil {
		t.Fatal("unsupported provider was accepted")
	}
}

func TestPublishedHandlerPreservesTrustedContext(t *testing.T) {
	for _, source := range []string{"core", "caller"} {
		t.Run(source, func(t *testing.T) {
			authority := &openlinker.RuntimeAuthorityContext{PrincipalScopeID: "principal"}
			handler := adapters.Handler{Provider: compatibilityProvider(func(_ context.Context, run adapters.RunContext) (openlinker.RuntimeResult, error) {
				if run.RunID != "run" || run.AgentID != "agent" || run.Authority != authority || run.Input != "input" {
					t.Fatal("assignment context changed")
				}
				if len(run.Metadata) != 1 || run.Metadata["custom"] != "value" || run.A2A["contextId"] != "a2a" {
					t.Fatal("reserved metadata leaked or A2A metadata was lost")
				}
				if (run.Conversation != nil) != (source == "core") {
					t.Fatal("conversation trust boundary changed")
				}
				return openlinker.RuntimeResult{Output: "answer"}, nil
			})}
			result, err := handler.Handle(context.Background(), openlinker.RuntimeContext{
				RunID: "run", AgentID: "agent", Authority: authority, Input: "input",
				Metadata: openlinker.RuntimeJSONMap{
					"custom": "value", "a2a": openlinker.RuntimeJSONMap{"contextId": "a2a"},
					"conversation": map[string]any{"session_key": "session", "current_run_id": "run", "source": source},
				},
			})
			if err != nil || result.Status != "success" || result.Output != "answer" {
				t.Fatalf("handler result changed: %#v, %v", result, err)
			}
		})
	}
}

func TestPublishedHandlerFailureCompatibility(t *testing.T) {
	for _, code := range []string{"PROVIDER_NOT_CONFIGURED", "PROVIDER_CANCELED", "PROVIDER_ERROR", "PROVIDER_PANIC", "PROVIDER_INVALID_RESULT", "CUSTOM"} {
		t.Run(code, func(t *testing.T) {
			handler := adapters.Handler{}
			if code != "PROVIDER_NOT_CONFIGURED" {
				handler.Provider = compatibilityProvider(func(context.Context, adapters.RunContext) (openlinker.RuntimeResult, error) {
					switch code {
					case "PROVIDER_CANCELED":
						return openlinker.RuntimeResult{}, context.Canceled
					case "PROVIDER_ERROR":
						return openlinker.RuntimeResult{}, errors.New("fixture error")
					case "PROVIDER_PANIC":
						panic("fixture panic")
					case "CUSTOM":
						return openlinker.RuntimeResult{Error: &openlinker.RuntimeHandlerError{Code: code}}, nil
					default:
						return openlinker.RuntimeResult{Status: "unexpected"}, nil
					}
				})
			}
			result, err := handler.Handle(context.Background(), openlinker.RuntimeContext{})
			if err != nil || result.Status != "failed" || result.Error == nil || result.Error.Code != code {
				t.Fatalf("failure contract changed: %#v, %v", result, err)
			}
		})
	}
}
