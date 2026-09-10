package codexhome

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/codexrpc"
)

// Exercise native persistence with synthetic, nearly-expired tokens. This test
// never reads personal auth or contacts a real token/model endpoint.
func TestInstalledCodexRefreshPersistsThroughAuthSymlink(t *testing.T) {
	if os.Getenv("OPENLINKER_TEST_CODEX_AUTH_REFRESH") != "1" {
		t.Skip("opt-in installed Codex with local token fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	version, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil || strings.TrimSpace(string(version)) != "codex-cli "+codexrpc.ProtocolVersion {
		t.Fatal("refresh test requires the pinned native CLI")
	}
	jwt := func(exp time.Time) string {
		payload, _ := json.Marshal(map[string]any{"exp": exp.Unix(), "email": "fixture@example.invalid", "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "fixture-account", "chatgpt_plan_type": "plus"}})
		return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".fixture"
	}
	source := t.TempDir()
	auth := filepath.Join(source, "auth.json")
	raw, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "last_refresh": "2020-01-01T00:00:00Z", "tokens": map[string]any{"id_token": jwt(time.Now().Add(2 * time.Minute)), "access_token": jwt(time.Now().Add(2 * time.Minute)), "refresh_token": "fixture-old-refresh", "account_id": "fixture-account"}})
	if err := os.WriteFile(auth, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Unix(100, 0)
	if err := os.Chtimes(auth, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" || r.Method != "POST" {
			http.Error(w, "unexpected route", 404)
			return
		}
		var request struct {
			GrantType    string `json:"grant_type"`
			RefreshToken string `json:"refresh_token"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.GrantType != "refresh_token" || request.RefreshToken != "fixture-old-refresh" {
			http.Error(w, "unexpected token fixture", 400)
			return
		}
		refreshes.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": jwt(time.Now().Add(time.Hour)), "refresh_token": "fixture-new-refresh", "id_token": jwt(time.Now().Add(time.Hour))})
	}))
	defer server.Close()
	env, cleanup, err := Prepare([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "CODEX_HOME=" + source, "CODEX_REFRESH_TOKEN_URL_OVERRIDE=" + server.URL + "/oauth/token", "NO_PROXY=127.0.0.1,localhost", "HTTP_PROXY=" + server.URL, "HTTPS_PROXY=" + server.URL, "ALL_PROXY=" + server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	attempt := homeValue(env)
	command := exec.CommandContext(ctx, bin, "app-server", "-c", "cli_auth_credentials_store=\"file\"", "--disable", "apps", "--disable", "remote_control", "--disable", "plugins")
	var diagnostics bytes.Buffer // disposable fixture credentials only
	command.Env, command.Dir, command.Stderr = env, t.TempDir(), &diagnostics
	in, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	client := codexrpc.New(in, out, func() { _ = command.Process.Kill(); _ = in.Close(); _ = out.Close() })
	defer func() {
		client.Close()
		_ = command.Wait()
		if t.Failed() {
			t.Log(diagnostics.String())
		}
	}()
	if err := client.Call(ctx, "initialize", codexrpc.InitializeParams{ClientInfo: codexrpc.ClientInfo{Name: "openlinker_refresh_test", Version: "1"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := client.Notify(ctx, "initialized", struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := client.Call(ctx, "account/read", map[string]bool{"refreshToken": true}, nil); err != nil {
		t.Fatal(err)
	}
	if refreshes.Load() != 1 {
		t.Fatalf("native refresh calls = %d", refreshes.Load())
	}
	info, err := os.Lstat(filepath.Join(attempt, "auth.json"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("native refresh replaced authentication symlink")
	}
	info, err = os.Stat(auth)
	if err != nil || !info.ModTime().After(oldTime) {
		t.Fatal("native auth mtime did not change")
	}
	client.Close()
	_ = command.Wait()
	cleanup()
	raw, err = os.ReadFile(auth)
	if err != nil || !strings.Contains(string(raw), "fixture-new-refresh") {
		t.Fatal("refreshed token lost after Attempt cleanup")
	}
	if _, err := os.Stat(attempt); !os.IsNotExist(err) {
		t.Fatal("Attempt directory was not removed")
	}
}
