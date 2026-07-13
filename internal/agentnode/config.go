package agentnode

import (
	"fmt"
	"os"
	"strings"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type Env map[string]string
type EnvLookup func(string) string

func NewFromEnv() (*Node, error) {
	return NewFromLookup(os.Getenv)
}

func NewFromEnvMap(env Env) (*Node, error) {
	return NewFromLookup(func(key string) string {
		return env[key]
	})
}

func NewFromLookup(get EnvLookup) (*Node, error) {
	adapterMode := get("OPENLINKER_AGENT_NODE_ADAPTER")
	if adapterMode == "" {
		adapterMode = inferAdapterMode(get)
	}
	adapter, err := adapterFromEnv(get, adapterMode)
	if err != nil {
		return nil, err
	}
	helper, err := helperFromEnv(get, adapterMode)
	if err != nil {
		return nil, err
	}
	publicA2A, err := publicA2AFromEnv(get, adapter)
	if err != nil {
		return nil, err
	}
	capacity, err := numberOption(get("OPENLINKER_AGENT_NODE_CAPACITY"), int(openlinker.RuntimeWorkerDefaultCapacity), "OPENLINKER_AGENT_NODE_CAPACITY")
	if err != nil {
		return nil, err
	}
	claimWait, err := numberOption(get("OPENLINKER_AGENT_NODE_CLAIM_WAIT_SECONDS"), int(openlinker.RuntimeWorkerDefaultClaimWait/time.Second), "OPENLINKER_AGENT_NODE_CLAIM_WAIT_SECONDS")
	if err != nil {
		return nil, err
	}
	commandWait, err := numberOption(get("OPENLINKER_AGENT_NODE_COMMAND_WAIT_SECONDS"), int(openlinker.RuntimeWorkerDefaultCommandWait/time.Second), "OPENLINKER_AGENT_NODE_COMMAND_WAIT_SECONDS")
	if err != nil {
		return nil, err
	}
	heartbeat, err := numberOption(get("OPENLINKER_AGENT_NODE_HEARTBEAT_SECONDS"), int(openlinker.RuntimeWorkerDefaultHeartbeatInterval/time.Second), "OPENLINKER_AGENT_NODE_HEARTBEAT_SECONDS")
	if err != nil {
		return nil, err
	}
	retryMinMS, err := numberOption(get("OPENLINKER_AGENT_NODE_RETRY_MIN_MS"), int(openlinker.RuntimeWorkerDefaultRetryMinimum/time.Millisecond), "OPENLINKER_AGENT_NODE_RETRY_MIN_MS")
	if err != nil {
		return nil, err
	}
	retryMaxMS, err := numberOption(get("OPENLINKER_AGENT_NODE_RETRY_MAX_MS"), int(openlinker.RuntimeWorkerDefaultRetryMaximum/time.Millisecond), "OPENLINKER_AGENT_NODE_RETRY_MAX_MS")
	if err != nil {
		return nil, err
	}
	return &Node{
		OpenLinkerURL:     strings.TrimSpace(get("OPENLINKER_URL")),
		RuntimeURL:        strings.TrimSpace(get("OPENLINKER_RUNTIME_URL")),
		Transport:         strings.ToLower(strings.TrimSpace(defaultString(get("OPENLINKER_AGENT_NODE_TRANSPORT"), string(openlinker.RuntimeTransportAuto)))),
		NodeID:            strings.TrimSpace(get("OPENLINKER_NODE_ID")),
		AgentID:           strings.TrimSpace(get("OPENLINKER_AGENT_ID")),
		AgentToken:        strings.TrimSpace(get("OPENLINKER_AGENT_TOKEN")),
		DataDir:           strings.TrimSpace(get("OPENLINKER_AGENT_NODE_DATA_DIR")),
		MTLSCertFile:      strings.TrimSpace(get("OPENLINKER_AGENT_NODE_MTLS_CERT_FILE")),
		MTLSKeyFile:       strings.TrimSpace(get("OPENLINKER_AGENT_NODE_MTLS_KEY_FILE")),
		MTLSCAFile:        strings.TrimSpace(get("OPENLINKER_AGENT_NODE_MTLS_CA_FILE")),
		MTLSServerName:    strings.TrimSpace(get("OPENLINKER_AGENT_NODE_MTLS_SERVER_NAME")),
		Capacity:          int64(capacity),
		ClaimWait:         time.Duration(claimWait) * time.Second,
		CommandWait:       time.Duration(commandWait) * time.Second,
		HeartbeatInterval: time.Duration(heartbeat) * time.Second,
		RetryMinimum:      time.Duration(retryMinMS) * time.Millisecond,
		RetryMaximum:      time.Duration(retryMaxMS) * time.Millisecond,
		Adapter:           adapter,
		Helper:            helper,
		PublicA2A:         publicA2A,
	}, nil
}

func adapterFromEnv(get EnvLookup, mode string) (Adapter, error) {
	timeout, err := numberOption(get("OPENLINKER_AGENT_NODE_TIMEOUT_MS"), 15*60_000, "OPENLINKER_AGENT_NODE_TIMEOUT_MS")
	if err != nil {
		return nil, err
	}
	envAllowlist := parseCommaList(get("OPENLINKER_AGENT_NODE_ENV_ALLOWLIST"))
	switch mode {
	case "http", "openclaw":
		headers, err := parseJSONMap(get("OPENLINKER_AGENT_NODE_HTTP_HEADERS"), "OPENLINKER_AGENT_NODE_HTTP_HEADERS")
		if err != nil {
			return nil, err
		}
		return HTTPAdapter{
			URL:     get("OPENLINKER_AGENT_NODE_HTTP_URL"),
			Headers: headers,
			Timeout: time.Duration(timeout) * time.Millisecond,
		}, nil
	case "a2a":
		headers, err := parseJSONMap(get("OPENLINKER_AGENT_NODE_A2A_HEADERS"), "OPENLINKER_AGENT_NODE_A2A_HEADERS")
		if err != nil {
			return nil, err
		}
		modes, err := parseJSONStringArray(get("OPENLINKER_AGENT_NODE_A2A_ACCEPTED_OUTPUT_MODES"), "OPENLINKER_AGENT_NODE_A2A_ACCEPTED_OUTPUT_MODES")
		if err != nil {
			return nil, err
		}
		return A2AAdapter{
			BaseURL:             get("OPENLINKER_AGENT_NODE_A2A_BASE_URL"),
			Token:               get("OPENLINKER_UPSTREAM_A2A_TOKEN"),
			Headers:             headers,
			Method:              openlinker.NormalizeA2AJSONRPCMethodForDialect(defaultString(get("OPENLINKER_AGENT_NODE_A2A_METHOD"), openlinker.A2AMethodMessageSend), defaultString(get("OPENLINKER_AGENT_NODE_A2A_DIALECT"), get("OPENLINKER_AGENT_NODE_A2A_METHOD_DIALECT"))),
			AcceptedOutputModes: modes,
			ProtocolVersion:     get("OPENLINKER_AGENT_NODE_A2A_PROTOCOL_VERSION"),
			Dialect:             openlinker.NormalizeA2ADialect(defaultString(get("OPENLINKER_AGENT_NODE_A2A_DIALECT"), get("OPENLINKER_AGENT_NODE_A2A_METHOD_DIALECT"))),
			Timeout:             time.Duration(timeout) * time.Millisecond,
		}, nil
	case "command":
		args, err := parseJSONStringArray(get("OPENLINKER_AGENT_NODE_ARGS"), "OPENLINKER_AGENT_NODE_ARGS")
		if err != nil {
			return nil, err
		}
		cwd := get("OPENLINKER_AGENT_NODE_CWD")
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		return CommandAdapter{
			Command:      get("OPENLINKER_AGENT_NODE_COMMAND"),
			Args:         args,
			CWD:          cwd,
			EnvAllowlist: envAllowlist,
			Timeout:      time.Duration(timeout) * time.Millisecond,
		}, nil
	case "codex":
		codexTimeout, err := numberOption(get("OPENLINKER_AGENT_NODE_TIMEOUT_MS"), 30*60_000, "OPENLINKER_AGENT_NODE_TIMEOUT_MS")
		if err != nil {
			return nil, err
		}
		return CodexAdapter{
			CodexBin:     defaultString(get("OPENLINKER_AGENT_NODE_CODEX_BIN"), "codex"),
			Workspace:    defaultString(get("OPENLINKER_AGENT_NODE_CODEX_WORKSPACE"), mustGetwd()),
			Sandbox:      defaultString(get("OPENLINKER_AGENT_NODE_CODEX_SANDBOX"), "read-only"),
			Approval:     defaultString(get("OPENLINKER_AGENT_NODE_CODEX_APPROVAL"), "never"),
			Model:        get("OPENLINKER_AGENT_NODE_CODEX_MODEL"),
			Timeout:      time.Duration(codexTimeout) * time.Millisecond,
			MockResponse: get("OPENLINKER_AGENT_NODE_CODEX_MOCK_RESPONSE"),
			SessionReuse: boolOption(get("OPENLINKER_AGENT_NODE_CODEX_SESSION_REUSE"), false),
			SessionStore: get("OPENLINKER_AGENT_NODE_CODEX_SESSION_STORE"),
			EnvAllowlist: envAllowlist,
		}, nil
	case "module":
		return nil, fmt.Errorf("module adapter is not supported by the Go agent node; use http, command, openclaw, or codex")
	default:
		return nil, fmt.Errorf("unsupported OPENLINKER_AGENT_NODE_ADAPTER=%s", mode)
	}
}

func helperFromEnv(get EnvLookup, adapterMode string) (*LocalHelperServer, error) {
	mode := strings.ToLower(defaultString(get("OPENLINKER_AGENT_NODE_HELPER"), "auto"))
	enabled := false
	switch mode {
	case "auto":
		enabled = adapterMode == "http" || adapterMode == "openclaw" || adapterMode == "command" || adapterMode == "codex"
	case "1", "true", "yes", "on":
		enabled = true
	case "0", "false", "no", "off":
		enabled = false
	default:
		return nil, fmt.Errorf("invalid OPENLINKER_AGENT_NODE_HELPER=%s; use auto, true, or false", mode)
	}
	if !enabled {
		return nil, nil
	}
	port, err := numberOption(get("OPENLINKER_AGENT_NODE_HELPER_PORT"), 0, "OPENLINKER_AGENT_NODE_HELPER_PORT")
	if err != nil {
		return nil, err
	}
	return &LocalHelperServer{
		Host: defaultString(get("OPENLINKER_AGENT_NODE_HELPER_HOST"), "127.0.0.1"),
		Port: port,
	}, nil
}

func publicA2AFromEnv(get EnvLookup, adapter Adapter) (*PublicA2AServer, error) {
	if !boolOption(get("OPENLINKER_AGENT_NODE_PUBLIC_A2A"), false) {
		return nil, nil
	}
	port, err := numberOption(get("OPENLINKER_AGENT_NODE_PUBLIC_A2A_PORT"), 0, "OPENLINKER_AGENT_NODE_PUBLIC_A2A_PORT")
	if err != nil {
		return nil, err
	}
	return &PublicA2AServer{
		Host:        defaultString(get("OPENLINKER_AGENT_NODE_PUBLIC_A2A_HOST"), "127.0.0.1"),
		Port:        port,
		Slug:        defaultString(get("OPENLINKER_AGENT_NODE_PUBLIC_A2A_SLUG"), "agent-node"),
		Name:        defaultString(get("OPENLINKER_AGENT_NODE_PUBLIC_A2A_NAME"), "OpenLinker Agent Node"),
		Description: get("OPENLINKER_AGENT_NODE_PUBLIC_A2A_DESCRIPTION"),
		Token:       get("OPENLINKER_PUBLIC_A2A_TOKEN"),
		Adapter:     adapter,
		AllowLocalPushURLs: boolOption(
			get("OPENLINKER_AGENT_NODE_PUBLIC_A2A_ALLOW_LOCAL_PUSH_URLS"),
			false,
		),
	}, nil
}

func inferAdapterMode(get EnvLookup) string {
	if get("OPENLINKER_AGENT_NODE_HTTP_URL") != "" {
		return "http"
	}
	if get("OPENLINKER_AGENT_NODE_A2A_BASE_URL") != "" {
		return "a2a"
	}
	if get("OPENLINKER_AGENT_NODE_COMMAND") != "" {
		return "command"
	}
	if get("OPENLINKER_AGENT_NODE_CODEX_WORKSPACE") != "" || get("OPENLINKER_AGENT_NODE_CODEX_BIN") != "" {
		return "codex"
	}
	return "http"
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func parseCommaList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}
