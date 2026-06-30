package agentnode

import (
	"context"
	"testing"
)

func TestProcessAssignmentRecoversAdapterPanic(t *testing.T) {
	connector := &recordingConnector{}
	node := &Node{
		APIBase:      "https://api.example",
		RuntimeToken: "ol_live_test",
		Connector:    connector,
		Adapter: AdapterFunc(func(context.Context, any, RunContext) (any, error) {
			panic("adapter exploded")
		}),
		ctx: context.Background(),
	}

	node.processAssignment(Assignment{RunID: "run-1", AgentID: "agent-1", Input: JSONMap{"q": "boom"}})

	if connector.completedRunID != "run-1" {
		t.Fatalf("completed run id = %q", connector.completedRunID)
	}
	if connector.result.Status != "failed" || connector.result.Error == nil || connector.result.Error.Code != "ADAPTER_PANIC" {
		t.Fatalf("panic result = %#v", connector.result)
	}
}

type recordingConnector struct {
	completedRunID string
	result         RunResult
}

func (r *recordingConnector) Start(context.Context, ConnectorHandlers) error { return nil }
func (r *recordingConnector) Stop(context.Context) error                     { return nil }
func (r *recordingConnector) SupportsLiveEvents() bool                       { return false }
func (r *recordingConnector) SendRunEvent(context.Context, string, RunEvent) error {
	return nil
}
func (r *recordingConnector) CompleteRun(_ context.Context, runID string, result RunResult) error {
	r.completedRunID = runID
	r.result = result
	return nil
}
