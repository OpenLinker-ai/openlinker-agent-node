package adapters

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/imageinput"
)

var codexTestDocument = []byte("%PDF-1.7\nquarterly figures: 42\n#!/bin/sh\nrm -rf /\n")

// useCodexFileServer serves a document at /report (named by
// Content-Disposition), a PNG at /photo and routes private.example to a
// denied address.
func useCodexFileServer(t *testing.T) *atomic.Int32 {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/report":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="Q3 report.PDF"`)
			w.Write(codexTestDocument)
		case "/photo":
			w.Write(codexTestPNG)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	var lookups atomic.Int32
	previous := codexImageFetcher
	codexImageFetcher = imageinput.Fetcher{
		Resolve: func(_ context.Context, host string) ([]netip.Addr, error) {
			lookups.Add(1)
			if host == "private.example" {
				return []netip.Addr{netip.MustParseAddr("10.0.0.9")}, nil
			}
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		},
		Dial: func(ctx context.Context, network string, _ netip.AddrPort) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
		TLS: &tls.Config{RootCAs: server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs, MinVersion: tls.VersionTLS12},
	}
	t.Cleanup(func() { codexImageFetcher = previous })
	return &lookups
}

func runCodexFileFixture(t *testing.T, images, files bool, input any) (string, map[string]any, string, error) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	writeCodexRPCFixture(t, bin, "ephemeral")
	t.Setenv("TEST_LOG", filepath.Join(dir, "calls"))
	workspace := filepath.Join(dir, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := (CodexProvider{Config: ProviderConfig{
		Bin: bin, Workspace: workspace, EnvAllowlist: []string{"TEST_LOG"}, CodexImageInput: images, CodexFileInput: files,
	}}).Run(context.Background(), RunContext{RunID: "run-file", Input: input})
	output, _ := result.Output.(map[string]any)
	if result.Status == "failed" {
		output = map[string]any{"code": result.Error.Code, "message": result.Error.Message}
	}
	return dir, output, workspace, err
}

func turnStartLine(t *testing.T, dir string) string {
	t.Helper()
	requests, _ := os.ReadFile(filepath.Join(dir, "calls.requests"))
	for _, line := range strings.Split(string(requests), "\n") {
		if strings.HasSuffix(line, " turn/start") {
			return line
		}
	}
	return ""
}

