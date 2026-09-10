package adapters

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestClaudeSessionEvidenceFromSuccessfulProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake provider uses a POSIX shell")
	}
	const previous = "synthetic-previous-session"
	const fresh = "synthetic-fresh-session"
	for _, test := range []struct {
		name            string
		storedID        string
		responseID      string
		omitResponseID  bool
		reuse           bool
		missingSession  bool
		wantResumeCalls []string
		wantRequestID   string
	}{
		{name: "new", responseID: fresh, reuse: true, wantResumeCalls: []string{""}},
		{name: "resume", storedID: previous, responseID: previous, reuse: true, wantResumeCalls: []string{previous}, wantRequestID: previous},
		{name: "different-response", storedID: previous, responseID: fresh, reuse: true, wantResumeCalls: []string{previous}, wantRequestID: previous},
		{name: "missing-response-id", storedID: previous, omitResponseID: true, reuse: true, wantResumeCalls: []string{previous}, wantRequestID: previous},
		{name: "empty-response-id", storedID: previous, reuse: true, wantResumeCalls: []string{previous}, wantRequestID: previous},
		{name: "missing-both-ids", omitResponseID: true, reuse: true, wantResumeCalls: []string{""}},
		{name: "response-id-exact-utf8", responseID: " \t合成-session\n", reuse: true, wantResumeCalls: []string{""}},
		{name: "resume-hashes-actual-trimmed-argument", storedID: " \t合成-session ", responseID: fresh, reuse: true, wantResumeCalls: []string{"合成-session"}, wantRequestID: "合成-session"},
		{name: "missing-session-recovery", storedID: previous, responseID: fresh, reuse: true, missingSession: true, wantResumeCalls: []string{previous, ""}},
		{name: "recovery-missing-response-id", storedID: previous, omitResponseID: true, reuse: true, missingSession: true, wantResumeCalls: []string{previous, ""}},
		{name: "default-false-existing-map", storedID: previous, responseID: fresh, wantResumeCalls: []string{""}},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := map[string]any{"type": "result", "subtype": "success", "result": "synthetic answer"}
			if !test.omitResponseID {
				message["session_id"] = test.responseID
			}
			response, err := json.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			body := "printf '%s\\0' \"$@\" >> args\nprintf '\\n' >> args\ncat >/dev/null\n"
			if test.missingSession {
				body += `case "$*" in
*--resume*) printf '%s\n' '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"session not found","session_id":"synthetic-failed-session"}'; exit 1 ;;
esac
`
			}
			body += "printf '%s\\n' '" + string(response) + "'\n"
			bin, dir := reviewFakeCLI(t, body)
			store := filepath.Join(dir, "sessions.json")
			if test.storedID != "" {
				if err := saveSessionForClientMode(store, "claude", dir, "synthetic-context", test.storedID, "standard", 1); err != nil {
					t.Fatal(err)
				}
				if record := readSessionStore(store).Sessions[sessionStoreKey("claude", dir, "synthetic-context")]; record.SessionID != strings.TrimSpace(test.storedID) {
					t.Fatal("existing mapping precondition was not established")
				}
			}
			before, _ := os.ReadFile(store)
			provider, err := NewProvider(ProviderConfig{
				Provider: "claude", Bin: bin, Workspace: dir, Timeout: 5 * time.Second,
				SessionStore: store, SessionReuse: test.reuse, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + dir},
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := provider.Run(context.Background(), RunContext{
				RunID: "synthetic-run", Input: "synthetic task",
				Conversation: &ConversationContext{SessionKey: "synthetic-context"},
			})
			if err != nil {
				t.Fatal(err)
			}
			output, ok := result.Output.(map[string]any)
			if !ok || result.Status != "success" {
				t.Fatalf("expected successful provider result, got status %q", result.Status)
			}
			assertClaudeSessionHash(t, output, "claude_resume_session_id_sha256", test.wantRequestID)
			assertClaudeSessionHash(t, output, "claude_session_id_sha256", test.responseID)
			if got := claudeFixtureResumeArguments(t, filepath.Join(dir, "args")); !reflect.DeepEqual(got, test.wantResumeCalls) {
				t.Fatalf("actual resume arguments = %q, want %q", got, test.wantResumeCalls)
			}
			if test.reuse {
				if output["claude_session_reuse"] != true || output["claude_session_resumed"] != (test.storedID != "") || output["claude_session_recovered"] != test.missingSession {
					t.Fatal("existing session evidence semantics changed")
				}
			} else {
				if _, exists := output["claude_session_reuse"]; exists {
					t.Fatal("default false must retain its existing absent reuse field")
				}
				after, err := os.ReadFile(store)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("disabled reuse changed the existing mapping")
				}
			}
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			for _, privateID := range []string{previous, fresh, "合成-session", "synthetic-failed-session"} {
				if bytes.Contains(raw, []byte(privateID)) {
					t.Fatal("result exposed an unhashed provider session ID")
				}
			}
		})
	}
}

