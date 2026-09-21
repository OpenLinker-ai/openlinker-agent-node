package agentnode

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/sessionsandbox"
)

// nativeIsolationHint tells operators how to opt out when the default policy
// cannot start. There is never an automatic unsandboxed fallback.
const nativeIsolationHint = "; native session isolation is the default for Codex/Claude, set OPENLINKER_AGENT_NODE_SESSION_ISOLATION=off to run without it"

// defaultSessionRoot is a sibling of the host-auth coordination directory, not
// inside it, so tool-writable session storage never overlaps the lock files.
func defaultSessionRoot(get EnvLookup) (string, error) {
	home := strings.TrimSpace(get("HOME"))
	if !filepath.IsAbs(home) {
		return "", errors.New("native isolation needs an absolute HOME for the default SESSION_ROOT; set OPENLINKER_AGENT_NODE_SESSION_ROOT")
	}
	return filepath.Join(home, ".local", "state", "openlinker-agent-node-sessions"), nil
}

func sessionIsolationFromEnv(get EnvLookup, defaultNative bool) (sessionsandbox.Config, error) {
	c, defaulted, err := parseSessionIsolation(get, defaultNative)
	if err != nil && defaulted {
		err = errors.New(err.Error() + nativeIsolationHint)
	}
	return c, err
}

func parseSessionIsolation(get EnvLookup, defaultNative bool) (c sessionsandbox.Config, defaulted bool, _ error) {
	c = sessionsandbox.Config{
		Mode: strings.ToLower(strings.TrimSpace(get("OPENLINKER_AGENT_NODE_SESSION_ISOLATION"))),
		Root: get("OPENLINKER_AGENT_NODE_SESSION_ROOT"), RuntimeBin: get("OPENLINKER_AGENT_NODE_SESSION_SANDBOX_BIN"),
		TempRoot: get("OPENLINKER_AGENT_NODE_SESSION_TEMP_ROOT"),
	}
	if c.Mode == "" && defaultNative {
		// Model-driven local clients are isolated unless explicitly turned off.
		c.Mode, defaulted = "native", true
	}
	if c.Enabled() && c.Root == "" {
		root, err := defaultSessionRoot(get)
		if err != nil {
			return c, defaulted, err
		}
		c.Root = root
	}
	if c.Mode == "" || c.Mode == "off" {
		// [] and null still explicitly configure a native-only policy. Do not
		// erase that intent during JSON decoding and then accept an off boundary.
		for _, name := range []string{"OPENLINKER_AGENT_NODE_SESSION_READ_PATHS", "OPENLINKER_AGENT_NODE_SESSION_NETWORK_DOMAINS"} {
			if strings.TrimSpace(get(name)) != "" {
				return c, defaulted, fmt.Errorf("%s requires OPENLINKER_AGENT_NODE_SESSION_ISOLATION=native", name)
			}
		}
	}
	var err error
	c.ReadPaths, err = parseJSONStringArray(get("OPENLINKER_AGENT_NODE_SESSION_READ_PATHS"), "OPENLINKER_AGENT_NODE_SESSION_READ_PATHS")
	if err != nil {
		return c, defaulted, err
	}
	c.AllowedDomains, err = parseJSONStringArray(get("OPENLINKER_AGENT_NODE_SESSION_NETWORK_DOMAINS"), "OPENLINKER_AGENT_NODE_SESSION_NETWORK_DOMAINS")
	if err != nil {
		return c, defaulted, err
	}
	if c.Enabled() {
		// Stable installation authority, never a rotating token/runtime Session.
		u, err := url.Parse(strings.TrimSpace(get("OPENLINKER_URL")))
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return c, defaulted, errors.New("native isolation requires OPENLINKER_URL with a stable Core URL and no credentials/query/fragment")
		}
		c.Namespace = strings.TrimRight(u.String(), "/")
	}
	if err := c.Validate(); err != nil {
		return c, defaulted, err
	}
	if c.Enabled() {
		for _, root := range []string{c.Root, c.TempRoot} {
			if root != "" {
				if err := sessionsandbox.CheckOutsideGitWorktree(root); err != nil {
					return c, defaulted, err
				}
			}
		}
	}
	return c, defaulted, nil
}

// Only model-driven local clients can be isolated; other adapters reject any
// native-only setting instead of silently ignoring it.
func unisolatedAdapterOptions(get EnvLookup, authConcurrency string) error {
	c, err := sessionIsolationFromEnv(get, false)
	if err != nil {
		return err
	}
	if c.Enabled() {
		return errors.New("native isolation is supported only by Codex and Claude adapters")
	}
	if authConcurrency != "" {
		return errors.New("HOST_AUTH_CONCURRENCY requires native Codex or Claude isolation")
	}
	return nil
}

// providerIsolationFromEnv applies the native default for Codex and Claude.
func providerIsolationFromEnv(get EnvLookup, provider, authConcurrency string) (sessionsandbox.Config, error) {
	// Only the Codex adapter reads its mock response, which starts no client and
	// so keeps the unisolated default. A leftover Codex mock variable must never
	// turn off isolation for the real Claude client.
	mock := provider == "codex" && get("OPENLINKER_AGENT_NODE_CODEX_MOCK_RESPONSE") != ""
	c, err := sessionIsolationFromEnv(get, !mock)
	if err != nil {
		return c, err
	}
	if c.Enabled() && mock {
		return c, errors.New("mock responses cannot be combined with native isolation")
	}
	if !c.Enabled() && authConcurrency != "" {
		return c, errors.New("HOST_AUTH_CONCURRENCY requires native Codex or Claude isolation")
	}
	if !c.Enabled() {
		return c, nil
	}
	return c, nativeProviderOptions(get, provider)
}

// Native mode always runs the client in a private per-conversation workspace.
// Reject settings it would otherwise ignore or contradict, so an operator who
// pointed the Agent at a project does not assume the project is still in use.
func nativeProviderOptions(get EnvLookup, provider string) error {
	prefix := "OPENLINKER_AGENT_NODE_" + strings.ToUpper(provider) + "_"
	if get(prefix+"WORKSPACE") != "" {
		return errors.New(prefix + "WORKSPACE is not used with native isolation, which runs each conversation in a private workspace; remove it (set OPENLINKER_AGENT_NODE_ADAPTER=" + provider + " if it selected the adapter)" + nativeIsolationHint)
	}
	if v := get(prefix + "SESSION_REUSE"); v != "" && !boolOption(v, false) {
		return errors.New("native isolation requires " + prefix + "SESSION_REUSE=true" + nativeIsolationHint)
	}
	return nil
}
