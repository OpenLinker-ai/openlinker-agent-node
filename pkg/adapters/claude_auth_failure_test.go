package adapters

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

const (
	claudeFixtureInit         = `{"type":"system","subtype":"init","session_id":"fixture-session"}`
	claudeFixtureRequesting   = `{"type":"system","subtype":"status","status":"requesting","session_id":"fixture-session"}`
	claudeFixtureAuthMessage  = `{"type":"assistant","error":"authentication_failed","is_api_error_message":true,"message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"Failed to authenticate: OAuth session expired and could not be refreshed"}]},"session_id":"fixture-session"}`
	claudeFixtureAuthResult   = `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","result":"Failed to authenticate: OAuth session expired and could not be refreshed","session_id":"fixture-session"}`
	claudeFixtureAuthRetry    = `{"type":"system","subtype":"api_retry","attempt":%d,"max_retries":10,"retry_delay_ms":50,"error_status":401,"error":"authentication_failed","session_id":"fixture-session"}`
	claudeFixtureLimitRetry   = `{"type":"system","subtype":"api_retry","attempt":%d,"max_retries":10,"retry_delay_ms":50,"error_status":429,"error":"rate_limit","session_id":"fixture-session"}`
	claudeFixtureModelResult  = `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","result":"API Error: model not found","session_id":"fixture-session"}`
	claudeFixtureSuccessFinal = `{"type":"result","subtype":"success","result":"fixture answer","session_id":"fixture-session"}`
)

type claudeAuthOutcome struct {
	result  openlinker.RuntimeResult
	err     error
	elapsed time.Duration
	calls   string
}

func claudeFailureFixture(t *testing.T, body string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n[ \"${CLAUDE_FIXTURE_WARM:-}\" = 1 ] && exit 0\nprintf '%s\\n' \"$*\" >> " + jsonString(calls) + "\ncat >/dev/null\n" + body
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	warm := exec.Command(bin)
	warm.Env = append(os.Environ(), "CLAUDE_FIXTURE_WARM=1")
	if err := warm.Run(); err != nil {
		t.Fatal(err)
	}
	return bin, calls
}

func claudeFixtureLines(lines ...string) string {
	var out strings.Builder
	for _, line := range lines {
		out.WriteString("printf '%s\\n' '" + line + "'\n")
	}
	return out.String()
}

func shortenClaudeWaits(t *testing.T, startupDeadline time.Duration) {
	t.Helper()
	startup, grace := claudeStartupOutputDeadline, claudeTerminalErrorGrace
	claudeStartupOutputDeadline, claudeTerminalErrorGrace = startupDeadline, 200*time.Millisecond
	t.Cleanup(func() { claudeStartupOutputDeadline, claudeTerminalErrorGrace = startup, grace })
}

func runClaudeFailureFixture(t *testing.T, mode, body string, prepare func(*ProviderConfig, RunContext)) claudeAuthOutcome {
	t.Helper()
	bin, calls := claudeFailureFixture(t, body)
	config := ProviderConfig{Bin: bin, Workspace: t.TempDir(), SessionReuse: true}
	run := isolationRun("auth-conversation")
	if mode == "native" {
		config = isolationConfig(t, "claude")
		config.Bin = bin
	}
	config.Timeout = 20 * time.Second
	if prepare != nil {
		prepare(&config, run)
	}
	start := time.Now()
	result, err := (ClaudeProvider{Config: config}).Run(context.Background(), run)
	raw, _ := os.ReadFile(calls)
	return claudeAuthOutcome{result: result, err: err, elapsed: time.Since(start), calls: string(raw)}
}

func requireClaudeFailedFast(t *testing.T, outcome claudeAuthOutcome, code string, fragments ...string) {
	t.Helper()
	if outcome.elapsed > 8*time.Second {
		t.Fatalf("waited %s instead of failing promptly", outcome.elapsed)
	}
	if outcome.err != nil {
		t.Fatalf("expected classified %s result, got error: %v", code, outcome.err)
	}
	if outcome.result.Status != "failed" || outcome.result.Error == nil || outcome.result.Error.Code != code {
		t.Fatalf("expected %s, got %#v", code, outcome.result)
	}
	for _, fragment := range fragments {
		if !strings.Contains(outcome.result.Error.Message, fragment) {
			t.Fatalf("message %q lacks %q", outcome.result.Error.Message, fragment)
		}
	}
	if len(outcome.result.Error.Message) > 500 {
		t.Fatalf("unbounded message: %d bytes", len(outcome.result.Error.Message))
	}
}

func claudeFailureModes(t *testing.T, test func(*testing.T, string)) {
	for _, mode := range []string{"standard", "native"} {
		t.Run(mode, func(t *testing.T) { test(t, mode) })
	}
}

func TestClaudeExpiredLoginFailsPromptlyWithClassifiedError(t *testing.T) {
	shortenClaudeWaits(t, time.Minute)
	retries := ""
	for attempt := 1; attempt <= 3; attempt++ {
		retries += claudeFixtureLines(strings.Replace(claudeFixtureAuthRetry, "%d", string(rune('0'+attempt)), 1)) + "sleep 0.05\n"
	}
	for _, scenario := range []struct{ name, body string }{
		{"result-then-exit-nonzero", claudeFixtureLines(claudeFixtureInit, claudeFixtureRequesting, claudeFixtureAuthMessage, claudeFixtureAuthResult) + "exit 1\n"},
		{"result-then-exit-zero", claudeFixtureLines(claudeFixtureInit, claudeFixtureAuthMessage, claudeFixtureAuthResult) + "exit 0\n"},
		{"result-then-stays-alive", claudeFixtureLines(claudeFixtureInit, claudeFixtureAuthMessage, claudeFixtureAuthResult) + "sleep 30\n"},
		{"error-message-without-result-stays-alive", claudeFixtureLines(claudeFixtureInit, claudeFixtureRequesting, claudeFixtureAuthMessage) + "sleep 30\n"},
		{"repeated-auth-retries-stay-alive", claudeFixtureLines(claudeFixtureInit, claudeFixtureRequesting) + retries + "sleep 30\n"},
		{"stderr-only-then-exit", "printf '%s\\n' 'Invalid API key · Please run /login' >&2\nexit 1\n"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			claudeFailureModes(t, func(t *testing.T, mode string) {
				outcome := runClaudeFailureFixture(t, mode, scenario.body, nil)
				requireClaudeFailedFast(t, outcome, "PROVIDER_AUTH_FAILED", "sign in")
				if strings.Count(outcome.calls, "\n") != 1 {
					t.Fatalf("auth failure was retried: %q", outcome.calls)
				}
			})
		})
	}
}

