package adapters

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/sessionsandbox"
)

func validateSessionIsolation(config ProviderConfig) error {
	if err := config.SessionIsolation.Validate(); err != nil {
		return err
	}
	if !config.SessionIsolation.Enabled() {
		return nil
	}
	if !config.SessionReuse {
		return errors.New("Docker session isolation requires the provider SESSION_REUSE=true")
	}
	if config.SessionStore != "" {
		return errors.New("Docker sessions manage private per-session maps; remove the legacy SESSION_STORE override")
	}
	if len(config.DelegationTargets) != 0 || config.DelegationSocket != "" {
		return errors.New("Docker session isolation does not yet support host MCP delegation sockets")
	}
	for _, key := range config.EnvAllowlist {
		if strings.HasPrefix(key, "OPENLINKER_") || strings.HasPrefix(key, "DOCKER_") {
			return errors.New("platform and Docker control variables cannot be allowed into isolated sessions")
		}
	}
	return nil
}

// prepareIsolatedSession changes only this invocation's configuration. The
// SDK-authenticated authority and Core conversation are the sole scope inputs.
// Runtime Session IDs/epochs and Run IDs deliberately do not split a conversation.
func prepareIsolatedSession(ctx context.Context, config ProviderConfig, run RunContext) (ProviderConfig, func() error, error) {
	noop := func() error { return nil }
	if err := validateSessionIsolation(config); err != nil {
		return config, noop, err
	}
	if !config.SessionIsolation.Enabled() {
		return config, noop, nil
	}
	if run.Authority == nil || strings.TrimSpace(run.Authority.PrincipalScopeID) == "" || run.AgentID == "" || run.RunID == "" ||
		run.Conversation == nil || run.Conversation.Source != "core" || strings.TrimSpace(run.Conversation.SessionKey) == "" || run.Conversation.CurrentRunID != run.RunID {
		return config, noop, errors.New("isolated session requires trusted Core principal, Agent, and current conversation authority")
	}
	provider := strings.ToLower(strings.TrimSpace(config.Provider))
	environment, err := isolatedEnvironment(config, provider, true)
	if err != nil {
		return config, noop, err
	}
	scope := sessionsandbox.Scope(provider, run.AgentID, run.Authority.PrincipalScopeID, run.Conversation.SessionKey)
	session, err := sessionsandbox.Open(ctx, config.SessionIsolation, scope)
	if err != nil {
		return config, noop, err
	}
	config.sandbox = session
	config.Workspace = sessionsandbox.Workspace
	config.SessionStore = session.Store()
	config.Env = environment
	if provider == "codex" {
		// The entire client already runs under Docker. A nested native sandbox
		// would require relaxing the outer container's syscall/capability policy.
		config.Sandbox = "danger-full-access"
		config.CodexApproval = "never"
		// Use explicit API-key auth; never hydrate a personal auth.json or keychain.
		if config.CodexBaseURL == "" {
			config.CodexBaseURL = "https://api.openai.com/v1"
		}
	}
	return config, session.Close, nil
}

func isolatedEnvironment(config ProviderConfig, provider string, requireCredential bool) ([]string, error) {
	key := "CODEX_API_KEY"
	if provider == "claude" {
		key = "ANTHROPIC_API_KEY"
	}
	allowed := map[string]bool{key: true, "HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true}
	for _, name := range config.EnvAllowlist {
		allowed[name] = true
	}
	values := map[string]string{}
	environment := config.Env
	if environment == nil {
		environment = os.Environ()
	}
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if ok && allowed[name] {
			values[name] = value
		}
	}
	if requireCredential && strings.TrimSpace(values[key]) == "" {
		return nil, errors.New("Docker session isolation requires a dedicated " + key + "; personal OAuth/keychain login is not imported")
	}
	// These roots are fixed even if the operator's allowlist contains them.
	for name, value := range map[string]string{
		"HOME": "/session/home", "CODEX_HOME": "/session/codex", "CLAUDE_CONFIG_DIR": "/session/claude",
		"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "TMPDIR": "/tmp",
		"TMP": "/tmp", "TEMP": "/tmp", "USER": "openlinker", "LANG": "C", "LC_ALL": "C",
		"XDG_CONFIG_HOME": "/session/home/.config", "XDG_CACHE_HOME": "/session/home/.cache", "XDG_DATA_HOME": "/session/home/.local/share",
	} {
		values[name] = value
	}
	result := make([]string, 0, len(values))
	for name, value := range values {
		if strings.HasPrefix(name, "OPENLINKER_") || strings.HasPrefix(name, "DOCKER_") || strings.ContainsAny(name+value, "\r\n\x00") {
			return nil, errors.New("unsafe isolated client environment")
		}
		result = append(result, name+"="+value)
	}
	sort.Strings(result)
	return result, nil
}
