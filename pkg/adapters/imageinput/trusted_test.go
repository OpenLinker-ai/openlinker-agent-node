package imageinput

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestParseOrigin(t *testing.T) {
	for raw, want := range map[string]Origin{
		"https://files.corp":             {"https", "files.corp", 443},
		"HTTP://Files.Corp.:9000/":       {"http", "files.corp", 9000},
		"http://10.20.0.5":               {"http", "10.20.0.5", 80},
		"https://[::ffff:10.0.0.1]:8443": {"https", "10.0.0.1", 8443},
	} {
		got, err := ParseOrigin(raw)
		if err != nil || got != want {
			t.Errorf("%s = %+v %v, want %+v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "files.corp", "ftp://files.corp", "https://u:p@files.corp", "https://files.corp/images",
		"https://files.corp?x=1", "https://*.corp", "https://files.corp:0", "https://files.corp:99999"} {
		if _, err := ParseOrigin(raw); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
}

func mustOrigins(t *testing.T, values ...string) []Origin {
	t.Helper()
	var origins []Origin
	for _, value := range values {
		origin, err := ParseOrigin(value)
		if err != nil {
			t.Fatal(err)
		}
		origins = append(origins, origin)
	}
	return origins
}

func TestTrustedOriginsAllowPrivateSourcesOnly(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-untrusted-private":
			http.Redirect(w, r, "https://internal.example/a.png", http.StatusFound)
		case "/to-public":
			http.Redirect(w, r, "https://other.example.com/a.png", http.StatusFound)
		default:
			w.Write(pngBytes)
		}
	}))
	defer server.Close()
	addresses := map[string][]string{"example.com": {"10.20.0.5"}, "internal.example": {"10.0.0.9"}, "metadata.example": {"169.254.169.254"}}
	newFetcher := func(only bool) (Fetcher, *[]string, *int) {
		fetcher, dialed := testFetcher(t, server, nil)
		publicLookups := 0
		fetcher.Resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
			publicLookups++
			values := addresses[host]
			if values == nil {
				values = []string{publicAddress}
			}
			var result []netip.Addr
			for _, value := range values {
				result = append(result, netip.MustParseAddr(value))
			}
			return result, nil
		}
		trustedResolve := fetcher.Resolve
		fetcher.TrustedResolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
			publicLookups-- // counted separately below
			return trustedResolve(ctx, host)
		}
		fetcher.Trusted = mustOrigins(t, "https://example.com", "https://metadata.example")
		fetcher.TrustedOnly = only
		return fetcher, dialed, &publicLookups
	}
	fetch := func(f Fetcher, link string) error {
		set, err := f.Materialize(context.Background(), t.TempDir(), []Field{{Name: "image_url", URL: link}})
		if err == nil {
			set.Cleanup()
		}
		return err
	}

	fetcher, dialed, lookups := newFetcher(false)
	if err := fetch(fetcher, "https://example.com/a.png"); err != nil {
		t.Fatalf("trusted private source rejected: %v", err)
	}
	if len(*dialed) != 1 || (*dialed)[0] != "10.20.0.5:443" || *lookups != 0 {
		t.Fatalf("trusted host must use the trusted resolver and private address: %v lookups=%d", *dialed, *lookups)
	}
	expectCode(t, fetch(fetcher, "https://metadata.example/a.png"), CodeBlocked)
	expectCode(t, fetch(fetcher, "https://internal.example/a.png"), CodeBlocked)
	expectCode(t, fetch(fetcher, "https://example.com/to-untrusted-private"), CodeBlocked)
	if err := fetch(fetcher, "https://example.com/to-public"); err != nil {
		t.Fatalf("public redirect from a trusted source rejected: %v", err)
	}
	expectCode(t, fetch(fetcher, "https://example.com:8443/a.png"), CodeInvalid)

	only, _, _ := newFetcher(true)
	if err := fetch(only, "https://example.com/a.png"); err != nil {
		t.Fatal(err)
	}
	expectCode(t, fetch(only, "https://other.example.com/a.png"), CodeBlocked)
	expectCode(t, fetch(only, "http://other.example.com/a.png"), CodeBlocked)
	expectCode(t, fetch(only, "https://example.com/to-public"), CodeBlocked)
}

func TestTrustedHTTPOriginWithPort(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(pngBytes) }))
	defer plain.Close()
	var dialed []string
	fetcher := Fetcher{
		Trusted: mustOrigins(t, "http://files.corp:9000"),
		TrustedResolve: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("192.168.10.4")}, nil
		},
		Dial: func(ctx context.Context, network string, address netip.AddrPort) (net.Conn, error) {
			dialed = append(dialed, address.String())
			return (&net.Dialer{}).DialContext(ctx, network, plain.Listener.Addr().String())
		},
	}
	set, err := fetcher.Materialize(context.Background(), t.TempDir(), []Field{{Name: "image_url", URL: "http://files.corp:9000/a.png"}})
	if err != nil {
		t.Fatal(err)
	}
	set.Cleanup()
	if len(dialed) != 1 || dialed[0] != "192.168.10.4:9000" {
		t.Fatalf("dialed %v", dialed)
	}
	for _, link := range []string{"http://files.corp/a.png", "https://files.corp:9000/a.png", "http://files.corp:9001/a.png"} {
		if _, err := fetcher.Materialize(context.Background(), t.TempDir(), []Field{{Name: "image_url", URL: link}}); err == nil {
			t.Errorf("%s accepted outside the exact trusted origin", link)
		}
	}
}