func TestClaudeExpiredLoginNamesTheProviderMessage(t *testing.T) {
	shortenClaudeWaits(t, time.Minute)
	claudeFailureModes(t, func(t *testing.T, mode string) {
		outcome := runClaudeFailureFixture(t, mode, claudeFixtureLines(claudeFixtureInit, claudeFixtureAuthMessage, claudeFixtureAuthResult)+"exit 1\n", nil)
		requireClaudeFailedFast(t, outcome, "PROVIDER_AUTH_FAILED", "OAuth session expired and could not be refreshed")
	})
}

func TestClaudeSilentStartupFailsPromptly(t *testing.T) {
	shortenClaudeWaits(t, 2*time.Second)
	claudeFailureModes(t, func(t *testing.T, mode string) {
		outcome := runClaudeFailureFixture(t, mode, "sleep 30\n", nil)
		requireClaudeFailedFast(t, outcome, "PROVIDER_UNRESPONSIVE", "no output")
	})
}

func TestClaudeQuietWorkAfterStartupIsNotInterrupted(t *testing.T) {
	shortenClaudeWaits(t, 2*time.Second)
	claudeFailureModes(t, func(t *testing.T, mode string) {
		outcome := runClaudeFailureFixture(t, mode, claudeFixtureLines(claudeFixtureInit)+"sleep 3\n"+claudeFixtureLines(claudeFixtureSuccessFinal), nil)
		if outcome.err != nil || outcome.result.Status != "success" {
			t.Fatalf("quiet work was interrupted: %v %#v %#v", outcome.err, outcome.result, outcome.result.Error)
		}
	})
}

