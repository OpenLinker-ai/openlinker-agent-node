package adapters

import (
	"context"
	"errors"
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
	if err := validateIsolatedModelEndpoint(c); err != nil {
		return err
	}
	if c.SessionIsolation.RuntimeBin != "" {
		return errors.New("host-auth native isolation uses the installed client sandbox; remove SESSION_SANDBOX_BIN")
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
		return errors.New("native isolation uses a fixed host-client environment allowlist; remove ENV_ALLOWLIST")
	}
	for _, tool := range c.AllowedTools {
		switch tool {
		case "Read", "Edit", "Write", "Glob", "Grep", "Bash":
		default:
			return errors.New("native Claude ALLOWED_TOOLS must be Read/Edit/Write/Glob/Grep/Bash; use WEB_SEARCH for search")
		}
	}
	return nil
}

// CheckSessionIsolation validates the tool boundary before Worker startup.
// Authentication remains owned by the installed client; no login is performed.
func CheckSessionIsolation(ctx context.Context, c ProviderConfig) error {
	if err := validateSessionIsolation(c); err != nil {
		return err
	}
	if !c.SessionIsolation.Enabled() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return checkNativeToolSandbox(ctx, c)
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
	env, err := nativeClientEnvironment(c)
	if err != nil {
		return c, noop, err
	}
	scope := sessionsandbox.Scope(c.Provider, r.AgentID, r.Authority.PrincipalScopeID, r.Conversation.SessionKey)
	s, err := sessionsandbox.OpenClient(ctx, c.SessionIsolation, scope)
	if err != nil {
		return c, noop, err
	}
	c.sandbox = s
	c.Workspace = s.Workspace()
	c.SessionStore = s.Store()
	c.Env = env
	c.toolPolicy, err = newNativeToolPolicy(c, s)
	if err != nil {
		_ = s.Close()
		return c, noop, err
	}
	if c.Provider == "codex" {
		c.Sandbox = "workspace-write"
		c.CodexApproval = "never"
	}
	if c.Provider == "claude" {
		c.Permission = "default"
	}
	return c, s.Close, nil
}
