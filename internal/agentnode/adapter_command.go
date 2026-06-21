package agentnode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type CommandAdapter struct {
	Command string
	Args    []string
	CWD     string
	Env     []string
	Timeout time.Duration
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
	cmd := exec.CommandContext(reqCtx, a.Command, a.Args...)
	if a.CWD != "" {
		cmd.Dir = a.CWD
	}
	baseEnv := a.Env
	if baseEnv == nil {
		baseEnv = os.Environ()
	}
	cmd.Env = append(sanitizedEnv(baseEnv), helperEnv(runCtx)...)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if reqCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("command timed out after %s", timeout)
		}
		return nil, fmt.Errorf("command failed: %w: %s", err, strings.TrimSpace(stderr.String()))
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

func sanitizedEnv(env []string) []string {
	next := make([]string, 0, len(env))
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(key, "OPENLINKER_") && looksSecretKey(key) {
			continue
		}
		next = append(next, item)
	}
	return next
}

func looksSecretKey(key string) bool {
	upper := strings.ToUpper(key)
	return strings.Contains(upper, "TOKEN") ||
		strings.Contains(upper, "JWT") ||
		strings.Contains(upper, "PASSWORD") ||
		strings.Contains(upper, "SECRET") ||
		strings.Contains(upper, "KEY")
}
