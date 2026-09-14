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
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// The opt-in case is an audit of client permission checks, not an OS-isolation
// guarantee. In particular a host-preseeded hard link is already a grant of an
// inode. Keep it separate from links the sandbox can actually create.
func TestNativeClaudeFileToolLinksInstalledClient(t *testing.T) {
	bin := os.Getenv("OPENLINKER_TEST_NATIVE_CLAUDE_BIN")
	if bin == "" {
		t.Skip("set the official Claude client path for real file-tool acceptance")
	}
	for _, optIn := range []bool{true, false} {
		name := "bash-default"
		if optIn {
			name = "file-tools-opt-in"
		}
		t.Run(name, func(t *testing.T) {
			c := isolationConfig(t, "claude")
			c.Bin, c.Model, c.Timeout = bin, "claude-sonnet-4-6", 120*time.Second
			if optIn {
				c.AllowedTools = []string{"Bash", "Read", "Grep", "Glob", "Edit", "Write"}
			}
			home := t.TempDir()
			conf := filepath.Join(home, ".claude")
			if err := os.Mkdir(conf, 0700); err != nil {
				t.Fatal(err)
			}
			write := func(path, value string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			const secret = "SYNTHETIC_PROBE_file_content_71be0c"
			const envSecret = "SYNTHETIC_PROBE_parent_environment_30c73a"
			const hiddenName = "SYNTHETIC_PROBE_hidden_filename_192fa8"
			write(filepath.Join(conf, ".credentials.json"), secret)
			// API authentication is synthetic; the mock also sees client-local
			// /proc/self/environ probes without touching any personal login.
			c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CLAUDE_CONFIG_DIR=" + conf,
				"ANTHROPIC_API_KEY=" + envSecret, "NO_PROXY=127.0.0.1,localhost,::1", "HTTP_PROXY=", "HTTPS_PROXY=", "ALL_PROXY="}
			prepared, closeSession, err := prepareIsolatedSession(context.Background(), c, isolationRun(name))
			if err != nil {
				t.Fatal(err)
			}
			workspace := prepared.Workspace
			if err := closeSession(); err != nil {
				t.Fatal(err)
			}
			type probe struct {
				id, tool, source string
				input            map[string]any
				positive         string
			}
			var probes []probe
			add := func(id, tool string, input map[string]any, source, positive string) {
				probes = append(probes, probe{id, tool, source, input, positive})
			}
			own := filepath.Join(workspace, "positive.txt")
			add("setup", "Bash", map[string]any{"command": "/bin/bash ./links.sh", "description": "Create synthetic link probes"}, "", "SETUP_COMPLETE")
			add("positive-write", "Write", map[string]any{"file_path": own, "content": "positive-file-before"}, "", "")
			add("positive-read", "Read", map[string]any{"file_path": own}, "", "positive-file-before")
			add("positive-edit", "Edit", map[string]any{"file_path": own, "old_string": "positive-file-before", "new_string": "positive-file-after"}, "", "")
			add("positive-grep", "Grep", map[string]any{"path": own, "pattern": "positive-file", "output_mode": "content"}, "", "positive-file-after")
			add("positive-glob", "Glob", map[string]any{"path": workspace, "pattern": "positive.txt"}, "", "positive.txt")
			var script strings.Builder
			expectedSymlinks := map[string]string{}
			for _, kind := range []string{"symlink-file", "symlink-dir", "hardlink-bash", "hardlink-preseeded", "proc-self-root"} {
				if kind == "proc-self-root" && runtime.GOOS != "linux" {
					continue
				}
				for _, tool := range []string{"Read", "Grep", "Glob", "Edit", "Write"} {
					id := kind + "-" + strings.ToLower(tool)
					sourceDir := filepath.Join(conf, id)
					if err := os.Mkdir(sourceDir, 0700); err != nil {
						t.Fatal(err)
					}
					source := filepath.Join(sourceDir, hiddenName)
					write(source, "editable-prefix\n"+secret+"\n")
					alias := filepath.Join(workspace, id)
					target := source
					if kind == "proc-self-root" {
						target = "/proc/self/root" + source
						if tool == "Glob" {
							target = "/proc/self/root" + sourceDir
						}
					}
					path := alias
					switch kind {
					case "hardlink-preseeded":
						if err := os.Link(source, alias); err != nil {
							t.Fatal("synthetic hardlink fixture must be on the same filesystem:", err)
						}
					case "hardlink-bash":
						fmt.Fprintf(&script, "ln %s %s 2>/dev/null || true\n", jsonString(source), jsonString(alias))
					case "symlink-dir":
						fmt.Fprintf(&script, "ln -s %s %s\n", jsonString(sourceDir), jsonString(alias))
						expectedSymlinks[alias] = sourceDir
						path = filepath.Join(alias, hiddenName)
					default:
						fmt.Fprintf(&script, "ln -s %s %s\n", jsonString(target), jsonString(alias))
						expectedSymlinks[alias] = target
					}
					input := map[string]any{"file_path": path}
					switch tool {
					case "Grep":
						input = map[string]any{"path": path, "pattern": "SYNTHETIC_PROBE_", "output_mode": "content"}
					case "Glob":
						input = map[string]any{"path": workspace, "pattern": id}
						if kind == "symlink-dir" || kind == "proc-self-root" {
							input = map[string]any{"path": alias, "pattern": "*"}
						}
					case "Edit":
						// An Edit needs a preceding Read. Audit both results, so a
						// missing prerequisite cannot masquerade as a safe edit.
						add(id+"-preread", "Read", map[string]any{"file_path": path}, source, "")
						input["old_string"], input["new_string"] = "editable-prefix", "MUTATED"
					case "Write":
						add(id+"-preread", "Read", map[string]any{"file_path": path}, source, "")
						input["content"] = "MUTATED"
					}
					add(id, tool, input, source, "")
				}
			}
			if runtime.GOOS == "linux" {
				alias := filepath.Join(workspace, "proc-self-environ")
				fmt.Fprintf(&script, "ln -s /proc/self/environ %s\n", jsonString(alias))
				expectedSymlinks[alias] = "/proc/self/environ"
				add("proc-self-environ-read", "Read", map[string]any{"file_path": alias}, "", "")
				add("proc-self-environ-grep", "Grep", map[string]any{"path": alias, "pattern": "SYNTHETIC_PROBE_", "output_mode": "content"}, "", "")
				add("proc-self-environ-edit", "Edit", map[string]any{"file_path": alias, "old_string": "ANTHROPIC_API_KEY=", "new_string": "FIXTURE_REJECTED="}, "", "")
				add("proc-self-environ-write", "Write", map[string]any{"file_path": alias, "content": "FIXTURE_REJECTED"}, "", "")
				procDir := filepath.Join(workspace, "proc-self-dir")
				fmt.Fprintf(&script, "ln -s /proc/self %s\n", jsonString(procDir))
				expectedSymlinks[procDir] = "/proc/self"
				add("proc-self-glob", "Glob", map[string]any{"path": procDir, "pattern": "environ"}, "", "")
			}
			// A trusted host has already granted this inode. Probe Bash too:
			// reducing the tool set must not be advertised as removing aliases
			// that the operator has deliberately placed inside the workspace.
			add("hardlink-preseeded-bash", "Bash", map[string]any{"command": "/bin/cat ./hardlink-preseeded-read", "description": "Read a host-preseeded synthetic inode alias"}, "", "")
			script.WriteString("printf bash-only-initial > positive.txt\nprintf SETUP_COMPLETE\n")
			write(filepath.Join(workspace, "links.sh"), script.String())
			var mu sync.Mutex
			results := map[string]map[string]any{}
			setupChecked := false
			hardlinks := map[string]bool{}
			toolNames := map[string]bool{}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					fmt.Fprint(w, `{}`)
					return
				}
				if strings.Contains(r.URL.Path, "count_tokens") {
					fmt.Fprint(w, `{"input_tokens":20}`)
					return
				}
				raw, _ := io.ReadAll(r.Body)
				var request struct {
					Tools    []struct{ Name string }             `json:"tools"`
					Messages []struct{ Content json.RawMessage } `json:"messages"`
				}
				if err := json.Unmarshal(raw, &request); err != nil {
					t.Error(err)
				}
				mu.Lock()
				defer mu.Unlock()
				calls++
				for _, tool := range request.Tools {
					toolNames[tool.Name] = true
				}
				for _, message := range request.Messages {
					var blocks []map[string]any
					_ = json.Unmarshal(message.Content, &blocks)
					for _, block := range blocks {
						if block["type"] == "tool_result" {
							if id, ok := block["tool_use_id"].(string); ok {
								results[id] = block
							}
						}
					}
				}
				if _, ok := results["setup"]; ok && !setupChecked {
					setupChecked = true
					for _, tool := range []string{"Read", "Grep", "Glob", "Edit", "Write"} {
						id := "hardlink-bash-" + strings.ToLower(tool)
						alias, e1 := os.Stat(filepath.Join(workspace, id))
						source, e2 := os.Stat(filepath.Join(conf, id, hiddenName))
						hardlinks[tool] = e1 == nil && e2 == nil && os.SameFile(alias, source)
						if hardlinks[tool] {
							t.Errorf("sandbox created a hard link to a protected host inode: %s", tool)
						}
					}
					for alias, target := range expectedSymlinks {
						if actual, err := os.Readlink(alias); err != nil || actual != target {
							t.Errorf("Bash did not create the expected symlink %s: %v", filepath.Base(alias), err)
						}
					}
				}
				for _, p := range probes {
					if _, done := results[p.id]; !done {
						claudeFixtureEvents(w, calls, map[string]any{"type": "tool_use", "id": p.id, "name": p.tool, "input": p.input}, true)
						return
					}
				}
				claudeFixtureEvents(w, calls, map[string]any{"type": "text", "text": "fixture complete"}, false)
			}))
			defer server.Close()
			c.Env = append(c.Env, "ANTHROPIC_BASE_URL="+server.URL)
			p, err := NewProvider(c)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Run(context.Background(), isolationRun(name)); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, tool := range []string{"Read", "Grep", "Glob", "Edit", "Write"} {
				if toolNames[tool] != optIn {
					t.Errorf("%s advertised=%v, explicit opt-in=%v", tool, toolNames[tool], optIn)
				}
			}
			for _, probe := range probes {
				result, ok := results[probe.id]
				if !ok {
					t.Errorf("missing result %s", probe.id)
					continue
				}
				body := jsonObject(result["content"])
				if probe.id == "setup" {
					t.Logf("setup detail: %s", body)
					if result["is_error"] == true {
						t.Error("Bash setup failed; link probes are inconclusive")
					}
				}
				leak := strings.Contains(body, secret) || strings.Contains(body, envSecret)
				// A Glob must reveal an unknown child name, not merely echo an
				// input path containing a filename already supplied by the fixture.
				if probe.tool == "Glob" && (strings.HasPrefix(probe.id, "symlink-dir-") || strings.HasPrefix(probe.id, "proc-self-root-")) {
					leak = leak || strings.Contains(body, hiddenName)
				}
				mutated := false
				if probe.source != "" {
					raw, err := os.ReadFile(probe.source)
					mutated = err != nil || string(raw) != "editable-prefix\n"+secret+"\n"
				}
				preseededAudit := strings.HasPrefix(probe.id, "hardlink-preseeded-") && (optIn || probe.tool == "Bash")
				if !preseededAudit && (leak || mutated) {
					t.Errorf("policy escape via %s: leak=%v host_mutation=%v", probe.id, leak, mutated)
				}
				if optIn && strings.HasPrefix(probe.id, "positive-") && result["is_error"] == true {
					t.Errorf("positive control %s returned an error: %s", probe.id, body)
				}
				if (optIn || probe.tool == "Bash") && probe.positive != "" && !strings.Contains(body, probe.positive) {
					t.Errorf("positive control %s failed: %s", probe.id, body)
				}
				if !optIn && probe.tool != "Bash" && result["is_error"] != true {
					t.Errorf("disabled tool %s executed: %s", probe.id, body)
				}
				t.Logf("%s: tool_error=%v credential_or_filename=%v host_mutation=%v", probe.id, result["is_error"] == true, leak, mutated)
			}
			for _, tool := range []string{"Read", "Grep", "Glob", "Edit", "Write"} {
				t.Logf("Bash created hardlink for %s: %v", tool, hardlinks[tool])
			}
			if optIn {
				raw, err := os.ReadFile(own)
				if err != nil || string(raw) != "positive-file-after" {
					t.Fatalf("positive Write/Edit did not mutate own file: %q %v", raw, err)
				}
			}
		})
	}
}
