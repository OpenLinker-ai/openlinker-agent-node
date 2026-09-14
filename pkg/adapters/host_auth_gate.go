package adapters

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/appfiles"
)

func validateHostAuthConcurrency(c ProviderConfig) error {
	if c.HostAuthConcurrency == "" {
		return nil
	}
	if !c.SessionIsolation.Enabled() {
		return errors.New("HOST_AUTH_CONCURRENCY requires SESSION_ISOLATION=native")
	}
	if c.HostAuthConcurrency != "serial" && c.HostAuthConcurrency != "client-managed" {
		return errors.New("HOST_AUTH_CONCURRENCY must be serial or client-managed")
	}
	return nil
}

func hostAuthStatePath(home string) string {
	return filepath.Join(home, ".local", "state", "openlinker-agent-node")
}

// HOME is the trusted client's HOME, not a session's tool HOME or TMPDIR.
// Services must share the underlying state directory, not just its pathname.
func hostAuthLockPath(c ProviderConfig) (string, error) {
	env, err := nativeClientEnvironment(c)
	if err != nil {
		return "", err
	}
	if c.Provider != "codex" && c.Provider != "claude" {
		return "", errors.New("host-auth admission requires Codex or Claude")
	}
	return prepareHostAuthLockPath(env, c.Provider)
}

// This is cooperative Node admission, not an account-wide token-refresh lock.
// Ordinary CLI/desktop processes do not participate. Hold it before starting the
// client (and loading auth) through process exit, retries and session persistence.
func acquireHostAuthPermit(ctx context.Context, c ProviderConfig, emit func(string, any) error) (func() error, error) {
	noop := func() error { return nil }
	if c.sandbox == nil || c.HostAuthConcurrency == "client-managed" {
		return noop, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return noop, err
	}
	path, err := hostAuthLockPath(c)
	if err != nil {
		return noop, err
	}
	waiting := false
	for {
		if err := ctx.Err(); err != nil {
			return noop, err
		}
		lock, err := appfiles.AcquireLock(path)
		if err == nil {
			if err := ctx.Err(); err != nil {
				_ = lock.Release()
				return noop, err
			}
			return lock.Release, nil
		}
		if !errors.Is(err, appfiles.ErrLockBusy) {
			return noop, errors.New("native host-auth coordination lock is unavailable or unsafe")
		}
		if !waiting && emit != nil {
			if err := emit("run.message.delta", map[string]any{"text": "Waiting for another Node client using this provider to finish (host-auth serial policy)."}); err != nil {
				return noop, err
			}
		}
		waiting = true
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return noop, ctx.Err()
		case <-timer.C:
		}
	}
}
