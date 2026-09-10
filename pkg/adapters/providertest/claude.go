package providertest

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ClaudeLargeTranscriptKeepsOnlyResult exercises the consumer's real Claude.Run
// with a fake native process whose stdout and stderr each exceed 16 MiB.
func ClaudeLargeTranscriptKeepsOnlyResult(t *testing.T, run func(context.Context, string, string) (string, error)) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "provider")
	script := "#!/bin/sh\nset -eu\nexport OPENLINKER_CLAUDE_OUTPUT_FIXTURE=1\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run=TestClaudeOutputFixtureProcess\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := run(context.Background(), bin, dir)
	if err != nil {
		t.Fatal(err)
	}
	if result != "finished long task" {
		t.Fatal(result)
	}
}

func ClaudeOutputFixtureProcess() {
	if os.Getenv("OPENLINKER_CLAUDE_OUTPUT_FIXTURE") != "1" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	line := []byte(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"` + strings.Repeat("x", 2048) + `"}}}` + "\n")
	for i := 0; i < 8192; i++ { // > 16 MiB on each pipe, beyond the old lifetime cap.
		_, _ = os.Stdout.Write(line)
		_, _ = os.Stderr.Write(line)
	}
	_, _ = io.WriteString(os.Stdout, `{"type":"result","result":"finished long task","session_id":"native"}`)
	os.Exit(0)
}

// ClaudeStream exposes only the behavior and retained-record size asserted here.
type ClaudeStream struct {
	io.Writer
	Result       func() (string, error)
	PendingBytes func() int
}

func ClaudeStreamBoundsRecordsAndRejectsMalformedOrMissingResult(t *testing.T, maxBytes int, newStream func(cancel func()) ClaudeStream) {
	t.Helper()
	for _, test := range []struct {
		name, output string
		ok           bool
	}{
		{"fragmented", "{\"type\":\"assistant\"}\n{\"type\":\"result\",\"result\":\"answer\"}", true},
		{"oversize-record", strings.Repeat("x", maxBytes+1), false},
		{"malformed-after-result", "{\"type\":\"result\",\"result\":\"answer\"}\noops\n", false},
		{"missing-result", "{\"type\":\"assistant\"}\n", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			canceled := false
			s := newStream(func() { canceled = true })
			for p := test.output; len(p) > 0; {
				n := min(777, len(p))
				if _, err := s.Write([]byte(p[:n])); err != nil {
					break
				}
				p = p[n:]
			}
			r, err := s.Result()
			if (err == nil) != test.ok {
				t.Fatalf("unexpected result error: %v", err)
			}
			if test.ok && (r != "answer" || canceled) {
				t.Fatal("lost result or canceled valid stream")
			}
			if (test.name == "oversize-record" || test.name == "malformed-after-result") && !canceled {
				t.Fatal("invalid stream did not stop process")
			}
			if s.PendingBytes() > maxBytes {
				t.Fatal("unbounded record")
			}
		})
	}
}
