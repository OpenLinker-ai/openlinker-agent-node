package agentnode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"strings"
	"time"
)

type CommandAdapter struct {
	Command      string
	Args         []string
	CWD          string
	Env          []string
	EnvAllowlist []string
	Timeout      time.Duration
}

func (a CommandAdapter) Run(ctx context.Context, input any, runCtx RunContext) (any, error) {
	if a.Command == "" {
		return nil, fmt.Errorf("OPENLINKER_AGENT_NODE_COMMAND is required")
	}
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payload, err := json.Marshal(buildAdapterEnvelope(input, runCtx))
	if err != nil {
		return nil, err
	}
	// #nosec G204 -- command adapter intentionally executes an operator-configured binary without a shell.
	cmd := exec.CommandContext(reqCtx, a.Command, a.Args...)
	if a.CWD != "" {
		cmd.Dir = a.CWD
	}
	baseEnv := a.Env
	if baseEnv == nil {
		baseEnv = os.Environ()
	}
	cmd.Env = append(sanitizedEnv(baseEnv, a.EnvAllowlist), helperEnv(runCtx)...)
	cmd.Stdin = bytes.NewReader(payload)
	stdout := newLimitedOutputBuffer(cancel)
	stderr := newLimitedOutputBuffer(cancel)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if outputErr := adapterOutputLimitError("command", stdout, stderr); outputErr != nil {
			return nil, outputErr
		}
		if reqCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("command timed out after %s", timeout)
		}
		return nil, fmt.Errorf("command failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if outputErr := adapterOutputLimitError("command", stdout, stderr); outputErr != nil {
		return nil, outputErr
	}
	return parseCommandOutput(stdout.String(), stderr.String())
}

func parseCommandOutput(stdout, stderr string) (any, error) {
	text := strings.TrimSpace(stdout)
	if text == "" {
		return JSONMap{"ok": true, "stderr": strings.TrimSpace(stderr)}, nil
	}
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		result := JSONMap{"text": text}
		if trimmed := strings.TrimSpace(stderr); trimmed != "" {
			result["stderr"] = trimmed
		}
		return result, nil
	}
	if bodyMap, ok := value.(map[string]any); ok {
		if output, ok := bodyMap["output"]; ok {
			return output, nil
		}
	}
	return value, nil
}

func sanitizedEnv(env []string, extraAllowlist []string) []string {
	// 无额外 allowlist 时直接用全局 map，避免 clone 开销
	allowed := defaultAdapterEnvAllowlist
	if len(extraAllowlist) > 0 {
		allowed = maps.Clone(defaultAdapterEnvAllowlist)
		for _, key := range extraAllowlist {
			key = strings.TrimSpace(key)
			if key != "" {
				allowed[key] = true
			}
		}
	}
	next := make([]string, 0, len(env))
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if !adapterEnvKeyAllowed(key, allowed) {
			continue
		}
		next = append(next, item)
	}
	return next
}

var defaultAdapterEnvAllowlist = map[string]bool{
	"PATH":   true,
	"HOME":   true,
	"TMPDIR": true,
	"TEMP":   true,
	"TMP":    true,
	"LANG":   true,
}

func adapterEnvKeyAllowed(key string, allowed map[string]bool) bool {
	if allowed[key] {
		return true
	}
	return strings.HasPrefix(key, "LC_")
}
