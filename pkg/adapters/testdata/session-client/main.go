// Trusted deterministic protocol peer for session routing/cancellation tests.
// This fixture does not establish a sandbox boundary.
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

func main() {
	provider := filepath.Base(os.Args[0])
	if hasArgument("--version") {
		if provider == "codex" {
			fmt.Println("codex-cli 0.153.0")
		} else {
			fmt.Println("2.1.259 (Claude Code)")
		}
		return
	}
	for _, arg := range os.Args[1:] {
		if arg == "--help" {
			fmt.Println("app-server generate-json-schema --listen --config --disable --safe-mode --bare --no-chrome --disable-slash-commands --permission-mode --resume stream-json --verbose --include-partial-messages --strict-mcp-config")
			return
		}
	}
	if provider == "codex" {
		codex()
		return
	}
	prompt, _ := io.ReadAll(os.Stdin)
	id := sessionID("claude", argument("--resume"))
	answer := probe(string(prompt), id)
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "result", "subtype": "success", "result": answer, "session_id": id})
}

func argument(key string) string {
	for i, arg := range os.Args {
		if arg == key && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return ""
}

func sessionID(provider, resume string) string {
	path := "native-id"
	if resume != "" {
		stored, _ := os.ReadFile(path)
		if string(stored) != resume {
			panic("wrong native session resumed")
		}
		return resume
	}
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	id := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(id), 0o600); err != nil {
		panic(err)
	}
	return id
}

func codex() {
	// Mirror only the official client's helper-alias layout for Linux bridge
	// tests; these peers do not prove OS sandbox enforcement.
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		home = filepath.Join(os.Getenv("HOME"), ".codex")
	}
	root := filepath.Join(home, "tmp", "arg0")
	if err := os.MkdirAll(root, 0700); err != nil {
		panic(err)
	}
	helper, err := os.MkdirTemp(root, "codex-arg0")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(helper)
	self, _ := os.Executable()
	if err := os.Symlink(self, filepath.Join(helper, "codex-linux-sandbox")); err != nil {
		panic(err)
	}
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	id := ""
	for scanner.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ThreadID  string `json:"threadId"`
				Ephemeral bool   `json:"ephemeral"`
				CWD       string `json:"cwd"`
				Input     []struct {
					Text string `json:"text"`
				} `json:"input"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			panic("bad JSON")
		}
		reply := func(result any) { _ = encoder.Encode(map[string]any{"id": msg.ID, "result": result}) }
		switch msg.Method {
		case "initialize":
			reply(map[string]any{"userAgent": "isolation-fixture"})
		case "initialized":
		case "thread/start", "thread/resume":
			cwd, _ := os.Getwd()
			if msg.Params.CWD != cwd || msg.Params.Ephemeral {
				panic("nonpersistent or wrong cwd")
			}
			id = sessionID("codex", msg.Params.ThreadID)
			reply(map[string]any{"thread": map[string]any{"id": id}})
		case "turn/start":
			reply(map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}})
			answer := probe(msg.Params.Input[0].Text, id)
			_ = encoder.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": id, "turn": map[string]any{"id": "turn-1", "status": "completed", "items": []any{map[string]any{"id": "answer", "type": "agentMessage", "phase": "final_answer", "text": answer}}}}})
		}
	}
}

func probe(prompt, id string) string {
	var spec struct {
		Memory string
		Wait   bool
	}
	if match := regexp.MustCompile(`fixture_spec=([A-Za-z0-9+/=]+)`).FindStringSubmatch(prompt); len(match) == 2 {
		raw, _ := base64.StdEncoding.DecodeString(match[1])
		_ = json.Unmarshal(raw, &spec)
	}
	previous, _ := os.ReadFile("memory")
	if spec.Memory != "" {
		if err := os.WriteFile("memory", []byte(spec.Memory), 0600); err != nil {
			panic(err)
		}
	}
	if spec.Wait {
		if err := os.WriteFile("ready", []byte(id), 0600); err != nil {
			panic(err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := os.Stat("release"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				panic("routing barrier timed out")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	cwd, _ := os.Getwd()
	raw, _ := json.Marshal(map[string]any{"id": id, "workspace": cwd, "previous": string(previous), "home": os.Getenv("HOME")})
	return string(raw)
}

func hasArgument(want string) bool {
	for _, arg := range os.Args[1:] {
		if arg == want {
			return true
		}
	}
	return false
}
