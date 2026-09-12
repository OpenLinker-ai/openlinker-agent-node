package adapters

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/sessionsandbox"
)

func validateSessionIsolation(c ProviderConfig) error {
	if err := c.SessionIsolation.Validate(); err != nil {
		return err
	}
	if !c.SessionIsolation.Enabled() {
		return nil
	}
	if !c.SessionReuse {
		return errors.New("native session isolation requires SESSION_REUSE=true")
	}
	if c.SessionStore != "" {
		return errors.New("native isolation owns per-session maps; remove the legacy SESSION_STORE override")
	}
	if len(c.DelegationTargets) != 0 || c.DelegationSocket != "" {
		return errors.New("native isolation does not expose host delegation sockets")
	}
	if len(c.EnvAllowlist) != 0 {
		return errors.New("native isolation uses a fixed credential/environment allowlist; remove ENV_ALLOWLIST")
	}
	return nil
}

func isolatedEnvironment(c ProviderConfig) ([]string, error) {
	key := "CODEX_API_KEY"
	if c.Provider == "claude" {
		key = "ANTHROPIC_API_KEY"
	}
	env := c.Env
	if env == nil {
		env = os.Environ()
	}
	values := map[string]string{}
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if ok && (name == key || name == "PATH") {
			values[name] = value
		}
	}
	if strings.TrimSpace(values[key]) == "" {
		return nil, errors.New("native isolation requires a dedicated " + key + "; personal OAuth/keychain credentials are not imported")
	}
	if values["PATH"] == "" {
		values["PATH"] = os.Getenv("PATH")
	}
	result := make([]string, 0, len(values))
	for name, value := range values {
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("invalid native isolation environment")
		}
		result = append(result, name+"="+value)
	}
	sort.Strings(result)
	return result, nil
}

// CheckSessionIsolation is called before Worker startup. Both the dedicated
// credential and an actual sandbox denied-read/allowed-write probe must pass.
func CheckSessionIsolation(ctx context.Context, c ProviderConfig) error {
	if err := validateSessionIsolation(c); err != nil {
		return err
	}
	if !c.SessionIsolation.Enabled() {
		return nil
	}
	if _, err := isolatedEnvironment(c); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return sessionsandbox.Probe(ctx, c.SessionIsolation)
}

func prepareIsolatedSession(ctx context.Context, c ProviderConfig, r RunContext) (ProviderConfig, func() error, error) {
	noop := func() error { return nil }
	if err := validateSessionIsolation(c); err != nil {
		return c, noop, err
	}
	if !c.SessionIsolation.Enabled() {
		return c, noop, nil
	}
	if r.Authority == nil || strings.TrimSpace(r.Authority.PrincipalScopeID) == "" || r.AgentID == "" || r.RunID == "" ||
		r.Conversation == nil || r.Conversation.Source != "core" || strings.TrimSpace(r.Conversation.SessionKey) == "" || r.Conversation.CurrentRunID != r.RunID {
		return c, noop, errors.New("native isolation requires trusted Core principal, Agent and current conversation authority")
	}
	env, err := isolatedEnvironment(c)
	if err != nil {
		return c, noop, err
	}
	scope := sessionsandbox.Scope(c.Provider, r.AgentID, r.Authority.PrincipalScopeID, r.Conversation.SessionKey)
	s, err := sessionsandbox.Open(ctx, c.SessionIsolation, scope)
	if err != nil {
		return c, noop, err
	}
	c.sandbox = s
	c.Workspace = s.Workspace()
	c.SessionStore = s.Store()
	c.Env = s.Environment(env)
	if c.Provider == "codex" {
		// Outer OS sandbox contains the whole client. Nesting Codex's sandbox
		// would need additional OS privileges; never weaken the outer boundary.
		c.Sandbox = "danger-full-access"
		c.CodexApproval = "never"
		if c.CodexBaseURL == "" {
			c.CodexBaseURL = "https://api.openai.com/v1"
		}
	}
	return c, s.Close, nil
}
