package agentnode

import (
	"errors"
	"net/url"
	"strings"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/sessionsandbox"
)

func sessionIsolationFromEnv(get EnvLookup) (sessionsandbox.Config, error) {
	c := sessionsandbox.Config{
		Mode: strings.ToLower(strings.TrimSpace(get("OPENLINKER_AGENT_NODE_SESSION_ISOLATION"))),
		Root: get("OPENLINKER_AGENT_NODE_SESSION_ROOT"), RuntimeBin: get("OPENLINKER_AGENT_NODE_SESSION_SANDBOX_BIN"),
		TempRoot: get("OPENLINKER_AGENT_NODE_SESSION_TEMP_ROOT"),
	}
	var err error
	c.ReadPaths, err = parseJSONStringArray(get("OPENLINKER_AGENT_NODE_SESSION_READ_PATHS"), "OPENLINKER_AGENT_NODE_SESSION_READ_PATHS")
	if err != nil {
		return c, err
	}
	c.AllowedDomains, err = parseJSONStringArray(get("OPENLINKER_AGENT_NODE_SESSION_NETWORK_DOMAINS"), "OPENLINKER_AGENT_NODE_SESSION_NETWORK_DOMAINS")
	if err != nil {
		return c, err
	}
	if c.Enabled() {
		if get("OPENLINKER_AGENT_NODE_CODEX_MOCK_RESPONSE") != "" {
			return c, errors.New("mock responses cannot be combined with native isolation")
		}
		// Stable installation authority, never a rotating token/runtime Session.
		u, err := url.Parse(strings.TrimSpace(get("OPENLINKER_URL")))
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return c, errors.New("native isolation requires OPENLINKER_URL with a stable Core URL and no credentials/query/fragment")
		}
		c.Namespace = strings.TrimRight(u.String(), "/")
	}
	return c, c.Validate()
}
