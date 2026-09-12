// A deterministic protocol peer for native sandbox isolation acceptance. It does real
// filesystem/process probes and synthetic-key inspection, never model calls or
// real credential loading.
package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	provider := filepath.Base(os.Args[0])
	if hasArgument("credential-child") {
		credentialChild()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "child" {
		for {
			_ = os.WriteFile("heartbeat", []byte(time.Now().String()), 0o600)
			time.Sleep(50 * time.Millisecond)
		}
	}
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
	home := os.Getenv("CODEX_HOME")
	if provider == "claude" {
		home = os.Getenv("CLAUDE_CONFIG_DIR")
	}
	path := filepath.Join(home, "native-id")
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
		Denied      []string
		Host        string
		Memory      string
		Wait        bool
		Credentials bool
	}
	if match := regexp.MustCompile(`fixture_spec=([A-Za-z0-9+/=]+)`).FindStringSubmatch(prompt); len(match) == 2 {
		raw, _ := base64.StdEncoding.DecodeString(match[1])
		_ = json.Unmarshal(raw, &spec)
	}
	if strings.Contains(prompt, "fixture:hang") {
		child := exec.Command(os.Args[0], "child")
		if err := child.Start(); err != nil {
			panic(err)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	previous, _ := os.ReadFile("memory")
	if strings.Contains(prompt, "fixture:remember") {
		_ = os.WriteFile("memory", []byte("conversation-private-canary"), 0o600)
	}
	if spec.Memory != "" {
		if err := os.WriteFile("memory", []byte(spec.Memory), 0o600); err != nil {
			panic(err)
		}
	}
	if spec.Wait {
		// The trusted test driver releases each sandbox independently. Requiring
		// both ready files before either release proves actual overlapping Runs.
		if err := os.WriteFile("ready", []byte(id), 0o600); err != nil {
			panic(err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := os.Stat("release"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				panic("sandbox barrier was not released")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	report := map[string]any{"id": id, "previous": string(previous), "uid": os.Geteuid(), "home": os.Getenv("HOME"), "codex_home": os.Getenv("CODEX_HOME"), "claude_home": os.Getenv("CLAUDE_CONFIG_DIR")}
	if spec.Credentials {
		child := exec.Command(os.Args[0], "credential-child")
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			if key != "CODEX_API_KEY" && key != "ANTHROPIC_API_KEY" {
				child.Env = append(child.Env, entry)
			}
		}
		raw, err := child.Output()
		if err != nil {
			panic("credential child failed to execute")
		}
		var probe map[string]any
		if json.Unmarshal(raw, &probe) != nil {
			panic("credential child returned invalid evidence")
		}
		report["credential_probe"] = probe
	}
	blocked := make(map[string]bool)
	for _, path := range spec.Denied {
		if path == "" {
			continue
		}
		_, err := os.ReadFile(path)
		blocked[path] = err != nil
	}
	// A symlink cannot cross the mount boundary into a real host directory.
	if target := spec.Host; target != "" {
		_ = os.Remove("escape")
		_ = os.Symlink(target, "escape")
		_, err := os.ReadFile("escape")
		report["symlink_blocked"] = err != nil
	}
	_, socketErr := os.Stat("/var/run/docker.sock")
	report["docker_socket_blocked"] = socketErr != nil
	writeErr := os.WriteFile("/host-write-probe", []byte("no"), 0o600)
	report["root_write_blocked"] = writeErr != nil
	report["agent_token"] = os.Getenv("OPENLINKER_AGENT_TOKEN")
	report["loader_env"] = os.Getenv("NODE_OPTIONS")
	report["bare"] = false
	for _, arg := range os.Args {
		if arg == "--bare" {
			report["bare"] = true
		}
	}
	report["blocked"] = blocked
	interfaces, _ := net.Interfaces()
	nonLoopback := 0
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback == 0 {
			nonLoopback++
		}
	}
	report["non_loopback_interfaces"] = nonLoopback
	child := exec.Command(os.Args[0], "--version")
	report["child_process_works"] = child.Run() == nil
	raw, _ := json.Marshal(report)
	return string(raw)
}

func hasArgument(want string) bool {
	for _, v := range os.Args[1:] {
		if v == want {
			return true
		}
	}
	return false
}

// Only called by the synthetic protocol fixture. Never print an environment or
// credential: return a digest of any parent value through the normal Run output.
func credentialChild() {
	report := map[string]any{"child_has_key": os.Getenv("CODEX_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != ""}
	var raw []byte
	var err error
	var entries []string
	if runtime.GOOS == "linux" {
		_, selfErr := os.ReadFile("/proc/self/environ")
		report["positive_control"] = selfErr == nil
		report["method"] = "proc-parent-environ"
		raw, err = os.ReadFile("/proc/" + strconv.Itoa(os.Getppid()) + "/environ")
		entries = strings.Split(string(raw), "\x00")
	} else {
		// Listing format keywords proves ps can launch without requiring the
		// very process-information permission this probe is investigating.
		keywords, keywordErr := exec.Command("/bin/ps", "-L").Output()
		report["positive_control"] = keywordErr == nil && strings.Contains(string(keywords), "command")
		info, statErr := os.Stat("/bin/ps")
		report["probe_executable_present"] = statErr == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
		report["method"] = "ps-parent-eww"
		raw, err = exec.Command("/bin/ps", "eww", "-p", strconv.Itoa(os.Getppid()), "-o", "command=").Output()
		report["exec_permission_denied"] = errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES)
		entries = strings.Fields(string(raw))
	}
	report["read_permitted"] = err == nil
	digest := ""
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (key == "CODEX_API_KEY" || key == "ANTHROPIC_API_KEY") && value != "" {
			sum := sha256.Sum256([]byte(value))
			digest = hex.EncodeToString(sum[:])
		}
	}
	report["parent_key_sha256"] = digest
	_ = json.NewEncoder(os.Stdout).Encode(report)
}