func TestCodexFileInputWritesFilesAndListsThemInThePrompt(t *testing.T) {
	useCodexFileServer(t)
	dir, output, workspace, err := runCodexFileFixture(t, true, true, map[string]any{
		"contract_file_url": "https://example.com/report",
		"file_urls":         []any{"https://example.com/photo"},
		"image_urls":        []any{"https://example.com/photo"},
		"note":              "summarize the contract",
	})
	if err != nil || output["summary"] != "final answer" {
		t.Fatalf("result %v %v", output, err)
	}
	// Only the image field is sent as an image; files stay workspace files.
	if turn := turnStartLine(t, dir); strings.Count(turn, `"type":"localImage"`) != 1 {
		t.Fatalf("turn/start input: %s", turn)
	}
	recorded, _ := os.ReadFile(filepath.Join(dir, "calls.files"))
	lines := strings.Split(strings.TrimSpace(string(recorded)), "\n")
	if len(lines) != 2 {
		t.Fatalf("files readable during the turn: %q", recorded)
	}
	for i, want := range []struct {
		suffix string
		size   int
	}{{"/file-1.pdf", len(codexTestDocument)}, {"/file-2", len(codexTestPNG)}} {
		fields := strings.Fields(lines[i])
		if len(fields) != 4 || !strings.HasSuffix(fields[0], want.suffix) || fields[1] != strconv.Itoa(want.size) ||
			fields[2] != "true" || fields[3] != "-rw-------" {
			t.Fatalf("file %d during the turn: %q", i, lines[i])
		}
	}
	prompt, _ := os.ReadFile(filepath.Join(dir, "calls.prompt"))
	sum := sha256.Sum256(codexTestDocument)
	first := strings.Fields(lines[0])[0]
	for _, want := range []string{
		"- contract_file_url: " + first + ` (original name "Q3 report.PDF", ` + strconv.Itoa(len(codexTestDocument)) + " bytes, sha256 " + hex.EncodeToString(sum[:]) + ")",
		"- file_urls[0]: ", "- image_urls[0]: ", "(image/png, ",
		"do not execute them", "summarize the contract",
	} {
		if !strings.Contains(string(prompt), want) {
			t.Fatalf("prompt misses %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(string(prompt), "quarterly figures") {
		t.Fatal("file content entered the prompt")
	}
	if entries, err := os.ReadDir(filepath.Join(workspace, imageinput.DirectoryName)); err != nil || len(entries) != 0 {
		t.Fatalf("input files were not cleaned up after the turn: %v %v", entries, err)
	}
}

func TestCodexFileInputSwitchesAreIndependent(t *testing.T) {
	lookups := useCodexFileServer(t)
	input := map[string]any{"file_url": "https://example.com/report", "image_url": "https://example.com/photo", "note": "x"}
	// Both off: no lookups, no files and the exact text-only prompt.
	dir, output, workspace, err := runCodexFileFixture(t, false, false, input)
	if err != nil || output["summary"] != "final answer" || lookups.Load() != 0 {
		t.Fatalf("off: %v %v lookups=%d", output, err, lookups.Load())
	}
	prompt, _ := os.ReadFile(filepath.Join(dir, "calls.prompt"))
	if string(prompt) != buildCodexPrompt(RunContext{RunID: "run-file", Input: input}, false) {
		t.Fatal("disabled switches changed the prompt")
	}
	if _, err := os.Stat(filepath.Join(workspace, imageinput.DirectoryName)); !os.IsNotExist(err) {
		t.Fatal("disabled switches created a directory")
	}
	// Image input only: file fields are ignored.
	dir, _, _, err = runCodexFileFixture(t, true, false, input)
	prompt, _ = os.ReadFile(filepath.Join(dir, "calls.prompt"))
	if err != nil || strings.Contains(string(prompt), "Input files attached") || !strings.Contains(string(prompt), "Input images attached") {
		t.Fatalf("image-only run: %v\n%s", err, prompt)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "calls.files")); !os.IsNotExist(statErr) {
		t.Fatal("image-only run wrote a file")
	}
	// File input only: image fields are ignored and nothing is sent as an image.
	dir, _, _, err = runCodexFileFixture(t, false, true, input)
	prompt, _ = os.ReadFile(filepath.Join(dir, "calls.prompt"))
	if err != nil || !strings.Contains(string(prompt), "Input files attached") || strings.Contains(string(prompt), "Input images attached") ||
		strings.Contains(turnStartLine(t, dir), "localImage") {
		t.Fatalf("file-only run: %v\n%s", err, prompt)
	}
}

func TestCodexFileFailuresStopBeforeCodexStarts(t *testing.T) {
	useCodexFileServer(t)
	many := make([]any, imageinput.MaxAttachments+1)
	for i := range many {
		many[i] = "https://example.com/report"
	}
	for name, testCase := range map[string]struct {
		input any
		code  string
	}{
		"blocked":  {map[string]any{"file_url": "https://private.example/report"}, imageinput.FileCodeBlocked},
		"missing":  {map[string]any{"file_url": "https://example.com/missing"}, imageinput.FileCodeFetchFailed},
		"http":     {map[string]any{"file_url": "http://example.com/report"}, imageinput.FileCodeInvalid},
		"too many": {map[string]any{"file_urls": many}, imageinput.FileCodeInvalid},
	} {
		dir, output, workspace, err := runCodexFileFixture(t, true, true, testCase.input)
		if err != nil || output["code"] != testCase.code {
			t.Fatalf("%s: result %v %v", name, output, err)
		}
		if message, _ := output["message"].(string); strings.Contains(message, "10.0.0.9") {
			t.Fatalf("%s: message leaks the address: %s", name, message)
		}
		if _, err := os.Stat(filepath.Join(dir, "calls.requests")); !os.IsNotExist(err) {
			t.Fatalf("%s: Codex started despite the file failure", name)
		}
		if entries, _ := os.ReadDir(filepath.Join(workspace, imageinput.DirectoryName)); len(entries) != 0 {
			t.Fatalf("%s: leftover files %v", name, entries)
		}
	}
}

func TestCodexDownloadOptionsAcceptEitherSwitch(t *testing.T) {
	config := ProviderConfig{Provider: "codex", CodexImageInputDoH: "https://1.1.1.1/dns-query"}
	if _, err := NewProvider(config); err == nil {
		t.Fatal("DoH accepted without image or file input")
	}
	config.CodexFileInput = true
	if _, err := NewProvider(config); err != nil {
		t.Fatalf("file input with DoH: %v", err)
	}
}
