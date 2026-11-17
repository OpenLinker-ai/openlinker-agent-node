package adapters

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/imageinput"
)

var filePathPattern = regexp.MustCompile(`\.openlinker-inputs/[0-9a-f]{24}/file-1\.pdf`)

// TestNativeCodexFileInputInstalledClient drives the installed Codex client
// under native isolation against a synthetic Responses endpoint. It proves
// the delivered file is named in the prompt, is not sent as an image, and a
// sandboxed tool can read it (without execute permission) during the turn.
func TestNativeCodexFileInputInstalledClient(t *testing.T) {
	bin := os.Getenv("OPENLINKER_TEST_NATIVE_CODEX_BIN")
	if bin == "" {
		t.Skip("set OPENLINKER_TEST_NATIVE_CODEX_BIN for real client file acceptance")
	}
	useCodexFileServer(t)
	c := isolationConfig(t, "codex")
	c.Bin, c.Model, c.Timeout, c.CodexFileInput = bin, "gpt-5.4", 60*time.Second, true

	var mu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"models":[]}`)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		bodies = append(bodies, string(raw))
		body := string(raw)
		id := fmt.Sprintf("fixture_file_exec_%d", len(bodies))
		item := map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "phase": "final_answer", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": "file turn complete", "annotations": []any{}}}}
		if path := filePathPattern.FindString(body); path != "" && !strings.Contains(body, "fixture_file_exec_") {
			command := "printf 'FILE_BYTES:%s\\n' \"$(wc -c < " + path + " | tr -d ' ')\"; head -n 2 " + path +
				"; if [ -x " + path + " ]; then echo FILE_EXECUTABLE; else echo FILE_NOT_EXECUTABLE; fi"
			item = map[string]any{"id": id, "type": "function_call", "call_id": id, "name": "exec_command",
				"arguments": jsonObject(map[string]any{"cmd": command, "yield_time_ms": 1000, "max_output_tokens": 2000})}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(kind string, v any) { fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, jsonObject(v)) }
		response := fmt.Sprint("response_", len(bodies))
		emit("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": response}})
		emit("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
		emit("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": response, "status": "completed", "output": []any{item},
			"usage": map[string]any{"input_tokens": 20, "output_tokens": 10, "total_tokens": 30}}})
	}))
	defer server.Close()

	home := t.TempDir()
	conf := filepath.Join(home, ".codex")
	if err := os.Mkdir(conf, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(conf, "auth.json"), jsonObject(map[string]any{"OPENAI_API_KEY": "synthetic-file-acceptance-key"}))
	write(filepath.Join(conf, "config.toml"), "model_provider=\"fixture\"\n[model_providers.fixture]\nname=\"fixture\"\nbase_url="+jsonString(server.URL)+"\nwire_api=\"responses\"\nrequires_openai_auth=true\nsupports_websockets=false\n")
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CODEX_HOME=" + conf, "NO_PROXY=127.0.0.1,localhost,::1", "HTTP_PROXY=", "HTTPS_PROXY=", "ALL_PROXY="}
	lockPath, err := hostAuthLockPath(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	write(lockPath, "")
	provider, err := NewProvider(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := isolationRun("file-session")
	run.RunID = "run-file-0"
	run.Conversation.CurrentRunID = run.RunID
	run.Input = map[string]any{"contract_file_url": "https://example.com/report", "question": "What are the quarterly figures?"}
	result, err := provider.Run(ctx, run)
	if err != nil || result.Error != nil {
		t.Fatalf("run: %v %+v", err, result.Error)
	}
	if output := result.Output.(map[string]any); output["summary"] != "file turn complete" || output["session_isolation"] != "native" {
		t.Fatalf("output %v", output)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) < 2 {
		t.Fatalf("expected a tool round trip, got %d requests", len(bodies))
	}
	first := bodies[0]
	if !filePathPattern.MatchString(first) || !strings.Contains(first, `original name \"Q3 report.PDF\"`) {
		t.Fatal("prompt did not describe the file")
	}
	if strings.Contains(first, `"type":"input_image"`) || strings.Contains(first, "quarterly figures: 42") {
		t.Fatal("file was sent as an image or inlined into the prompt")
	}
	var toolOutput string
	for _, body := range bodies[1:] {
		if strings.Contains(body, "FILE_BYTES:") {
			toolOutput = body
		}
	}
	for _, want := range []string{"FILE_BYTES:" + strconv.Itoa(len(codexTestDocument)), "quarterly figures: 42", "FILE_NOT_EXECUTABLE"} {
		if !strings.Contains(toolOutput, want) {
			index := strings.Index(toolOutput, "function_call_output")
			t.Fatalf("sandboxed tool output misses %q: %s", want, toolOutput[max(0, index):min(len(toolOutput), index+800)])
		}
	}
	prepared, closeSession, err := prepareIsolatedSession(context.Background(), c, isolationRun("file-session"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeSession()
	if entries, err := os.ReadDir(filepath.Join(prepared.Workspace, imageinput.DirectoryName)); err != nil || len(entries) != 0 {
		t.Fatalf("input files remain in the session workspace: %v %v", entries, err)
	}
}
