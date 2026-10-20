package agentnode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/agentdelegation"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providertest"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func TestMCPProviderFixtureProcess(t *testing.T) { providertest.MCPFixtureProcess() }
func TestDefaultIsolatedAdaptersUseSelectedMCPTools(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			env := defaultIsolationEnv(t, provider)
			binary := filepath.Join(t.TempDir(), provider)
			providertest.WriteMCPFixture(t, binary, provider)
			env["OPENLINKER_AGENT_NODE_"+strings.ToUpper(provider)+"_BIN"] = binary
			node, err := NewFromEnvMap(env)
			if err != nil {
				t.Fatal(err)
			}
			processEnv := []string{"HOME=" + env["HOME"], "PATH=" + os.Getenv("PATH")}
			switch a := node.Adapter.(type) {
			case *CodexAdapter:
				a.Env = processEnv
				a.DelegationProxyBin = binary
			case *NativeAdapter:
				a.Config.Env = processEnv
				a.Config.DelegationProxyBin = binary
			default:
				t.Fatalf("unexpected adapter %T", node.Adapter)
			}
			tool := agentdelegation.BoundTool{BindingID: "11111111-1111-4111-8111-111111111111", TargetID: "22222222-2222-4222-8222-222222222222", Digest: strings.Repeat("a", 64), Name: "mcp_22222222222242228222222222222222", Title: "Lookup", InputSchema: map[string]any{"type": "object"}}
			calls, ready := 0, 0
			ids := []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa2", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa3", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa4"}
			for i, id := range ids {
				if i == 2 {
					tool.BindingID = "33333333-3333-4333-8333-333333333333"
				}
				var snapshot any = agentdelegation.BoundSnapshot{Schema: 1, Tools: []agentdelegation.BoundTool{tool}}
				if i == 3 {
					snapshot = nil
				}
				run := RunContext{RunID: id, AgentID: "55555555-5555-4555-8555-555555555555", Authority: &openlinker.RuntimeAuthorityContext{PrincipalScopeID: "caller"}, Conversation: &ConversationContext{Source: "core", SessionKey: "same-conversation", CurrentRunID: id}, MCPSnapshot: snapshot,
					emitChecked: func(kind string, _ any) error {
						if kind == "run.mcp_bindings.ready" {
							ready++
						}
						return nil
					},
					CallAgent: func(_ context.Context, target string, input any, options CallAgentOptions) (any, error) {
						calls++
						if target != tool.TargetID || input.(map[string]any)["text"] != "lookup" || options.Metadata == nil {
							t.Fatal("incorrect MCP callback")
						}
						return openlinker.RuntimeRunSummary{RunID: "66666666-6666-4666-8666-666666666666", Status: openlinker.RuntimeRunRunning}, nil
					},
					ReadDelegatedRun: func(context.Context, string) (*openlinker.RuntimeDelegatedRun, error) {
						return &openlinker.RuntimeDelegatedRun{RuntimeRunSummary: openlinker.RuntimeRunSummary{RunID: "66666666-6666-4666-8666-666666666666", Status: openlinker.RuntimeRunSuccess}, Output: map[string]any{"marker": "TOOL-MARKER"}}, nil
					},
				}
				result, err := node.Adapter.Run(context.Background(), map[string]any{"text": "use selected tool"}, run)
				if err != nil {
					t.Fatal(err)
				}
				output := normalizeAdapterResult(result).Output.(JSONMap)
				if output[provider+"_session_resumed"] != (i == 1) {
					t.Fatalf("Run %d session was not renewed for changed selection: %#v", i, output)
				}
				if output["session_isolation"] != "native" {
					t.Fatal("default isolation disabled")
				}
			}
			if calls != 3 || ready != 3 {
				t.Fatalf("calls=%d ready=%d", calls, ready)
			}
		})
	}
}
