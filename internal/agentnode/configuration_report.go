package agentnode

import (
	"encoding/json"

	agentexec "github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters"
)

// ConfigurationReport is an allowlisted policy summary. It deliberately omits
// credentials, endpoints, identities, paths, model names and arbitrary env values.
// Loading configuration does not run provider/OS preflight or verify enforcement.
type ConfigurationReport struct {
	Version           string              `json:"version"`
	Scope             string              `json:"scope"`
	ProviderPreflight string              `json:"provider_preflight"`
	Capacity          int64               `json:"capacity"`
	Native            *NativePolicyReport `json:"native,omitempty"`
	Warnings          []string            `json:"warnings,omitempty"`
}

type NativePolicyReport struct {
	Provider            string `json:"provider"`
	WebSearch           bool   `json:"web_search"`
	SessionIsolation    string `json:"session_isolation"`
	SessionReuse        bool   `json:"session_reuse"`
	HostAuthConcurrency string `json:"host_auth_concurrency,omitempty"`
	Mock                bool   `json:"mock"`
}

func (node *Node) ConfigurationReport() ConfigurationReport {
	report := ConfigurationReport{Version: AgentNodeVersion, Scope: "configured_policy", ProviderPreflight: "not_run", Capacity: node.Capacity}
	var config agentexec.ProviderConfig
	mock := false
	switch adapter := node.Adapter.(type) {
	case *CodexAdapter:
		config, mock = adapter.native().Config, adapter.MockResponse != ""
	case *NativeAdapter:
		config = adapter.Config
	default:
		return report
	}
	if config.Provider != "codex" && config.Provider != "claude" {
		return report
	}
	policy := &NativePolicyReport{Provider: config.Provider, WebSearch: config.WebSearch, SessionIsolation: "off", SessionReuse: config.SessionReuse, Mock: mock}
	if config.SessionIsolation.Enabled() {
		policy.SessionIsolation = "native"
		policy.HostAuthConcurrency = defaultString(config.HostAuthConcurrency, "serial")
		if policy.HostAuthConcurrency == "serial" && node.Capacity > 1 {
			report.Warnings = append(report.Warnings, "serial_host_auth_capacity_gt_one")
		}
	} else {
		report.Warnings = append(report.Warnings, "native_isolation_disabled")
	}
	if mock {
		report.Warnings = append(report.Warnings, "mock_provider_enabled")
	}
	report.Native = policy
	return report
}

func (node *Node) logConfigurationReport() {
	if node.Logger == nil {
		return
	}
	report := node.ConfigurationReport()
	if report.Native == nil {
		return
	}
	// This runs before Preflight: no log field claims that a policy was enforced.
	raw, err := json.Marshal(report)
	if err == nil {
		node.Logger.Printf("native configuration: %s", raw)
	}
}
