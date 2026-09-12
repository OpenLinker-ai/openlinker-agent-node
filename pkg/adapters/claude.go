package adapters

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerstream"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type ClaudeProvider struct{ Config ProviderConfig }

type claudeResponse struct {
	Type      string   `json:"type"`
	Subtype   string   `json:"subtype"`
	IsError   bool     `json:"is_error"`
	Result    string   `json:"result"`
	Errors    []string `json:"errors"`
	SessionID string   `json:"session_id"`
}

func (provider ClaudeProvider) Run(ctx context.Context, run RunContext) (resultValue openlinker.RuntimeResult, resultErr error) {
	if run.Emit != nil {
		_ = run.Emit("run.message.delta", map[string]any{"text": "Claude Code is processing the task."})
	}
	config := provider.Config
	config = providerConfigForDelegationRun(config, run)
	if config.SessionIsolation.Enabled() {
		config.Provider = "claude"
	}
	config, closeSandbox, isolationErr := prepareIsolatedSession(ctx, config, run)
	if isolationErr != nil {
		return openlinker.RuntimeResult{}, isolationErr
	}
	defer func() { resultErr = errors.Join(resultErr, closeSandbox()) }()
	bin := strings.TrimSpace(config.Bin)
	if bin == "" {
		bin = "claude"
	}
	workspace := strings.TrimSpace(config.Workspace)
	if workspace == "" {
		workspace, _ = os.Getwd()
	}
	permission := strings.TrimSpace(config.Permission)
	if permission == "" {
		permission = "dontAsk"
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sessionKey := conversationSessionKey(run)
	sessionPath := sessionStorePath(config.SessionStore, "claude", workspace)
	sessionID := ""
	clientMode := providerSessionClientMode(config)
	clientModeGeneration := uint64(1)
	if config.SessionReuse && sessionKey != "" {
		if config.sandbox == nil {
			unlock := lockSession("claude", workspace, sessionKey)
			defer unlock()
		}
		sessionID, clientModeGeneration, _ = loadSessionForClientMode(
			sessionPath,
			"claude",
			workspace,
			sessionKey,
			clientMode,
		)
	}
	resumed := sessionID != ""
	recovered := false
	var response claudeResponse
	var successfulResumeSessionID string
	for attempt := 0; attempt < 2; attempt++ {
		args := claudeArguments(config, permission, sessionID)
		var command *exec.Cmd
		if config.sandbox != nil {
			var err error
			command, err = config.sandbox.Command(requestCtx, bin, args, config.Env)
			if err != nil {
				return openlinker.RuntimeResult{}, err
			}
		} else {
			command = exec.CommandContext(requestCtx, bin, args...) // #nosec G204 -- operator-configured binary, no shell.
		}
		configureProviderProcess(command)
		command.Dir = workspace
		environment := config.Env
		if environment == nil {
			environment = os.Environ()
		}
		allowlist := append([]string{"ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE"}, config.EnvAllowlist...)
		if config.sandbox == nil {
			command.Env = append(sanitizedEnvironment(environment, allowlist), "LC_ALL=C", "LANG=C")
		}
		command.Stdin = strings.NewReader(
			buildPrompt(
				"Claude Code",
				runWithSessionHistory(run, sessionPath, "claude", workspace, sessionKey, sessionID),
			),
		)
		observer := providerstream.NewClaudeObserver(run.Emit, nil)
		stdout := newClaudeResultStream(cancel, observer)
		stderr := &outputTail{}
		command.Stdout, command.Stderr = stdout, stderr
		err := command.Run()
		var parseErr error
		response, parseErr = stdout.Result()
		if parseErr != nil && stdout.err != nil {
			return openlinker.RuntimeResult{}, parseErr
		}
		if ctxErr := requestCtx.Err(); ctxErr != nil {
			if errors.Is(ctxErr, context.DeadlineExceeded) {
				return openlinker.RuntimeResult{}, fmt.Errorf("Claude timed out after %s", timeout)
			}
			return openlinker.RuntimeResult{}, ctxErr
		}
		if err == nil {
			err = parseErr
			if response.IsError || strings.HasPrefix(response.Subtype, "error") {
				err = errors.New("Claude returned an unsuccessful result")
			}
		}
		if err != nil {
			if sessionID != "" && attempt == 0 && missingProviderSession(response.failureMessage()+"\n"+stderr.String()) {
				if deleteErr := deleteSessionID(sessionPath, "claude", workspace, sessionKey); deleteErr != nil {
					return openlinker.RuntimeResult{}, fmt.Errorf("Claude session recovery failed: %w", deleteErr)
				}
				sessionID = ""
				recovered = true
				continue
			}
			return openlinker.RuntimeResult{}, fmt.Errorf("Claude failed: %w: %s", err, boundedText(stderr.String(), 500, "no diagnostic output"))
		}
		// Bind evidence to this successful invocation, never a failed resume
		// discarded by the missing-session retry or the subsequently saved map.
		successfulResumeSessionID = sessionID
		break
	}
	summary := strings.TrimSpace(response.Result)
	if summary == "" {
		return openlinker.RuntimeResult{}, errors.New("Claude completed without a final result")
	}
	if config.SessionReuse && sessionKey != "" && strings.TrimSpace(response.SessionID) != "" {
		if err := saveSessionForClientMode(
			sessionPath,
			"claude",
			workspace,
			sessionKey,
			response.SessionID,
			clientMode,
			clientModeGeneration,
			run,
		); err != nil {
			return openlinker.RuntimeResult{}, sessionPersistenceError("Claude", err)
		}
	}
	result := map[string]any{
		"handled_by": "claude", "claude_permission": permission,
		"claude_model": modelLabel(config.Model), "summary": summary,
	}
	if successfulResumeSessionID != "" {
		result["claude_resume_session_id_sha256"] = fmt.Sprintf("%x", sha256.Sum256([]byte(successfulResumeSessionID)))
	}
	if response.SessionID != "" {
		result["claude_session_id_sha256"] = fmt.Sprintf("%x", sha256.Sum256([]byte(response.SessionID)))
	}
	if config.sandbox != nil {
		result["session_isolation"] = "native"
	}

	if config.SessionReuse && sessionKey != "" {
		result["claude_session_reuse"] = true
		result["claude_session_key_hash"] = sessionKeyHash("claude", workspace, sessionKey)
		result["claude_session_resumed"] = resumed
		result["claude_session_recovered"] = recovered
	}
	return openlinker.RuntimeResult{
		Status: "success", Output: result,
		Events: []openlinker.RuntimeEvent{{EventType: "run.message.delta", Payload: map[string]any{"text": summary}}},
	}, nil
}

func claudeArguments(config ProviderConfig, permission, sessionID string) []string {
	args := []string{"--safe-mode", "--no-chrome", "--disable-slash-commands"}
	if config.sandbox != nil {
		args = []string{"--bare", "--no-chrome", "--disable-slash-commands", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`}
	} else if config.DelegationSocket != "" {
		args = []string{"--bare", "--no-chrome", "--disable-slash-commands", "--strict-mcp-config", "--mcp-config", claudeRunMCPConfig(config)}
	}
	args = append(args, "-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--permission-mode", permission)
	if config.Model != "" {
		args = append(args, "--model", config.Model)
	}
	allowed := append([]string(nil), config.AllowedTools...)
	if config.DelegationSocket != "" {
		for _, tool := range []string{"delegate_agent", "get_delegated_run", "wait_delegated_run"} {
			allowed = appendUniqueString(allowed, "mcp__openlinker_delegation__"+tool)
		}
	}
	if len(allowed) > 0 {
		args = append(args, "--allowedTools", strings.Join(allowed, ","))
	}
	if !config.WebSearch {
		args = append(args, "--disallowedTools", "WebSearch,WebFetch")
	}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	return args
}

func (response claudeResponse) failureMessage() string {
	if !response.IsError && !strings.HasPrefix(response.Subtype, "error") {
		return ""
	}
	return response.Result + "\n" + strings.Join(response.Errors, "\n")
}
