// A local protocol fixture: it never connects to a model or reads native login.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	codex := strings.Contains(filepath.Base(os.Args[0]), "codex")
	args := strings.Join(os.Args[1:], " ")
	if args == "--version" {
		if codex {
			fmt.Println("codex-cli 0.153.0")
		} else {
			fmt.Println("2.1.259 (Claude Code)")
		}
		return
	}
	if codex && args == "app-server --help" {
		fmt.Println("app-server generate-json-schema --listen --config --disable")
		return
	}
	if !codex && args == "--help" {
		fmt.Println("--safe-mode --bare --no-chrome --disable-slash-commands --permission-mode --resume stream-json --verbose --include-partial-messages --strict-mcp-config")
		return
	}
	if codex {
		os.Exit(2)
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "synthetic-delegation-key" ||
		os.Getenv("ANTHROPIC_API_KEY_FILE") != "" || os.Getenv("OPENLINKER_AGENT_TOKEN") != "" ||
		!strings.Contains(args, "--bare") || strings.Contains(args, "--safe-mode") || !strings.Contains(args, "--strict-mcp-config") {
		fmt.Fprintln(os.Stderr, "credential or bare-mode contract failed")
		os.Exit(3)
	}
	io.Copy(io.Discard, os.Stdin)
	json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "result", "result": "validated delegated Claude execution", "session_id": "fixture-session"})
}
