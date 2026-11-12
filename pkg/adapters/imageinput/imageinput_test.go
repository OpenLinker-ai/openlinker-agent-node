package imageinput

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
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
)

var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 64)...)

const publicAddress = "93.184.216.34"

// testFetcher resolves every host to the given addresses, then dials the
// local TLS server regardless of the validated address. The httptest
// certificate is valid for example.com.
func testFetcher(t *testing.T, server *httptest.Server, addresses map[string][]string) (Fetcher, *[]string) {
	t.Helper()
	var dialed []string
	target := server.Listener.Addr().String()
	pool := server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	return Fetcher{
		Resolve: func(_ context.Context, host string) ([]netip.Addr, error) {
			values, ok := addresses[host]
			if !ok {
				values = []string{publicAddress}
			}
			var result []netip.Addr
			for _, value := range values {
				result = append(result, netip.MustParseAddr(value))
			}
			return result, nil
		},
		Dial: func(ctx context.Context, network string, address netip.AddrPort) (net.Conn, error) {
			dialed = append(dialed, address.String())
			return (&net.Dialer{}).DialContext(ctx, network, target)
		},
		TLS: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}, &dialed
}

func expectCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *Error
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
	if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "10.0.0.") {
		t.Fatalf("error leaks an address: %v", err)
	}
}

func assertNoInputFiles(t *testing.T, workspace string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(workspace, DirectoryName))
	if err == nil && len(entries) != 0 {
		t.Fatalf("leftover input files: %v", entries)
	}
}

func TestFieldsUseOnlyExplicitTopLevelNames(t *testing.T) {
	fields, err := Fields(map[string]any{
		"image_url": "https://example.com/a.png", "reference_image_url": "https://example.com/b.png",
		"_image_url": "https://example.com/c.png", "url": "https://example.com/d.png",
		"nested": map[string]any{"image_url": "https://example.com/e.png"}, "list_image_url": []any{"x"},
		"blank_image_url": "  ", "text": "see https://example.com/f.png",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 || fields[0].Name != "image_url" || fields[1].Name != "reference_image_url" {
		t.Fatalf("fields = %+v", fields)
	}
	for _, input := range []any{nil, "https://example.com/a.png", []any{"x"}, 3} {
		if fields, err := Fields(input); err != nil || len(fields) != 0 {
			t.Fatalf("%v selected %+v %v", input, fields, err)
		}
	}
	tooMany := map[string]any{}
	for i := 0; i <= MaxImages; i++ {
		tooMany["n"+strconv.Itoa(i)+"_image_url"] = "https://example.com/a.png"
	}
	_, err = Fields(tooMany)
	expectCode(t, err, CodeInvalid)
}

func TestInvalidLinksAreRejectedBeforeNetwork(t *testing.T) {
	called := false
	fetcher := Fetcher{Resolve: func(context.Context, string) ([]netip.Addr, error) {
		called = true
		return nil, errors.New("unexpected")
	}}
	for _, link := range []string{
		"http://example.com/a.png", "ftp://example.com/a.png", "https://user:pw@example.com/a.png",
		"https://example.com:8443/a.png", "/relative.png", "https:///a.png",
		"https://example.com/" + strings.Repeat("a", maxURLLength),
	} {
		workspace := t.TempDir()
		_, err := fetcher.Materialize(context.Background(), workspace, []Field{{Name: "image_url", URL: link}})
		expectCode(t, err, CodeInvalid)
		if _, statErr := os.Stat(filepath.Join(workspace, DirectoryName)); !os.IsNotExist(statErr) {
			t.Fatalf("%s created input directory", link)
		}
	}
	if called {
		t.Fatal("invalid links reached the resolver")
	}
}

func TestDeniedAddresses(t *testing.T) {
	for _, value := range []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1",
		"0.0.0.0", "224.0.0.1", "240.0.0.1", "255.255.255.255", "198.18.0.1",
		"::1", "::", "fe80::1", "fc00::1", "fd00::1", "ff02::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1",
		"64:ff9b::a00:1", "2002:a00:1::1",
	} {
		if !Denied(netip.MustParseAddr(value)) {
			t.Errorf("%s must be denied", value)
		}
	}
	for _, value := range []string{"93.184.216.34", "8.8.8.8", "2606:4700:4700::1111"} {
		if Denied(netip.MustParseAddr(value)) {
			t.Errorf("%s must be allowed", value)
		}
	}
}

func TestSSRFAddressesAreBlocked(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Write(pngBytes)
	}))
	defer server.Close()
	cases := map[string][]string{
		"loopback.example":  {"127.0.0.1"},
		"private.example":   {"10.0.0.5"},
		"metadata.example":  {"169.254.169.254"},
		"mixed.example":     {publicAddress, "192.168.0.10"},
		"mapped.example":    {"::ffff:127.0.0.1"},
		"multicast.example": {"224.0.0.251"},
	}
	for host := range cases {
		fetcher, dialed := testFetcher(t, server, cases)
		workspace := t.TempDir()
		_, err := fetcher.Materialize(context.Background(), workspace, []Field{{Name: "image_url", URL: "https://" + host + "/a.png"}})
		expectCode(t, err, CodeBlocked)
		if len(*dialed) != 0 {
			t.Fatalf("%s dialed %v", host, *dialed)
		}
		assertNoInputFiles(t, workspace)
	}
	for _, literal := range []string{"https://127.0.0.1/a.png", "https://[::1]/a.png", "https://169.254.169.254/a.png"} {
		fetcher, _ := testFetcher(t, server, nil)
		_, err := fetcher.Materialize(context.Background(), t.TempDir(), []Field{{Name: "image_url", URL: literal}})
		expectCode(t, err, CodeBlocked)
	}
	if requests.Load() != 0 {
		t.Fatal("blocked hosts reached the server")
	}
}

