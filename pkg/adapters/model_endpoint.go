package adapters

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// ValidateModelEndpoint validates an explicitly configured model API base URL.
// It never resolves a name or contacts the endpoint, and diagnostics omit the
// supplied value because mistaken URLs can contain credentials.
func ValidateModelEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		strings.ContainsAny(raw, "\\%?# \t\r\n\x00") || u.Port() != "" && u.Port() != "443" {
		return errors.New("model base URL must be HTTPS on port 443, without credentials, query, fragment, escapes or whitespace")
	}
	host := strings.ToLower(u.Hostname())
	if len(host) > 253 || strings.HasSuffix(u.Host, ":") || net.ParseIP(host) != nil || !strings.Contains(host, ".") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".localhost") {
		return errors.New("model base URL requires an exact DNS hostname")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return errors.New("invalid model base URL hostname")
		}
		for _, ch := range label {
			if ch != '-' && (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') {
				return errors.New("invalid model base URL hostname")
			}
		}
	}
	for _, segment := range strings.Split(strings.TrimPrefix(strings.TrimSuffix(u.Path, "/"), "/"), "/") {
		if segment == "." || segment == ".." || segment == "" && u.Path != "" && u.Path != "/" {
			return errors.New("model base URL requires a canonical API path")
		}
	}
	return nil
}

func validateIsolatedModelEndpoint(c ProviderConfig) error {
	endpoint := c.CodexBaseURL
	if c.Provider == "claude" {
		endpoint = c.ClaudeBaseURL
	}
	if endpoint == "" {
		return nil // Existing explicitly offline sandbox fixtures remain usable.
	}
	if err := ValidateModelEndpoint(endpoint); err != nil {
		return err
	}
	u, _ := url.Parse(endpoint)
	for _, domain := range c.SessionIsolation.AllowedDomains {
		if strings.TrimSuffix(domain, ":443") == strings.ToLower(u.Hostname()) {
			return nil
		}
	}
	return errors.New("model base URL hostname must be explicitly included in SESSION_NETWORK_DOMAINS")
}
