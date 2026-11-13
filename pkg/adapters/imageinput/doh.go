package imageinput

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	dohMediaType      = "application/dns-message"
	maxDoHMessageSize = 64 << 10
	dohQueryTimeout   = 5 * time.Second
)

// ValidateDoHURL accepts only https://<public IP>[:443]/<path> without
// credentials, query or fragment. An IP endpoint avoids resolving the
// resolver itself through the host DNS that DoH is meant to replace.
func ValidateDoHURL(raw string) (*url.URL, netip.Addr, error) {
	invalid := errors.New("image input DoH URL must be https://<public IP>/<path> on port 443")
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || parsed.Opaque != "" || parsed.Path == "" || parsed.Path == "/" {
		return nil, netip.Addr{}, invalid
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return nil, netip.Addr{}, invalid
	}
	address, err := netip.ParseAddr(parsed.Hostname())
	if err != nil || Denied(address) {
		return nil, netip.Addr{}, invalid
	}
	return parsed, address.Unmap(), nil
}

// DoHResolver resolves image hosts through one DNS-over-HTTPS endpoint. It
// only supplies candidate addresses: the Fetcher still rejects every denied
// address and pins the connection, and never falls back to host DNS.
type DoHResolver struct {
	endpoint *url.URL
	address  netip.Addr
	// Dial and TLS are test seams with the same restrictions as Fetcher's.
	Dial func(ctx context.Context, network string, address netip.AddrPort) (net.Conn, error)
	TLS  *tls.Config
}

func NewDoHResolver(raw string) (*DoHResolver, error) {
	endpoint, address, err := ValidateDoHURL(raw)
	if err != nil {
		return nil, err
	}
	return &DoHResolver{endpoint: endpoint, address: address}, nil
}

// Resolve returns the A and AAAA records for host. Any transport, protocol or
// format problem fails the whole lookup instead of returning a partial view.
func (r *DoHResolver) Resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	name, err := dnsmessage.NewName(strings.TrimSuffix(host, ".") + ".")
	if err != nil || host == "" {
		return nil, errors.New("invalid host name")
	}
	dial := r.Dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: dohQueryTimeout}
		dial = func(ctx context.Context, network string, address netip.AddrPort) (net.Conn, error) {
			return dialer.DialContext(ctx, network, address.String())
		}
	}
	target := netip.AddrPortFrom(r.address, 443)
	transport := &http.Transport{
		Proxy: nil,
		// Connect only to the configured resolver address.
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dial(ctx, network, target)
		},
		TLSClientConfig:     verifiedTLS(r.TLS),
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: dohQueryTimeout,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("DoH redirects are not followed")
	}}
	var addresses []netip.Addr
	for _, kind := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
		found, err := r.query(ctx, client, name, kind)
		if err != nil {
			return nil, err
		}
		addresses = append(addresses, found...)
	}
	if len(addresses) == 0 {
		return nil, errors.New("no addresses")
	}
	return addresses, nil
}

func (r *DoHResolver) query(ctx context.Context, client *http.Client, name dnsmessage.Name, kind dnsmessage.Type) ([]netip.Addr, error) {
	question := dnsmessage.Question{Name: name, Type: kind, Class: dnsmessage.ClassINET}
	// RFC 8484 recommends ID 0 for HTTP caching.
	message := dnsmessage.Message{Header: dnsmessage.Header{RecursionDesired: true}, Questions: []dnsmessage.Question{question}}
	packed, err := message.Pack()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, dohQueryTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint.String(), bytes.NewReader(packed))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", dohMediaType)
	request.Header.Set("Accept", dohMediaType)
	request.Header.Set("User-Agent", userAgent)
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("DoH request failed: %w", err)
	}
	defer response.Body.Close()
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || mediaType != dohMediaType {
		return nil, errors.New("DoH server returned an invalid response")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDoHMessageSize+1))
	if err != nil || len(body) > maxDoHMessageSize {
		return nil, errors.New("DoH response is unreadable or too large")
	}
	var parser dnsmessage.Parser
	header, err := parser.Start(body)
	if err != nil || !header.Response || header.ID != 0 || header.Truncated {
		return nil, errors.New("DoH response is not a matching DNS answer")
	}
	questions, err := parser.AllQuestions()
	if err != nil || len(questions) != 1 || !strings.EqualFold(questions[0].Name.String(), name.String()) ||
		questions[0].Type != kind || questions[0].Class != dnsmessage.ClassINET {
		return nil, errors.New("DoH response does not answer the query")
	}
	switch header.RCode {
	case dnsmessage.RCodeSuccess:
	case dnsmessage.RCodeNameError:
		return nil, nil
	default:
		return nil, errors.New("DoH server could not resolve the host")
	}
	var addresses []netip.Addr
	for {
		answer, err := parser.AnswerHeader()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}
		if err != nil {
			return nil, errors.New("DoH response has an invalid answer")
		}
		switch {
		case answer.Type == dnsmessage.TypeA && kind == dnsmessage.TypeA:
			record, err := parser.AResource()
			if err != nil {
				return nil, errors.New("DoH response has an invalid answer")
			}
			addresses = append(addresses, netip.AddrFrom4(record.A))
		case answer.Type == dnsmessage.TypeAAAA && kind == dnsmessage.TypeAAAA:
			record, err := parser.AAAAResource()
			if err != nil {
				return nil, errors.New("DoH response has an invalid answer")
			}
			addresses = append(addresses, netip.AddrFrom16(record.AAAA))
		default:
			// CNAME and other records are followed by the recursive resolver.
			if err := parser.SkipAnswer(); err != nil {
				return nil, errors.New("DoH response has an invalid answer")
			}
		}
	}
	return addresses, nil
}
