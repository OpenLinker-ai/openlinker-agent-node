package adapters

import (
	"context"
	"encoding/json"
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

var imagePathPattern = regexp.MustCompile(`\.openlinker-inputs/[0-9a-f]{24}/image-1\.png`)

// TestNativeCodexImageInputInstalledClient drives the installed Codex client
// under native isolation against a synthetic Responses endpoint. It proves the
// client sends the image to the model and its sandboxed tool can read the
// file, then resumes the session after the file was removed.
func TestNativeCodexImageInputInstalledClient(t *testing.T) {
	bin := os.Getenv("OPENLINKER_TEST_NATIVE_CODEX_BIN")
	if bin == "" {
		t.Skip("set OPENLINKER_TEST_NATIVE_CODEX_BIN for real client image acceptance")
	}
	useCodexImageServer(t)
	c := isolationConfig(t, "codex")
	c.Bin, c.Model, c.Timeout, c.CodexImageInput = bin, "gpt-5.4", 60*time.Second, true

	var mu sync.Mutex
	var bodies []string
	turn := 0
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
		id := fmt.Sprintf("fixture_image_exec_%d", len(bodies))
		item := map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "phase": "final_answer", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": fmt.Sprintf("image turn %d complete", turn), "annotations": []any{}}}}
		if path := imagePathPattern.FindString(body); turn == 0 && path != "" && !strings.Contains(body, "fixture_image_exec_") {
			command := "printf 'IMAGE_BYTES:%s\\n' \"$(wc -c < " + path + " | tr -d ' ')\"; head -c 4 " + path + " | od -An -c"
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
	write(filepath.Join(conf, "auth.json"), jsonObject(map[string]any{"OPENAI_API_KEY": "synthetic-image-acceptance-key"}))
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
	inputs := []any{
		map[string]any{"image_url": "https://example.com/photo", "question": "Is this image AI generated?"},
		map[string]any{"question": "What did you conclude about the earlier image?"},
	}
	for i, input := range inputs {
		mu.Lock()
		turn = i
		mu.Unlock()
		run := isolationRun("image-session")
		run.RunID = fmt.Sprint("run-image-", i)
		run.Conversation.CurrentRunID = run.RunID
		run.Input = input
		result, err := provider.Run(ctx, run)
		if err != nil || result.Error != nil {
			t.Fatalf("turn %d: %v %+v", i, err, result.Error)
		}
		output := result.Output.(map[string]any)
		if output["summary"] != fmt.Sprintf("image turn %d complete", i) || output["session_isolation"] != "native" {
			t.Fatalf("turn %d output %v", i, output)
		}
		if i == 1 && output["codex_session_resumed"] != true {
			t.Fatalf("second turn did not resume: %v", output)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) < 3 {
		t.Fatalf("expected tool round trip and resumed turn, got %d requests", len(bodies))
	}
	first := bodies[0]
	if !strings.Contains(first, `"type":"input_image"`) || !strings.Contains(first, `data:image/`) {
		t.Fatal("first model request did not carry the image")
	}
	if !imagePathPattern.MatchString(first) {
		t.Fatal("prompt did not name the image file")
	}
	var toolOutput string
	for _, body := range bodies[1:] {
		if strings.Contains(body, "IMAGE_BYTES:") {
			toolOutput = body
		}
	}
	if !strings.Contains(toolOutput, "IMAGE_BYTES:"+strconv.Itoa(len(codexTestPNG))) {
		index := strings.Index(toolOutput, "function_call_output")
		t.Fatalf("sandboxed tool could not read the image: %s", toolOutput[max(0, index):min(len(toolOutput), index+800)])
	}
	resumed := bodies[len(bodies)-1]
	t.Logf("resumed turn request carries prior image: %t", strings.Contains(resumed, `"type":"input_image"`))
	var decoded map[string]any
	if json.Unmarshal([]byte(resumed), &decoded) != nil {
		t.Fatal("resumed request is not JSON")
	}
	prepared, closeSession, err := prepareIsolatedSession(context.Background(), c, isolationRun("image-session"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeSession()
	if entries, err := os.ReadDir(filepath.Join(prepared.Workspace, imageinput.DirectoryName)); err != nil || len(entries) != 0 {
		t.Fatalf("image files remain in the session workspace: %v %v", entries, err)
	}
}
