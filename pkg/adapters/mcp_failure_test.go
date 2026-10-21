package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/agentdelegation"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providertest"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type failingMCPProvider struct {
	discovered bool
	cause      error
}

func (p failingMCPProvider) Run(_ context.Context, run RunContext) (openlinker.RuntimeResult, error) {
	if p.discovered {
		c, err := net.Dial("unix", run.DelegationSocket)
		if err != nil {
			return openlinker.RuntimeResult{}, err
		}
		defer c.Close()
		if err = json.NewEncoder(c).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}); err != nil {
			return openlinker.RuntimeResult{}, err
		}
		var result map[string]any
		if err = json.NewDecoder(c).Decode(&result); err != nil {
			return openlinker.RuntimeResult{}, err
		}
	}
	return openlinker.RuntimeResult{}, p.cause
}
func TestMCPProviderFailurePreservesCauseAndDiscoveryStage(t *testing.T) {
	for _, discovered := range []bool{false, true} {
		t.Run(map[bool]string{false: "before discovery", true: "after discovery"}[discovered], func(t *testing.T) {
			host := filepath.Join(t.TempDir(), "host")
			providertest.WriteMCPFixture(t, host, "claude")
			cause := errors.New("synthetic provider authentication failure")
			p, err := withDelegation(failingMCPProvider{discovered, cause}, ProviderConfig{Provider: "claude", DelegationProxyBin: host})
			if err != nil {
				t.Fatal(err)
			}
			tool := agentdelegation.BoundTool{BindingID: "11111111-1111-4111-8111-111111111111", TargetID: "22222222-2222-4222-8222-222222222222", Digest: strings.Repeat("a", 64), Name: "mcp_22222222222242228222222222222222", InputSchema: map[string]any{"type": "object"}}
			failed, ready := 0, 0
			run := RunContext{RunID: "33333333-3333-4333-8333-333333333333", Authority: &openlinker.RuntimeAuthorityContext{PrincipalScopeID: "fixture"}, MCPSnapshot: agentdelegation.BoundSnapshot{Schema: 1, Tools: []agentdelegation.BoundTool{tool}},
				CallAgent: func(context.Context, string, any, openlinker.RuntimeCallOptions) (any, error) {
					t.Fatal("unexpected call")
					return nil, nil
				},
				ReadDelegatedRun: func(context.Context, string) (*openlinker.RuntimeDelegatedRun, error) {
					t.Fatal("unexpected read")
					return nil, nil
				},
				Emit: func(kind string, payload any) error {
					if kind == "run.mcp_bindings.failed" {
						failed++
						if payload.(map[string]any)["stage"] != "provider_ended_before_discovery" {
							t.Fatal("missing observed stage")
						}
					}
					if kind == "run.mcp_bindings.ready" {
						ready++
					}
					return nil
				},
			}
			_, err = p.Run(context.Background(), run)
			if !errors.Is(err, cause) {
				t.Fatalf("provider cause was masked: %v", err)
			}
			if discovered && (failed != 0 || ready != 1) || !discovered && (failed != 1 || ready != 0) {
				t.Fatalf("failed=%d ready=%d", failed, ready)
			}
		})
	}
}
