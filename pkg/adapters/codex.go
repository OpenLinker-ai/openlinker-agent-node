package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type CodexProvider struct{ Config ProviderConfig }

func (provider CodexProvider) Run(ctx context.Context, run RunContext) (openlinker.RuntimeResult, error) {
	if run.Emit != nil {
		_ = run.Emit("run.message.delta", map[string]any{"text": "Codex is processing the task."})
	}
	config := provider.Config
	config = providerConfigForDelegationRun(config, run)
	bin := strings.TrimSpace(config.Bin)
	if bin == "" {
		bin = "codex"
	}
	workspace := strings.TrimSpace(config.Workspace)
	if workspace == "" {
		workspace, _ = os.Getwd()
	}
	sandbox := strings.TrimSpace(config.Sandbox)
	if sandbox == "" {
		sandbox = "read-only"
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sessionKey := conversationSessionKey(run)
	sessionPath := sessionStorePath(config.SessionStore, "codex", workspace)
	sessionID := ""
	clientMode := "codex_rpc_v1:" + providerSessionClientMode(config)
	clientModeGeneration := uint64(1)
	if config.SessionReuse && sessionKey != "" {
		unlock := lockSession("codex", workspace, sessionKey)
		defer unlock()
		sessionID, clientModeGeneration, _ = loadSessionForClientMode(
			sessionPath,
			"codex",
			workspace,
			sessionKey,
			clientMode,
		)
	}
	resumed := sessionID != ""
	recovered := false
	var observed, summary string
	for attempt := 0; attempt < 2; attempt++ {
		var err error
		observed, summary, err = runCodexRPC(requestCtx, bin, workspace, sandbox, sessionID,
			buildCodexPrompt(runWithSessionHistory(run, sessionPath, "codex", workspace, sessionKey, sessionID), config.WebSearch),
			config.SessionReuse && sessionKey != "", config, run.Emit)
		if requestCtx.Err() != nil {
			if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
				return openlinker.RuntimeResult{}, fmt.Errorf("Codex timed out after %s", timeout)
			}
			return openlinker.RuntimeResult{}, requestCtx.Err()
		}
		if err == nil {
			break
		}
		if sessionID != "" && attempt == 0 && errors.Is(err, errCodexSessionMissing) {
			if err := deleteSessionID(sessionPath, "codex", workspace, sessionKey); err != nil {
				return openlinker.RuntimeResult{}, err
			}
			sessionID = ""
			recovered = true
			continue
		}
		return openlinker.RuntimeResult{}, fmt.Errorf("Codex failed: %w", err)
	}
	if config.SessionReuse && sessionKey != "" {
		if err := saveSessionForClientMode(sessionPath, "codex", workspace, sessionKey, observed, clientMode, clientModeGeneration, run); err != nil {
			return openlinker.RuntimeResult{}, sessionPersistenceError("Codex", err)
		}
	}
	result := map[string]any{
		"handled_by": "codex", "codex_sandbox": sandbox,
		"codex_model": modelLabel(config.Model), "summary": summary,
	}
	if config.SessionReuse && sessionKey != "" {
		result["codex_session_reuse"] = true
		result["codex_session_key_hash"] = sessionKeyHash("codex", workspace, sessionKey)
		result["codex_session_resumed"] = resumed
		result["codex_session_recovered"] = recovered
	}
	return openlinker.RuntimeResult{
		Status: "success", Output: result,
		Events: []openlinker.RuntimeEvent{{EventType: "run.message.delta", Payload: map[string]any{"text": summary}}},
	}, nil
}

func codexLaunchConfiguration(config ProviderConfig, sandbox string) []string {
	args := []string{}
	args = append(args, codexDelegationMCPArguments(config)...)
	if value := strings.TrimSpace(config.CodexApproval); value != "" {
		args = append(args, "-c", fmt.Sprintf("approval_policy=%q", value))
	}
	if config.WebSearch {
		args = append(args, "-c", `web_search="live"`)
	} else {
		args = append(args, "-c", `web_search="disabled"`)
	}
	if value := strings.TrimSpace(config.CodexBaseURL); value != "" {
		// OpenAI-compatible routers commonly implement the HTTP Responses API
		// without the optional Responses WebSocket transport. Keep the built-in
		// OpenAI provider untouched and describe the router as a native custom
		// provider so Codex does not attempt an unsupported WebSocket upgrade.
		args = append(args,
			"-c", `model_provider="openlinker_proxy"`,
			"-c", `model_providers.openlinker_proxy.name="OpenLinker-compatible provider"`,
			"-c", fmt.Sprintf("model_providers.openlinker_proxy.base_url=%q", value),
			"-c", `model_providers.openlinker_proxy.env_key="CODEX_API_KEY"`,
			"-c", `model_providers.openlinker_proxy.wire_api="responses"`,
			"-c", `model_providers.openlinker_proxy.supports_websockets=false`,
		)
	}
	if sandbox == "danger-full-access" {
		// This mode is intended for an external isolation boundary such as the
		// official hardened Provider container. Keep model-spawned commands from
		// inheriting provider credentials even though the Codex process itself
		// needs them to call the configured model endpoint.
		// Leave Code Mode selection to the pinned Codex defaults/model catalog
		// and keep its host available for models that require it for MCP calls.
		// Isolation relies on the external container boundary and tool-specific
		// authorization, not on assumptions about Codex's V8 implementation.
		// The live test's selected host-API checks are regression sentinels,
		// not proof of a sandbox boundary.
		args = append(args,
			"-c", `shell_environment_policy.inherit="none"`,
			"-c", "shell_environment_policy.set="+codexCommandEnvironment(config.Env),
		)
	}
	return args
}

func codexCommandEnvironment(environment []string) string {
	if environment == nil {
		environment = os.Environ()
	}
	values := make(map[string]string, len(environment))
	for _, item := range environment {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}
	safeKeys := []string{
		"PATH", "Path", "PATHEXT",
		"HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH",
		"SYSTEMROOT", "SystemRoot", "WINDIR", "COMSPEC",
		"TMPDIR", "TEMP", "TMP", "LANG",
		"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy",
		"NO_PROXY", "no_proxy", "SSL_CERT_FILE",
	}
	pairs := make([]string, 0, len(safeKeys))
	for _, key := range safeKeys {
		value, ok := values[key]
		if !ok {
			continue
		}
		encoded, _ := json.Marshal(value)
		pairs = append(pairs, key+"="+string(encoded))
	}
	return "{" + strings.Join(pairs, ",") + "}"
}

func modelLabel(model string) string {
	if strings.TrimSpace(model) == "" {
		return "default"
	}
	return strings.TrimSpace(model)
}
