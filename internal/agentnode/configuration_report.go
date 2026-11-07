package agentnode

import (
	"encoding/json"
	"strings"

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
	for _, warning := range adapterConfigurationWarnings(node.Adapter) {
		report.Warnings = append(report.Warnings, warning.code)
	}
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

// adapterConfigurationWarning names an adapter setting that loads successfully
// but makes every Run fail. Codes are stable and allowlisted; messages name
// only the environment variable, never its value.
type adapterConfigurationWarning struct {
	code     string
	variable string
}

// adapterConfigurationWarnings mirrors the exact "is required" checks that each
// adapter's Run performs, so a warning never disagrees with execution.
func adapterConfigurationWarnings(adapter Adapter) []adapterConfigurationWarning {
	missingHTTP := adapterConfigurationWarning{code: "http_adapter_url_missing", variable: "OPENLINKER_AGENT_NODE_HTTP_URL"}
	missingCommand := adapterConfigurationWarning{code: "command_adapter_command_missing", variable: "OPENLINKER_AGENT_NODE_COMMAND"}
	missingA2A := adapterConfigurationWarning{code: "a2a_adapter_base_url_missing", variable: "OPENLINKER_AGENT_NODE_A2A_BASE_URL"}
	switch value := adapter.(type) {
	case HTTPAdapter:
		if value.URL == "" {
			return []adapterConfigurationWarning{missingHTTP}
		}
	case *HTTPAdapter:
		if value != nil && value.URL == "" {
			return []adapterConfigurationWarning{missingHTTP}
		}
	case CommandAdapter:
		if value.Command == "" {
			return []adapterConfigurationWarning{missingCommand}
		}
	case *CommandAdapter:
		if value != nil && value.Command == "" {
			return []adapterConfigurationWarning{missingCommand}
		}
	case A2AAdapter:
		if strings.TrimSpace(value.BaseURL) == "" {
			return []adapterConfigurationWarning{missingA2A}
		}
	case *A2AAdapter:
		if value != nil && strings.TrimSpace(value.BaseURL) == "" {
			return []adapterConfigurationWarning{missingA2A}
		}
	}
	return nil
}

// logAdapterConfigurationWarnings reports a missing required adapter setting at
// startup. It is diagnostic only: startup continues exactly as before.
func (node *Node) logAdapterConfigurationWarnings() {
	if node.Logger == nil {
		return
	}
	for _, warning := range adapterConfigurationWarnings(node.Adapter) {
		node.Logger.Printf(
			"openlinker agent node configuration warning (%s): %s is not set; every Run will fail until it is configured",
			warning.code, warning.variable,
		)
	}
}
