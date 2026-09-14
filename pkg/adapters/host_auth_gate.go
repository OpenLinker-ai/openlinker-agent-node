package adapters

import (
	"context"
	"errors"
	"fmt"
	"os"
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

// One conservative group per OS user/provider, independent of session roots,
// agents, auth-directory aliases or the unknown keychain/account identity.
// /tmp is the shared OS directory, not environment-controlled TMPDIR. The lock
// has owner-only/no-follow checks and must never be unlinked on release.
func hostAuthLockPath(provider string) string {
	return filepath.Join("/tmp", fmt.Sprintf("openlinker-node-host-auth-%d-%s.lock", os.Geteuid(), provider))
}

// This is cooperative Node admission, not an account-wide token-refresh lock.
// Ordinary CLI/desktop processes do not participate. Hold it before starting the
// client (and loading auth) through process exit, retries and session persistence.
func acquireHostAuthPermit(ctx context.Context, c ProviderConfig, emit func(string, any) error) (func() error, error) {
	noop := func() error { return nil }
	if c.sandbox == nil || c.HostAuthConcurrency == "client-managed" {
		return noop, ctx.Err()
	}
	waiting := false
	for {
		if err := ctx.Err(); err != nil {
			return noop, err
		}
		lock, err := appfiles.AcquireLock(hostAuthLockPath(c.Provider))
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
