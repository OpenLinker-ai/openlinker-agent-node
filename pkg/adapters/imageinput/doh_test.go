package imageinput

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// testPKI issues a CA and a leaf certificate for the given names and IPs.
func testPKI(t *testing.T, dnsNames []string, ips []net.IP) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "leaf"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), DNSNames: dnsNames, IPAddresses: ips, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}, pool
}

type dohAnswer struct {
	a, aaaa []string
	rcode   dnsmessage.RCode
	mutate  func(*dnsmessage.Message)
	status  int
	media   string
}

// fakeDoH serves RFC 8484 POST queries with a certificate for 1.1.1.1.
func fakeDoH(t *testing.T, answers map[string]dohAnswer) (*DoHResolver, *atomic.Int32) {
	t.Helper()
	cert, pool := testPKI(t, nil, []net.IP{net.ParseIP("1.1.1.1")})
	var queries atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries.Add(1)
		body, _ := io.ReadAll(r.Body)
		var query dnsmessage.Message
		if r.Method != http.MethodPost || r.URL.Path != "/dns-query" || r.Header.Get("Content-Type") != dohMediaType || query.Unpack(body) != nil || len(query.Questions) != 1 {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		q := query.Questions[0]
		answer := answers[strings.TrimSuffix(q.Name.String(), ".")]
		if answer.status != 0 {
			w.WriteHeader(answer.status)
			return
		}
		reply := dnsmessage.Message{Header: dnsmessage.Header{ID: query.ID, Response: true, RCode: answer.rcode}, Questions: query.Questions}
		if q.Type == dnsmessage.TypeA {
			for _, value := range answer.a {
				reply.Answers = append(reply.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
					Body: &dnsmessage.AResource{A: netip.MustParseAddr(value).As4()}})
			}
		} else {
			for _, value := range answer.aaaa {
				reply.Answers = append(reply.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeAAAA, Class: dnsmessage.ClassINET, TTL: 60},
					Body: &dnsmessage.AAAAResource{AAAA: netip.MustParseAddr(value).As16()}})
			}
		}
		if answer.mutate != nil {
			answer.mutate(&reply)
		}
		packed, _ := reply.Pack()
		media := dohMediaType
		if answer.media != "" {
			media = answer.media
		}
		w.Header().Set("Content-Type", media)
		w.Write(packed)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	server.StartTLS()
	t.Cleanup(server.Close)
	resolver, err := NewDoHResolver("https://1.1.1.1/dns-query")
	if err != nil {
		t.Fatal(err)
	}
	resolver.TLS = &tls.Config{RootCAs: pool}
	resolver.Dial = func(ctx context.Context, network string, address netip.AddrPort) (net.Conn, error) {
		if address.Port() != 443 || Denied(address.Addr()) {
			t.Errorf("DoH dialed %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	return resolver, &queries
}

func TestValidateDoHURL(t *testing.T) {
	for _, good := range []string{"https://1.1.1.1/dns-query", "https://223.5.5.5:443/dns-query", "https://[2606:4700:4700::1111]/dns-query"} {
		if _, _, err := ValidateDoHURL(good); err != nil {
			t.Errorf("%s rejected: %v", good, err)
		}
	}
	for _, bad := range []string{
		"", "http://1.1.1.1/dns-query", "https://cloudflare-dns.com/dns-query", "https://1.1.1.1", "https://1.1.1.1/",
		"https://1.1.1.1:8443/dns-query", "https://u:p@1.1.1.1/dns-query", "https://1.1.1.1/dns-query?x=1",
		"https://127.0.0.1/dns-query", "https://10.0.0.1/dns-query", "https://198.18.0.1/dns-query", "https://[::1]/dns-query",
	} {
		if _, _, err := ValidateDoHURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestDoHResolvesAndFetcherStillValidates(t *testing.T) {
	resolver, queries := fakeDoH(t, map[string]dohAnswer{
		"example.com":     {a: []string{publicAddress}, aaaa: []string{"2606:4700:4700::1111"}},
		"private.example": {a: []string{"10.0.0.8"}},
		"fakeip.example":  {a: []string{"198.18.1.163"}},
		"missing.example": {rcode: dnsmessage.RCodeNameError},
	})
	addresses, err := resolver.Resolve(context.Background(), "example.com")
	if err != nil || len(addresses) != 2 || addresses[0] != netip.MustParseAddr(publicAddress) {
		t.Fatalf("addresses %v %v", addresses, err)
	}
	if queries.Load() != 2 {
		t.Fatalf("expected A and AAAA queries, got %d", queries.Load())
	}
	image := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(pngBytes) }))
	defer image.Close()
	fetcher, dialed := testFetcher(t, image, nil)
	fetcher.Resolve = resolver.Resolve
	set, err := fetcher.Materialize(context.Background(), t.TempDir(), []Field{{Name: "image_url", URL: "https://example.com/a.png"}})
	if err != nil {
		t.Fatal(err)
	}
	set.Cleanup()
	if len(*dialed) != 1 || (*dialed)[0] != publicAddress+":443" {
		t.Fatalf("dialed %v", *dialed)
	}
	for host, code := range map[string]string{"private.example": CodeBlocked, "fakeip.example": CodeBlocked, "missing.example": CodeFetchFailed} {
		_, err := fetcher.Materialize(context.Background(), t.TempDir(), []Field{{Name: "image_url", URL: "https://" + host + "/a.png"}})
		expectCode(t, err, code)
	}
}

func TestDoHRejectsMalformedOrUntrustedAnswers(t *testing.T) {
	resolver, _ := fakeDoH(t, map[string]dohAnswer{
		"status.example":    {status: http.StatusBadGateway},
		"media.example":     {a: []string{publicAddress}, media: "text/html"},
		"servfail.example":  {rcode: dnsmessage.RCodeServerFailure},
		"truncated.example": {a: []string{publicAddress}, mutate: func(m *dnsmessage.Message) { m.Truncated = true }},
		"id.example":        {a: []string{publicAddress}, mutate: func(m *dnsmessage.Message) { m.ID = 7 }},
		"question.example": {a: []string{publicAddress}, mutate: func(m *dnsmessage.Message) {
			m.Questions[0].Name = dnsmessage.MustNewName("other.example.")
		}},
		"notresponse.example": {a: []string{publicAddress}, mutate: func(m *dnsmessage.Message) { m.Response = false }},
	})
	for _, host := range []string{"status.example", "media.example", "servfail.example", "truncated.example", "id.example", "question.example", "notresponse.example", "", strings.Repeat("a", 300)} {
		if addresses, err := resolver.Resolve(context.Background(), host); err == nil {
			t.Errorf("%q resolved to %v", host, addresses)
		}
	}
	// A resolver certificate from an untrusted CA or for another IP is refused.
	untrusted, _ := fakeDoH(t, map[string]dohAnswer{"example.com": {a: []string{publicAddress}}})
	untrusted.TLS = nil
	if _, err := untrusted.Resolve(context.Background(), "example.com"); err == nil {
		t.Error("untrusted DoH certificate accepted")
	}
	otherIP, _ := fakeDoH(t, map[string]dohAnswer{"example.com": {a: []string{publicAddress}}})
	otherIP.address = netip.MustParseAddr("8.8.8.8")
	otherIP.endpoint.Host = "8.8.8.8"
	if _, err := otherIP.Resolve(context.Background(), "example.com"); err == nil {
		t.Error("DoH certificate for another IP accepted")
	}
}

func TestImageTLSAlwaysVerifiesTheRequestHost(t *testing.T) {
	cert, pool := testPKI(t, []string{"example.com"}, nil)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(pngBytes) }))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	server.StartTLS()
	defer server.Close()
	fetcher, _ := testFetcher(t, server, nil)
	fetcher.TLS = &tls.Config{RootCAs: pool}
	set, err := fetcher.Materialize(context.Background(), t.TempDir(), []Field{{Name: "image_url", URL: "https://example.com/a.png"}})
	if err != nil {
		t.Fatalf("trusted certificate rejected: %v", err)
	}
	set.Cleanup()
	clientCert, _ := testPKI(t, []string{"node.internal"}, nil)
	for name, configure := range map[string]func(*Fetcher) string{
		"host mismatch": func(*Fetcher) string { return "https://other.example/a.png" },
		"untrusted CA": func(f *Fetcher) string {
			f.TLS = nil
			return "https://example.com/a.png"
		},
		"skip verify ignored": func(f *Fetcher) string {
			f.TLS = &tls.Config{InsecureSkipVerify: true}
			return "https://other.example/a.png"
		},
		"server name override ignored": func(f *Fetcher) string {
			f.TLS = &tls.Config{RootCAs: pool, ServerName: "example.com"}
			return "https://other.example/a.png"
		},
	} {
		f := fetcher
		link := configure(&f)
		_, err := f.Materialize(context.Background(), t.TempDir(), []Field{{Name: "image_url", URL: link}})
		expectCode(t, err, CodeFetchFailed)
		_ = name
	}
	// No client certificate (for example the Node's mTLS identity) is offered.
	requested := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) != 0 {
			t.Error("client certificate presented to an image host")
		}
		w.Write(pngBytes)
	}))
	requested.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequestClientCert}
	requested.StartTLS()
	defer requested.Close()
	f, _ := testFetcher(t, requested, nil)
	f.TLS = &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{clientCert}}
	set, err = f.Materialize(context.Background(), t.TempDir(), []Field{{Name: "image_url", URL: "https://example.com/a.png"}})
	if err != nil {
		t.Fatal(err)
	}
	set.Cleanup()
	if config := verifiedTLS(&tls.Config{MinVersion: tls.VersionTLS10, KeyLogWriter: os.Stderr}); config.MinVersion != tls.VersionTLS12 || config.KeyLogWriter != nil {
		t.Fatal("weak TLS settings survived")
	}
}
