package adapters

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"image"
	"image/color"
	"image/png"
	"math/big"
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
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/imageinput"
)

// codexTestPNG is a real decodable image: real clients may decode or resize it.
var codexTestPNG = func() []byte {
	canvas := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for x := 0; x < 16; x++ {
		for y := 0; y < 16; y++ {
			canvas.Set(x, y, color.RGBA{R: uint8(x * 16), G: uint8(y * 16), B: 128, A: 255})
		}
	}
	var buffer bytes.Buffer
	_ = png.Encode(&buffer, canvas)
	return buffer.Bytes()
}()

// useCodexImageServer routes example.com (a public address) to a local TLS
// server and private.example to a denied address. It records resolver use.
func useCodexImageServer(t *testing.T) *atomic.Int32 {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow.png" {
			time.Sleep(2 * time.Second)
		}
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
	if entries, err := os.ReadDir(filepath.Join(workspace, imageinput.DirectoryName)); err != nil || len(entries) != 0 {
		t.Fatalf("input images were not cleaned up after the turn: %v %v", entries, err)
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

func TestCodexImageDownloadCountsAgainstProviderTimeout(t *testing.T) {
	useCodexImageServer(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	writeCodexRPCFixture(t, bin, "ephemeral")
	t.Setenv("TEST_LOG", filepath.Join(dir, "calls"))
	started := time.Now()
	result, err := (CodexProvider{Config: ProviderConfig{
		Bin: bin, Workspace: dir, EnvAllowlist: []string{"TEST_LOG"}, CodexImageInput: true,
		Timeout: 300 * time.Millisecond,
	}}).Run(context.Background(), RunContext{RunID: "run-image", Input: map[string]any{"image_url": "https://example.com/slow.png"}})
	if err == nil || !strings.Contains(err.Error(), "timed out after 300ms") || result.Status == "failed" {
		t.Fatalf("result %+v err %v", result, err)
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("download outlived the provider timeout: %s", elapsed)
	}
	if _, err := os.Stat(filepath.Join(dir, "calls.requests")); !os.IsNotExist(err) {
		t.Fatal("Codex started after the provider timeout")
	}
}

func TestCodexImageInputUsesConfiguredDoHWithoutHostDNS(t *testing.T) {
	useCodexImageServer(t)
	codexImageFetcher.Resolve = nil // production resolution path
	var endpoints, hosts []string
	previous := codexDoHResolver
	codexDoHResolver = func(endpoint string) (func(context.Context, string) ([]netip.Addr, error), error) {
		endpoints = append(endpoints, endpoint)
		return func(_ context.Context, host string) ([]netip.Addr, error) {
			hosts = append(hosts, host)
			if host == "fakeip.example" {
				return []netip.Addr{netip.MustParseAddr("198.18.1.163")}, nil
			}
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}, nil
	}
	t.Cleanup(func() { codexDoHResolver = previous })
	run := func(link string) (map[string]any, string) {
		dir := t.TempDir()
		bin := filepath.Join(dir, "codex")
		writeCodexRPCFixture(t, bin, "ephemeral")
		t.Setenv("TEST_LOG", filepath.Join(dir, "calls"))
		provider, err := NewProvider(ProviderConfig{Provider: "codex", Bin: bin, Workspace: dir, EnvAllowlist: []string{"TEST_LOG"},
			CodexImageInput: true, CodexImageInputDoH: "https://223.5.5.5/dns-query"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := provider.Run(context.Background(), RunContext{RunID: "run-doh", Input: map[string]any{"image_url": link}})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status == "failed" {
			return map[string]any{"code": result.Error.Code}, dir
		}
		return result.Output.(map[string]any), dir
	}
	output, dir := run("https://example.com/photo")
	if output["summary"] != "final answer" {
		t.Fatalf("output %v", output)
	}
	if images, _ := os.ReadFile(filepath.Join(dir, "calls.images")); len(images) == 0 {
		t.Fatal("image not sent")
	}
	if output, _ := run("https://fakeip.example/photo"); output["code"] != imageinput.CodeBlocked {
		t.Fatalf("fake-IP DoH answer not blocked: %v", output)
	}
	if strings.Join(endpoints, ",") != "https://223.5.5.5/dns-query,https://223.5.5.5/dns-query" || strings.Join(hosts, ",") != "example.com,fakeip.example" {
		t.Fatalf("DoH endpoints %v hosts %v", endpoints, hosts)
	}
	for _, config := range []ProviderConfig{
		{Provider: "codex", CodexImageInputDoH: "https://223.5.5.5/dns-query"},
		{Provider: "codex", CodexImageInput: true, CodexImageInputDoH: "https://10.0.0.1/dns-query"},
	} {
		if _, err := NewProvider(config); err == nil {
			t.Fatalf("accepted %+v", config)
		}
	}
}

func TestCodexImageInputTrustedPrivateSourceWithCAFile(t *testing.T) {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "corp CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	ca, _ := x509.ParseCertificate(caDER)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafDER, _ := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("10.20.0.5")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}, ca, &leafKey.PublicKey, caKey)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(codexTestPNG) }))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}}}
	server.StartTLS()
	defer server.Close()
	caFile := filepath.Join(t.TempDir(), "corp-ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := codexImageFetcher
	var dialed []string
	codexImageFetcher = imageinput.Fetcher{Dial: func(ctx context.Context, network string, address netip.AddrPort) (net.Conn, error) {
		dialed = append(dialed, address.String())
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	t.Cleanup(func() { codexImageFetcher = previous })
	run := func(config ProviderConfig, link string) map[string]any {
		dir := t.TempDir()
		bin := filepath.Join(dir, "codex")
		writeCodexRPCFixture(t, bin, "ephemeral")
		t.Setenv("TEST_LOG", filepath.Join(dir, "calls"))
		config.Provider, config.Bin, config.Workspace, config.EnvAllowlist, config.CodexImageInput = "codex", bin, dir, []string{"TEST_LOG"}, true
		provider, err := NewProvider(config)
		if err != nil {
			t.Fatal(err)
		}
		result, err := provider.Run(context.Background(), RunContext{RunID: "run-trusted", Input: map[string]any{"image_url": link}})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status == "failed" {
			return map[string]any{"code": result.Error.Code}
		}
		return result.Output.(map[string]any)
	}
	trusted := ProviderConfig{CodexImageInputTrustedSources: []string{"https://10.20.0.5"}, CodexImageInputTrustedOnly: true, CodexImageInputCAFile: caFile}
	if output := run(trusted, "https://10.20.0.5/photo"); output["summary"] != "final answer" {
		t.Fatalf("trusted private source failed: %v", output)
	}
	if len(dialed) != 1 || dialed[0] != "10.20.0.5:443" {
		t.Fatalf("dialed %v", dialed)
	}
	if output := run(trusted, "https://example.com/photo"); output["code"] != imageinput.CodeBlocked {
		t.Fatalf("trusted-only mode accepted another source: %v", output)
	}
	withoutCA := trusted
	withoutCA.CodexImageInputCAFile = ""
	if output := run(withoutCA, "https://10.20.0.5/photo"); output["code"] != imageinput.CodeFetchFailed {
		t.Fatalf("private CA trusted without CA file: %v", output)
	}
	notTrusted := ProviderConfig{CodexImageInputCAFile: caFile}
	if output := run(notTrusted, "https://10.20.0.5/photo"); output["code"] != imageinput.CodeBlocked {
		t.Fatalf("private address accepted without trusted source: %v", output)
	}
	for _, config := range []ProviderConfig{
		{Provider: "codex", CodexImageInputTrustedSources: []string{"https://10.20.0.5"}},
		{Provider: "codex", CodexImageInput: true, CodexImageInputTrustedOnly: true},
		{Provider: "codex", CodexImageInput: true, CodexImageInputTrustedSources: []string{"https://*.corp"}},
		{Provider: "codex", CodexImageInput: true, CodexImageInputCAFile: filepath.Join(t.TempDir(), "missing.pem")},
	} {
		if _, err := NewProvider(config); err == nil {
			t.Fatalf("accepted %+v", config)
		}
	}
}

func TestCodexSendsInlineImagesWithoutBase64InPrompt(t *testing.T) {
	lookups := useCodexImageServer(t)
	inline := "data:image/png;base64," + base64.StdEncoding.EncodeToString(codexTestPNG)
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	writeCodexRPCFixture(t, bin, "ephemeral")
	t.Setenv("TEST_LOG", filepath.Join(dir, "calls"))
	history := &ConversationContext{Source: "core", SessionKey: "s", CurrentRunID: "run-inline", HistoryBeforeCurrent: []ConversationMessage{
		{RunID: "earlier", Role: "user", Content: "first order", Payload: map[string]any{"input": map[string]any{"image_url": inline}}},
	}}
	result, err := (CodexProvider{Config: ProviderConfig{Bin: bin, Workspace: dir, EnvAllowlist: []string{"TEST_LOG"}, CodexImageInput: true}}).Run(
		context.Background(), RunContext{RunID: "run-inline", Input: map[string]any{"image_url": inline, "question": "real or AI?"}, Conversation: history})
	if err != nil || result.Output.(map[string]any)["summary"] != "final answer" {
		t.Fatalf("result %+v %v", result, err)
	}
	if lookups.Load() != 0 {
		t.Fatal("inline image used the network")
	}
	images, _ := os.ReadFile(filepath.Join(dir, "calls.images"))
	if fields := strings.Fields(string(images)); len(fields) != 3 || fields[1] != strconv.Itoa(len(codexTestPNG)) || fields[2] != "true" {
		t.Fatalf("inline image not sent: %q", images)
	}
	prompt, _ := os.ReadFile(filepath.Join(dir, "calls.prompt"))
	if strings.Contains(string(prompt), base64.StdEncoding.EncodeToString(codexTestPNG)[:40]) || strings.Contains(string(prompt), "data:image/") {
		t.Fatal("base64 image data reached the prompt text")
	}
	if strings.Count(string(prompt), "inline image omitted") != 2 || !strings.Contains(string(prompt), "real or AI?") {
		t.Fatalf("prompt: %s", prompt)
	}
}