func TestClaudeTransientAuthRetryThenSuccessIsNotInterrupted(t *testing.T) {
	shortenClaudeWaits(t, time.Minute)
	claudeFailureModes(t, func(t *testing.T, mode string) {
		body := claudeFixtureLines(claudeFixtureInit, strings.Replace(claudeFixtureAuthRetry, "%d", "1", 1)) + "sleep 0.3\n" + claudeFixtureLines(claudeFixtureSuccessFinal)
		outcome := runClaudeFailureFixture(t, mode, body, nil)
		if outcome.err != nil || outcome.result.Status != "success" {
			t.Fatalf("a refreshed login was rejected: %v %#v %#v", outcome.err, outcome.result, outcome.result.Error)
		}
	})
}

func TestClaudeRateLimitRetriesAreNotAuthFailures(t *testing.T) {
	shortenClaudeWaits(t, time.Minute)
	claudeFailureModes(t, func(t *testing.T, mode string) {
		retries := ""
		for attempt := 1; attempt <= 3; attempt++ {
			retries += claudeFixtureLines(strings.Replace(claudeFixtureLimitRetry, "%d", string(rune('0'+attempt)), 1)) + "sleep 0.05\n"
		}
		outcome := runClaudeFailureFixture(t, mode, claudeFixtureLines(claudeFixtureInit)+retries+claudeFixtureLines(claudeFixtureSuccessFinal), nil)
		if outcome.err != nil || outcome.result.Status != "success" {
			t.Fatalf("rate-limit retries were misclassified: %v %#v %#v", outcome.err, outcome.result, outcome.result.Error)
		}
	})
}

func TestClaudeOtherErrorResultsKeepProviderText(t *testing.T) {
	shortenClaudeWaits(t, time.Minute)
	for _, scenario := range []struct{ name, body string }{
		{"exit-nonzero", claudeFixtureLines(claudeFixtureInit, claudeFixtureModelResult) + "exit 1\n"},
		{"stays-alive", claudeFixtureLines(claudeFixtureInit, claudeFixtureModelResult) + "sleep 30\n"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			claudeFailureModes(t, func(t *testing.T, mode string) {
				outcome := runClaudeFailureFixture(t, mode, scenario.body, nil)
				if outcome.elapsed > 8*time.Second {
					t.Fatalf("waited %s after an error result", outcome.elapsed)
				}
				if outcome.err == nil || !strings.Contains(outcome.err.Error(), "model not found") {
					t.Fatalf("provider text was dropped: %v %#v", outcome.err, outcome.result)
				}
				if outcome.result.Error != nil && outcome.result.Error.Code == "PROVIDER_AUTH_FAILED" {
					t.Fatal("non-auth error classified as auth failure")
				}
			})
		})
	}
}

func TestClaudeAuthFailureKeepsSavedSessionMapping(t *testing.T) {
	shortenClaudeWaits(t, time.Minute)
	claudeFailureModes(t, func(t *testing.T, mode string) {
		const saved = "33333333-3333-4333-8333-333333333333"
		var store, workspace, namespace, key, clientMode string
		outcome := runClaudeFailureFixture(t, mode, claudeFixtureLines(claudeFixtureInit, claudeFixtureAuthMessage, claudeFixtureAuthResult)+"exit 1\n", func(config *ProviderConfig, run RunContext) {
			prepared, closeSession, err := prepareIsolatedSession(context.Background(), *config, run)
			if err != nil {
				t.Fatal(err)
			}
			namespace, key = providerSessionScope("claude", prepared, run)
			workspace = prepared.Workspace
			store = sessionStorePath(prepared.SessionStore, "claude", workspace)
			clientMode = providerSessionClientMode(prepared)
			if err := saveSessionForClientMode(store, namespace, workspace, key, saved, clientMode, 1); err != nil {
				t.Fatal(err)
			}
			if err := closeSession(); err != nil {
				t.Fatal(err)
			}
		})
		requireClaudeFailedFast(t, outcome, "PROVIDER_AUTH_FAILED")
		if !strings.Contains(outcome.calls, "--resume "+saved) {
			t.Fatalf("saved session was not resumed: %q", outcome.calls)
		}
		if id, _, _ := loadSessionForClientMode(store, namespace, workspace, key, clientMode); id != saved {
			t.Fatalf("auth failure changed the saved session mapping: %q", id)
		}
	})
}
