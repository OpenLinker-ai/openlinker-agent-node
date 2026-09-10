package adapters

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClaudeLargeTranscriptKeepsOnlyResult(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin, dir := reviewFakeCLI(t, "export OPENLINKER_CLAUDE_OUTPUT_FIXTURE=1\nexec '"+strings.ReplaceAll(executable, "'", "'\\''")+"' -test.run=TestClaudeOutputFixtureProcess\n")
	result, err := (ClaudeProvider{Config: ProviderConfig{Bin: bin, Workspace: dir, Timeout: 15 * time.Second}}).Run(context.Background(), RunContext{Input: "long task"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output.(map[string]any)["summary"] != "finished long task" {
		t.Fatal(result)
	}
}

func TestClaudeOutputFixtureProcess(t *testing.T) {
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

func TestClaudeStreamBoundsRecordsAndRejectsMalformedOrMissingResult(t *testing.T) {
	for _, test := range []struct {
		name, output string
		ok           bool
	}{
		{"fragmented", "{\"type\":\"assistant\"}\n{\"type\":\"result\",\"result\":\"answer\"}", true},
		{"oversize-record", strings.Repeat("x", maxProviderOutputBytes+1), false},
		{"malformed-after-result", "{\"type\":\"result\",\"result\":\"answer\"}\noops\n", false},
		{"missing-result", "{\"type\":\"assistant\"}\n", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			canceled := false
			s := newClaudeResultStream(func() { canceled = true }, nil)
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
			if test.ok && (r.Result != "answer" || canceled) {
				t.Fatal("lost result or canceled valid stream")
			}
			if (test.name == "oversize-record" || test.name == "malformed-after-result") && !canceled {
				t.Fatal("invalid stream did not stop process")
			}
			if len(s.pending) > maxProviderOutputBytes {
				t.Fatal("unbounded record")
			}
		})
	}
}

func TestDiagnosticTailRetainsLatestBytes(t *testing.T) {
	tail := &outputTail{}
	_, _ = tail.Write([]byte(strings.Repeat("old", maxProviderDiagnosticBytes)))
	_, _ = tail.Write([]byte("latest diagnostic"))
	if len(tail.data) != maxProviderDiagnosticBytes || !strings.HasSuffix(tail.String(), "latest diagnostic") {
		t.Fatal("diagnostic tail lost")
	}
}