func TestClaudeSessionEvidenceAbsentWhenProcessDoesNotSucceed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake provider uses a POSIX shell")
	}
	for _, scenario := range []string{"exit-failure", "structured-failure", "missing-result", "missing-summary", "not-executed", "canceled-before-exec"} {
		t.Run(scenario, func(t *testing.T) {
			response := `{"type":"result","subtype":"success","result":"synthetic answer","session_id":"synthetic-response-session"}`
			if scenario == "structured-failure" {
				response = `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"synthetic failure","session_id":"synthetic-response-session"}`
			} else if scenario == "missing-result" {
				response = `{"type":"assistant","session_id":"synthetic-response-session"}`
			} else if scenario == "missing-summary" {
				response = `{"type":"result","subtype":"success","result":"","session_id":"synthetic-response-session"}`
			}
			body := "printf 'started' > executed\ncat >/dev/null\nprintf '%s\\n' '" + response + "'\n"
			if scenario == "exit-failure" {
				body += "exit 1\n"
			}
			bin, dir := reviewFakeCLI(t, body)
			if scenario == "not-executed" {
				bin = filepath.Join(dir, "missing-provider")
			}
			store := filepath.Join(dir, "sessions.json")
			if err := saveSessionForClientMode(store, "claude", dir, "synthetic-context", "synthetic-previous-session", "standard", 1); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "canceled-before-exec" {
				cancel()
			}
			result, err := (ClaudeProvider{Config: ProviderConfig{
				Bin: bin, Workspace: dir, Timeout: 5 * time.Second, SessionReuse: true, SessionStore: store,
				Env: []string{"PATH=/usr/bin:/bin", "HOME=" + dir},
			}}).Run(ctx, RunContext{Conversation: &ConversationContext{SessionKey: "synthetic-context"}})
			if err == nil || result.Output != nil || result.Status != "" {
				t.Fatal("unsuccessful process manufactured successful session evidence")
			}
			_, statErr := os.Stat(filepath.Join(dir, "executed"))
			wantExecution := scenario != "not-executed" && scenario != "canceled-before-exec"
			if (statErr == nil) != wantExecution {
				t.Fatalf("process execution = %t, want %t", statErr == nil, wantExecution)
			}
		})
	}
}

func assertClaudeSessionHash(t *testing.T, output map[string]any, field, id string) {
	t.Helper()
	value, exists := output[field]
	if id == "" {
		if exists {
			t.Fatalf("%s must be absent without an actual ID", field)
		}
		return
	}
	if want := fmt.Sprintf("%x", sha256.Sum256([]byte(id))); value != want {
		t.Fatalf("%s = %v, want SHA256 of the exact process ID", field, value)
	}
}

func claudeFixtureResumeArguments(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var resumes []string
	for _, invocation := range bytes.Split(bytes.TrimSuffix(raw, []byte("\n")), []byte("\n")) {
		args := strings.Split(string(bytes.TrimSuffix(invocation, []byte{0})), "\x00")
		resume := ""
		for i, arg := range args {
			if arg == "--resume" {
				if i+1 == len(args) || resume != "" {
					t.Fatal("invalid fixture resume arguments")
				}
				resume = args[i+1]
			}
		}
		resumes = append(resumes, resume)
	}
	return resumes
}