func TestDownloadDialsOnlyValidatedAddressWithoutCredentials(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Referer") != "" {
			t.Errorf("credential-bearing header sent: %v", r.Header)
		}
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("user agent = %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "text/plain") // the signature decides the type
		w.Write(pngBytes)
	}))
	defer server.Close()
	fetcher, dialed := testFetcher(t, server, nil)
	workspace := t.TempDir()
	set, err := fetcher.Materialize(context.Background(), workspace, []Field{{Name: "image_url", URL: "https://example.com/photo.exe"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(*dialed) != 1 || (*dialed)[0] != publicAddress+":443" {
		t.Fatalf("dialed %v", *dialed)
	}
	if len(set.Images) != 1 || set.Images[0].MediaType != "image/png" || !strings.HasSuffix(set.Images[0].Path, "image-1.png") {
		t.Fatalf("images = %+v", set.Images)
	}
	image := set.Images[0]
	if !strings.HasPrefix(image.Path, workspace) || image.Relative != filepath.ToSlash(strings.TrimPrefix(image.Path, workspace+string(filepath.Separator))) {
		t.Fatalf("path %s not inside workspace", image.Path)
	}
	info, err := os.Stat(image.Path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode: %v %v", info, err)
	}
	if dir, _ := os.Stat(filepath.Dir(image.Path)); dir.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode %v", dir.Mode())
	}
	if data, _ := os.ReadFile(image.Path); !bytes.Equal(data, pngBytes) {
		t.Fatal("content mismatch")
	}
	if !strings.Contains(set.Instructions(), "image_url: "+image.Relative) {
		t.Fatalf("instructions: %s", set.Instructions())
	}
	if err := set.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, DirectoryName)); !os.IsNotExist(err) {
		t.Fatal("input directory not removed")
	}
	if err := set.Cleanup(); err != nil {
		t.Fatal("second cleanup failed")
	}
}

