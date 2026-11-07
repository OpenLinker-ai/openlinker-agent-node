package agentnode

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestConfigurationReportWarnsAboutMissingAdapterTarget(t *testing.T) {
	for _, scenario := range []struct {
		name string
		env  Env
		want string
	}{
		{name: "inferred http", env: Env{}, want: "http_adapter_url_missing"},
		{name: "explicit http", env: Env{"OPENLINKER_AGENT_NODE_ADAPTER": "http"}, want: "http_adapter_url_missing"},
		{name: "openclaw", env: Env{"OPENLINKER_AGENT_NODE_ADAPTER": "openclaw"}, want: "http_adapter_url_missing"},
		{name: "command", env: Env{"OPENLINKER_AGENT_NODE_ADAPTER": "command"}, want: "command_adapter_command_missing"},
		{name: "a2a", env: Env{"OPENLINKER_AGENT_NODE_ADAPTER": "a2a"}, want: "a2a_adapter_base_url_missing"},
		{name: "a2a blank", env: Env{"OPENLINKER_AGENT_NODE_ADAPTER": "a2a", "OPENLINKER_AGENT_NODE_A2A_BASE_URL": "   "}, want: "a2a_adapter_base_url_missing"},
		{name: "http configured", env: Env{"OPENLINKER_AGENT_NODE_HTTP_URL": "http://127.0.0.1:9/private-endpoint"}},
		{name: "command configured", env: Env{"OPENLINKER_AGENT_NODE_ADAPTER": "command", "OPENLINKER_AGENT_NODE_COMMAND": "private-command"}},
		{name: "a2a configured", env: Env{"OPENLINKER_AGENT_NODE_A2A_BASE_URL": "http://127.0.0.1:9/private-a2a"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			node, err := NewFromEnvMap(scenario.env)
			if err != nil {
				t.Fatalf("configuration that loads today must keep loading: %v", err)
			}
			report := node.ConfigurationReport()
			if scenario.want == "" {
				if len(report.Warnings) != 0 || report.Native != nil {
					t.Fatalf("valid adapter reported warnings: %#v", report)
				}
			} else if len(report.Warnings) != 1 || report.Warnings[0] != scenario.want {
				t.Fatalf("warnings = %v, want [%s]", report.Warnings, scenario.want)
			}
			raw, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			for _, private := range []string{"private-endpoint", "private-command", "private-a2a", "127.0.0.1"} {
				if strings.Contains(string(raw), private) {
					t.Fatalf("configuration report disclosed a configured value: %s", raw)
				}
			}
		})
	}
}

func TestAdapterConfigurationWarningsMatchRunChecks(t *testing.T) {
	for _, scenario := range []struct {
		adapter Adapter
		want    string
	}{
		{adapter: HTTPAdapter{}, want: "http_adapter_url_missing"},
		{adapter: &HTTPAdapter{}, want: "http_adapter_url_missing"},
		{adapter: CommandAdapter{}, want: "command_adapter_command_missing"},
		{adapter: &CommandAdapter{}, want: "command_adapter_command_missing"},
		{adapter: A2AAdapter{BaseURL: " "}, want: "a2a_adapter_base_url_missing"},
		{adapter: &A2AAdapter{}, want: "a2a_adapter_base_url_missing"},
		{adapter: HTTPAdapter{URL: "http://127.0.0.1:9"}},
		{adapter: &CommandAdapter{Command: "true"}},
		{adapter: A2AAdapter{BaseURL: "http://127.0.0.1:9"}},
		{adapter: (*HTTPAdapter)(nil)},
		{adapter: &CodexAdapter{}},
	} {
		warnings := adapterConfigurationWarnings(scenario.adapter)
		if scenario.want == "" {
			if len(warnings) != 0 {
				t.Fatalf("%T reported %v", scenario.adapter, warnings)
			}
			continue
		}
		if len(warnings) != 1 || warnings[0].code != scenario.want {
			t.Fatalf("%T warnings = %v, want %s", scenario.adapter, warnings, scenario.want)
		}
		// The warning must agree with execution: each adapter's Run fails with
		// the same missing setting before doing any work.
		_, err := scenario.adapter.Run(t.Context(), map[string]any{}, RunContext{})
		if err == nil || !strings.Contains(err.Error(), warnings[0].variable) {
			t.Fatalf("%T Run error %v does not match warning %s", scenario.adapter, err, warnings[0].variable)
		}
	}
}

func TestStartupLogsAdapterWarningAndStillStarts(t *testing.T) {
	core := newShutdownTestCore(t)
	output := &shutdownTestLog{}
	node := newShutdownTestNode(t, core, HTTPAdapter{}, output)
	signals := make(chan os.Signal, 2)
	served := make(chan error, 1)
	go func() { served <- node.Serve(signals) }()

	eventuallyForTest(t, 10*time.Second, func() bool {
		return core.snapshot().hello.RuntimeSessionID != ""
	}, "Worker attached despite the adapter warning")
	logs := output.String()
	if !strings.Contains(logs, "configuration warning (http_adapter_url_missing)") ||
		!strings.Contains(logs, "OPENLINKER_AGENT_NODE_HTTP_URL is not set") {
		t.Fatalf("startup did not log the adapter warning:\n%s", logs)
	}
	if strings.Contains(logs, "synthetic-shutdown-test-token") {
		t.Fatal("startup warning leaked a credential")
	}
	signals <- os.Interrupt
	if err := waitForServe(t, served, ProcessShutdownTimeout); err != nil {
		t.Fatalf("Node with a warning did not stop cleanly: %v", err)
	}
	assertShutdownTestStoreClosed(t, node)
}
