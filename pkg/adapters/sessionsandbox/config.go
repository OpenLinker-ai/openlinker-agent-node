// Package sessionsandbox isolates an entire native client process, including
// its tools, using the OS sandbox through a pinned Anthropic sandbox runtime.
// It does not start a Worker or infer identity from model-controlled input.
// Experimental: this package/configuration has no stable compatibility promise.
package sessionsandbox

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const RuntimeVersion = "0.0.76"

// Config is experimental and opt-in. Existing native workspaces and personal client histories
// are never imported. Namespace must identify the trusted Core installation.
type Config struct {
	Mode, Root, Namespace, RuntimeBin string
	TempRoot                          string
	ReadPaths, AllowedDomains         []string
}

func (c Config) Enabled() bool { return c.Mode == "native" }

func (c Config) Validate() error {
	if c.Mode == "" || c.Mode == "off" {
		if c.Root != "" || c.TempRoot != "" || c.RuntimeBin != "" || len(c.ReadPaths)+len(c.AllowedDomains) != 0 {
			return errors.New("session options require SESSION_ISOLATION=native")
		}
		return nil
	}
	if !c.Enabled() {
		return errors.New("SESSION_ISOLATION must be off or native")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return errors.New("native session isolation requires macOS or Linux; no unsandboxed fallback is available")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return errors.New("native session isolation currently requires amd64 or arm64")
	}
	if os.Geteuid() <= 0 {
		return errors.New("native session isolation requires a non-root Node user")
	}
	if !safePath(c.Root) || filepath.Clean(c.Root) == "/" || strings.TrimSpace(c.Namespace) == "" {
		return errors.New("native isolation requires an absolute private SESSION_ROOT and stable Core namespace")
	}
	if c.TempRoot != "" && (!safePath(c.TempRoot) || filepath.Clean(c.TempRoot) == "/") {
		return errors.New("SESSION_TEMP_ROOT must be an absolute private directory")
	}
	for _, p := range c.ReadPaths {
		if !safePath(p) {
			return errors.New("SESSION_READ_PATHS must contain absolute paths without glob or control characters")
		}
	}
	for _, d := range c.AllowedDomains {
		// Exact DNS names, optionally with :443. No wildcards, IP literals,
		// local endpoints or arbitrary destination ports in the initial policy.
		host := strings.TrimSuffix(d, ":443")
		if d == "" || host != strings.ToLower(host) || !strings.Contains(host, ".") ||
			strings.ContainsAny(host, ":/*?@[]\\ \t\r\n\x00") || net.ParseIP(host) != nil ||
			strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
			return errors.New("SESSION_NETWORK_DOMAINS must contain exact public DNS names (HTTPS port 443 only)")
		}
		for _, label := range strings.Split(host, ".") {
			if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return errors.New("invalid SESSION_NETWORK_DOMAINS DNS name")
			}
			for _, ch := range label {
				if ch != '-' && (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') {
					return errors.New("invalid SESSION_NETWORK_DOMAINS DNS name")
				}
			}
		}
	}
	return nil
}

func safePath(p string) bool {
	return filepath.IsAbs(p) && !strings.ContainsAny(p, "\x00\r\n\t*?[]{}")
}

func contains(parent, child string) bool {
	r, err := filepath.Rel(parent, child)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}
