package agentnode

import (
	"context"
	"sync"
	"time"

	agentexec "github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/sessionsandbox"
)

// CodexAdapter preserves the Agent Node configuration surface. Native process,
// prompt, progress, session security, and recovery are owned only by agentexec.
// Legacy Node session maps are not imported; Core history seeds a new session.
type CodexAdapter struct {
	SessionIsolation     sessionsandbox.Config
	CodexBin             string
	Workspace            string
	Sandbox              string
	Approval             string
	Model                string
	Timeout              time.Duration
	MockResponse         string
	SessionReuse         bool
	SessionStore         string
	Env                  []string
	EnvAllowlist         []string
	DelegationTargets    []string
	DelegationProxyBin   string
	DelegationBrokerRoot string
	once                 sync.Once
	adapter              *NativeAdapter
}

func (a *CodexAdapter) native() *NativeAdapter {
	a.once.Do(func() {
		a.adapter = &NativeAdapter{Config: agentexec.ProviderConfig{
			SessionIsolation: a.SessionIsolation,
			Provider:         "codex", Bin: a.CodexBin, Workspace: a.Workspace,
			Sandbox: a.Sandbox, CodexApproval: a.Approval, Model: a.Model,
			Timeout: a.Timeout, SessionReuse: a.SessionReuse, SessionStore: a.SessionStore,
			Env: a.Env, EnvAllowlist: a.EnvAllowlist,
			DelegationTargets: a.DelegationTargets, DelegationProxyBin: a.DelegationProxyBin,
			DelegationBrokerRoot: a.DelegationBrokerRoot,
		}}
	})
	return a.adapter
}

func (a *CodexAdapter) Preflight(ctx context.Context) error {
	if a.MockResponse != "" {
		return nil
	}
	return a.native().Preflight(ctx)
}

func (a *CodexAdapter) RuntimeFeatures() []string { return a.native().RuntimeFeatures() }

func (a *CodexAdapter) Run(ctx context.Context, input any, run RunContext) (any, error) {
	if a.MockResponse != "" {
		if run.Emit != nil {
			run.Emit("run.message.delta", JSONMap{"text": "Codex is processing the task."})
		}
		return AdapterResult{Status: "success", Output: JSONMap{"handled_by": "codex", "mocked": true, "summary": a.MockResponse},
			Events: []RunEvent{{EventType: "run.message.delta", Payload: JSONMap{"text": a.MockResponse}}}}, nil
	}
	return a.native().Run(ctx, input, run)
}
