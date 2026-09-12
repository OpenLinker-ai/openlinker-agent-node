package agentnode

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/sessionsandbox"
)

func sessionIsolationFromEnv(get EnvLookup) (sessionsandbox.Config, error) {
	config := sessionsandbox.Config{
		Mode:      strings.ToLower(strings.TrimSpace(get("OPENLINKER_AGENT_NODE_SESSION_ISOLATION"))),
		Root:      strings.TrimSpace(get("OPENLINKER_AGENT_NODE_SESSION_ROOT")),
		Image:     strings.TrimSpace(get("OPENLINKER_AGENT_NODE_SESSION_IMAGE")),
		Network:   strings.TrimSpace(get("OPENLINKER_AGENT_NODE_SESSION_NETWORK")),
		Namespace: strings.TrimSpace(defaultString(get("OPENLINKER_URL"), get("OPENLINKER_RUNTIME_URL"))),
	}
	if err := config.Validate(); err != nil {
		return config, err
	}
	if !config.Enabled() {
		return config, nil
	}
	return config, nil
}

func sessionIsolationForProvider(get EnvLookup, mode string, config sessionsandbox.Config) (sessionsandbox.Config, error) {
	if !config.Enabled() {
		return config, nil
	}
	prefix := "OPENLINKER_AGENT_NODE_" + strings.ToUpper(mode)
	// Existing defaults and paths stay unchanged unless this whole mode is
	// explicitly selected. A legacy workspace/store is never silently imported.
	if !strings.EqualFold(strings.TrimSpace(get(prefix+"_SESSION_REUSE")), "true") {
		return config, fmt.Errorf("%s_SESSION_REUSE must be explicitly true for Docker isolation", prefix)
	}
	if get(prefix+"_SESSION_STORE") != "" || get(prefix+"_WORKSPACE") != "" {
		return config, fmt.Errorf("Docker isolation owns each conversation's workspace and map; remove legacy %s_WORKSPACE and %s_SESSION_STORE", prefix, prefix)
	}
	if mode == "codex" && (get(prefix+"_SANDBOX") != "" || get(prefix+"_APPROVAL") != "" || get(prefix+"_MOCK_RESPONSE") != "") {
		return config, fmt.Errorf("Docker isolation owns Codex sandbox/approval policy and cannot use a mock response")
	}
	if get("OPENLINKER_AGENT_NODE_DELEGATION_TARGETS") != "" && strings.TrimSpace(get("OPENLINKER_AGENT_NODE_DELEGATION_TARGETS")) != "[]" {
		return config, fmt.Errorf("Docker isolation cannot expose host MCP delegation sockets")
	}
	// Resolve existing ancestors too: aliases must not hide an SDK-state overlap.
	if data := strings.TrimSpace(get("OPENLINKER_AGENT_NODE_DATA_DIR")); data != "" {
		absolute, err := prospectiveRealPath(data)
		if err != nil {
			return config, err
		}
		root, err := prospectiveRealPath(config.Root)
		if err != nil {
			return config, err
		}
		for _, pair := range [][2]string{{root, absolute}, {absolute, root}} {
			rel, err := filepath.Rel(pair[0], pair[1])
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return config, fmt.Errorf("SESSION_ROOT and SDK DATA_DIR must be separate, non-nested directories")
			}
		}
	}
	return config, nil
}

func prospectiveRealPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(absolute)
		if err == nil {
			for index := len(tail) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, tail[index])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(absolute) == absolute {
			return "", err
		}
		tail = append(tail, filepath.Base(absolute))
		absolute = filepath.Dir(absolute)
	}
}
