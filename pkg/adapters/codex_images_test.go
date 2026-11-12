package adapters

import (
	"bytes"
	"context"
	"crypto/tls"
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

var codexTestPNG = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 100)...)

// useCodexImageServer routes example.com (a public address) to a local TLS
// server and private.example to a denied address. It records resolver use.
func useCodexImageServer(t *testing.T) *atomic.Int32 {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fake.png" {
			w.Write([]byte("<html>not an image</html>"))
			return
		}
		w.Write(codexTestPNG)
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

func runCodexImageFixture(t *testing.T, enabled bool, input any) (string, map[string]any, string, error) {
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
		Bin: bin, Workspace: workspace, EnvAllowlist: []string{"TEST_LOG"}, CodexImageInput: enabled,
	}}).Run(context.Background(), RunContext{RunID: "run-image", Input: input})
	output, _ := result.Output.(map[string]any)
	if result.Status == "failed" {
		output = map[string]any{"code": result.Error.Code, "message": result.Error.Message}
	}
	return dir, output, workspace, err
}

func TestCodexSendsInputImagesWithTheTurn(t *testing.T) {
	useCodexImageServer(t)
	dir, output, workspace, err := runCodexImageFixture(t, true, map[string]any{
		"image_url": "https://example.com/photo", "note": "is this AI generated?",
	})
	if err != nil || output["summary"] != "final answer" {
		t.Fatalf("result %v %v", output, err)
	}
	requests, _ := os.ReadFile(filepath.Join(dir, "calls.requests"))
	var turn string
	for _, line := range strings.Split(string(requests), "\n") {
		if strings.HasSuffix(line, " turn/start") {
			turn = line
		}
	}
	if !strings.Contains(turn, `"type":"text"`) || strings.Count(turn, `"type":"localImage"`) != 1 {
		t.Fatalf("turn/start input: %s", turn)
	}
	images, _ := os.ReadFile(filepath.Join(dir, "calls.images"))
	fields := strings.Fields(string(images))
	if len(fields) != 3 || fields[1] != strconv.Itoa(len(codexTestPNG)) || fields[2] != "true" {
		t.Fatalf("image not readable during the turn: %q", images)
	}
	path := fields[0]
	if !strings.HasPrefix(path, filepath.Join(workspace, imageinput.DirectoryName)+string(filepath.Separator)) || !strings.Contains(turn, path) {
		t.Fatalf("image path %s outside the run directory or not sent", path)
	}
	prompt, _ := os.ReadFile(filepath.Join(dir, "calls.prompt"))
	relative, _ := filepath.Rel(workspace, path)
	if !strings.Contains(string(prompt), "- image_url: "+filepath.ToSlash(relative)+" (image/png, ") ||
		!strings.Contains(string(prompt), "is this AI generated?") {
		t.Fatalf("prompt does not describe the image: %s", prompt)
	}
	if _, err := os.Stat(filepath.Join(workspace, imageinput.DirectoryName)); !os.IsNotExist(err) {
		t.Fatal("input images were not cleaned up after the turn")
	}
}

func TestCodexImageInputOffKeepsTextOnlyTurn(t *testing.T) {
	lookups := useCodexImageServer(t)
	input := map[string]any{"image_url": "https://example.com/photo", "note": "describe"}
	dirOff, outputOff, workspace, err := runCodexImageFixture(t, false, input)
	if err != nil || outputOff["summary"] != "final answer" {
		t.Fatalf("result %v %v", outputOff, err)
	}
	if lookups.Load() != 0 {
		t.Fatal("disabled image input resolved a link")
	}
	if _, err := os.Stat(filepath.Join(dirOff, "calls.images")); !os.IsNotExist(err) {
		t.Fatal("disabled image input sent an image")
	}
	if _, err := os.Stat(filepath.Join(workspace, imageinput.DirectoryName)); !os.IsNotExist(err) {
		t.Fatal("disabled image input created a directory")
	}
	promptOff, _ := os.ReadFile(filepath.Join(dirOff, "calls.prompt"))
	want := buildCodexPrompt(RunContext{RunID: "run-image", Input: input}, false)
	if string(promptOff) != want {
		t.Fatalf("disabled prompt changed:\n%s\nwant:\n%s", promptOff, want)
	}
	// Enabled but without image fields also keeps the exact text-only turn.
	dirNone, _, _, err := runCodexImageFixture(t, true, map[string]any{"url": "https://example.com/photo"})
	if err != nil || lookups.Load() != 0 {
		t.Fatalf("non-image field fetched: %v", err)
	}
	promptNone, _ := os.ReadFile(filepath.Join(dirNone, "calls.prompt"))
	if string(promptNone) != buildCodexPrompt(RunContext{RunID: "run-image", Input: map[string]any{"url": "https://example.com/photo"}}, false) {
		t.Fatal("enabled switch changed a run without image fields")
	}
}

func TestCodexImageFailuresStopBeforeCodexStarts(t *testing.T) {
	useCodexImageServer(t)
	for link, code := range map[string]string{
		"https://private.example/a.png": imageinput.CodeBlocked,
		"https://example.com/fake.png":  imageinput.CodeUnsupportedType,
		"http://example.com/a.png":      imageinput.CodeInvalid,
	} {
		dir, output, workspace, err := runCodexImageFixture(t, true, map[string]any{"image_url": link})
		if err != nil || output["code"] != code {
			t.Fatalf("%s: result %v %v", link, output, err)
		}
		if message, _ := output["message"].(string); strings.Contains(message, "10.0.0.9") || strings.Contains(message, "<html>") {
			t.Fatalf("message leaks details: %s", message)
		}
		if _, err := os.Stat(filepath.Join(dir, "calls.requests")); !os.IsNotExist(err) {
			t.Fatalf("%s: Codex started despite image failure", link)
		}
		if entries, _ := os.ReadDir(filepath.Join(workspace, imageinput.DirectoryName)); len(entries) != 0 {
			t.Fatalf("%s: leftover files %v", link, entries)
		}
	}
}
