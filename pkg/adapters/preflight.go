package adapters

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/provideroutput"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerpreflight"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/sessionsandbox"
)

const MinimumCodexVersion = providerpreflight.MinimumCodexVersion
const MinimumClaudeVersion = providerpreflight.MinimumClaudeVersion

func CheckProviderCLI(ctx context.Context, c ProviderConfig) (string, error) {
	return providerpreflight.Check(ctx, providerpreflight.Config{Provider: c.Provider, Bin: c.Bin, Env: c.Env})
}

func checkNativeToolSandbox(ctx context.Context, c ProviderConfig) error {
	env, err := nativeClientEnvironment(c)
	if err != nil {
		return err
	}
	c.Env = env
	if _, err = CheckProviderCLI(ctx, c); err != nil {
		return err
	}
	s, err := sessionsandbox.OpenClient(ctx, c.SessionIsolation, sessionsandbox.Scope("host-client-preflight", c.Provider))
	if err != nil {
		return err
	}
	defer s.Close()
	c.sandbox, c.Workspace = s, s.Workspace()
	c.toolPolicy, err = newNativeToolPolicy(c, s)
	if err != nil {
		return err
	}
	bin := c.Bin
	if bin == "" {
		bin = c.Provider
	}
	if c.Provider == "codex" && runtime.GOOS == "darwin" {
		args := append([]string(nil), c.toolPolicy.codex...)
		args = append(args, "sandbox", "-P", "openlinker_session", "-C", s.Workspace(), "--", "/bin/sh", "-c",
			`if cat "$1" >/dev/null 2>&1; then exit 91; fi; printf verified > "$2"; test "$(cat "$2")" = verified`, "probe", s.PolicyPath(), filepath.Join(s.Workspace(), "probe"))
		if err = nativeHostCommand(ctx, c, bin, args).Run(); err != nil {
			return errors.New("Codex tool sandbox enforcement probe failed; no unsandboxed fallback")
		}
		return nil
	}
	if runtime.GOOS == "linux" {
		deps := []string{"bwrap"}
		if c.Provider == "claude" {
			deps = append(deps, "socat", "rg")
		}
		for _, dep := range deps {
			if _, err = exec.LookPath(dep); err != nil {
				return errors.New("native sandbox OS dependencies are missing")
			}
		}
		cmd := exec.CommandContext(ctx, "bwrap", "--unshare-user", "--unshare-net", "--unshare-pid", "--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev", "--", "/bin/true")
		if err = cmd.Run(); err != nil {
			return errors.New("native sandbox requires working unprivileged user/network/PID namespaces")
		}
		if c.Provider == "codex" {
			return nil
		}
	}
	// Claude owns sandbox initialization and rejects unavailable backends with
	// failIfUnavailable before running a task. Check the CLI surface and OS
	// prerequisites without calling a model or touching login state.
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	probe := nativeHostCommand(probeCtx, c, bin, []string{"--help"})
	stdout, stderr := provideroutput.NewLimitedBuffer(cancel), provideroutput.NewLimitedBuffer(cancel)
	probe.Stdout, probe.Stderr = stdout, stderr
	if err = probe.Run(); err != nil {
		return errors.New("Claude native sandbox capability probe failed")
	}
	if err = provideroutput.LimitError("Claude", stdout, stderr); err != nil {
		return err
	}
	for _, flag := range []string{"--restricted", "--safe-mode", "--settings", "--permission-prompts", "--tools"} {
		if !strings.Contains(stdout.String(), flag) {
			return errors.New("Claude lacks required host-auth sandbox capabilities")
		}
	}
	if runtime.GOOS == "darwin" {
		if _, err = exec.LookPath("sandbox-exec"); err != nil {
			return errors.New("Claude native sandbox requires macOS sandbox-exec")
		}
	}
	return nil
}