func TestRedirectsAreRevalidatedEveryHop(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-private":
			http.Redirect(w, r, "https://internal.example/a.png", http.StatusFound)
		case "/to-http":
			http.Redirect(w, r, "http://example.com/a.png", http.StatusFound)
		case "/to-port":
			http.Redirect(w, r, "https://example.com:8443/a.png", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/two":
			http.Redirect(w, r, "/one", http.StatusFound)
		case "/one":
			http.Redirect(w, r, "/a.png", http.StatusFound)
		default:
			w.Write(pngBytes)
		}
	}))
	defer server.Close()
	addresses := map[string][]string{"internal.example": {"10.0.0.7"}}
	for path, code := range map[string]string{
		"/to-private": CodeBlocked, "/to-http": CodeFetchFailed, "/to-port": CodeInvalid, "/loop": CodeFetchFailed,
	} {
		fetcher, _ := testFetcher(t, server, addresses)
		workspace := t.TempDir()
		_, err := fetcher.Materialize(context.Background(), workspace, []Field{{Name: "image_url", URL: "https://example.com" + path}})
		expectCode(t, err, code)
		assertNoInputFiles(t, workspace)
	}
	fetcher, dialed := testFetcher(t, server, addresses)
	set, err := fetcher.Materialize(context.Background(), t.TempDir(), []Field{{Name: "image_url", URL: "https://example.com/two"}})
	if err != nil {
		t.Fatal(err)
	}
	defer set.Cleanup()
	if len(*dialed) != 3 {
		t.Fatalf("each hop must dial a validated address: %v", *dialed)
	}
}

func TestOversizedImagesAreRejected(t *testing.T) {
	big := append(append([]byte{}, pngBytes...), bytes.Repeat([]byte{0}, 2048)...)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chunked" {
			w.Write(big[:100]) // no Content-Length: streamed
			w.(http.Flusher).Flush()
			w.Write(big[100:])
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(big)))
		w.Write(big)
	}))
	defer server.Close()
	for _, path := range []string{"/declared", "/chunked"} {
		fetcher, _ := testFetcher(t, server, nil)
		fetcher.MaxBytes = 1024
		workspace := t.TempDir()
		_, err := fetcher.Materialize(context.Background(), workspace, []Field{{Name: "image_url", URL: "https://example.com" + path}})
		expectCode(t, err, CodeTooLarge)
		assertNoInputFiles(t, workspace)
	}
}

func TestForgedTypesAndFailuresAreRejected(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/html":
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("<html><script>alert(1)</script></html>"))
		case "/svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Write([]byte("<svg xmlns='http://www.w3.org/2000/svg'/>"))
		case "/missing":
			http.NotFound(w, r)
		case "/slow":
			time.Sleep(300 * time.Millisecond)
			w.Write(pngBytes)
		}
	}))
	defer server.Close()
	for path, code := range map[string]string{
		"/html": CodeUnsupportedType, "/svg": CodeUnsupportedType, "/missing": CodeFetchFailed, "/slow": CodeFetchFailed,
	} {
		fetcher, _ := testFetcher(t, server, nil)
		fetcher.Timeout = 100 * time.Millisecond
		workspace := t.TempDir()
		_, err := fetcher.Materialize(context.Background(), workspace, []Field{{Name: "image_url", URL: "https://example.com" + path}})
		expectCode(t, err, code)
		assertNoInputFiles(t, workspace)
	}
	for data, want := range map[string]string{
		"\xff\xd8\xff\xe0": "image/jpeg", "GIF89a....": "image/gif", "RIFF\x00\x00\x00\x00WEBPVP8 ": "image/webp",
		"RIFF\x00\x00\x00\x00AVI ": "", "\x89PNG": "", "BM......": "",
	} {
		if got := Sniff([]byte(data)); got != want {
			t.Errorf("Sniff(%q) = %q, want %q", data, got, want)
		}
	}
}

func TestSymlinkedInputDirectoryIsRejected(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, DirectoryName)); err != nil {
		t.Skip(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(pngBytes) }))
	defer server.Close()
	fetcher, _ := testFetcher(t, server, nil)
	if _, err := fetcher.Materialize(context.Background(), workspace, []Field{{Name: "image_url", URL: "https://example.com/a.png"}}); err == nil {
		t.Fatal("symlinked input directory accepted")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("wrote outside the workspace: %v", entries)
	}
}
