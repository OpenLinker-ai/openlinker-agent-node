// Package sessionsandbox launches an entire client process in a Linux Docker
// container with one persistent session mount. It never starts a Runtime Worker.
// The caller supplies a scope derived from trusted platform authority, not input.
package sessionsandbox

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const Workspace = "/session/workspace"

// Config is an explicit opt-in. An empty Mode preserves the native launch path.
// Namespace separates Core installations. Root is private Node control state;
// only its generated per-session data child is ever mounted into a container.
type Config struct {
	Mode      string
	Root      string
	Image     string
	Network   string
	Namespace string
}

var digestImage = regexp.MustCompile(`^(?:[a-zA-Z0-9][a-zA-Z0-9._:/-]*@)?sha256:[a-f0-9]{64}$`)
var bridgeNetwork = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func (config Config) Enabled() bool { return config.Mode == "docker" }

func (config Config) Validate() error {
	if config.Mode == "" || config.Mode == "off" {
		if config.Root != "" || config.Image != "" || config.Network != "" {
			return errors.New("session sandbox options require SESSION_ISOLATION=docker")
		}
		return nil
	}
	if !config.Enabled() {
		return errors.New("SESSION_ISOLATION must be off or docker")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return errors.New("Docker session isolation currently requires a Linux or macOS Node host")
	}
	if os.Geteuid() <= 0 || os.Getegid() < 0 {
		return errors.New("Docker session isolation requires running Agent Node as a non-root OS user")
	}
	if !filepath.IsAbs(config.Root) || strings.ContainsAny(config.Root, ",\r\n\x00") {
		return errors.New("SESSION_ROOT must be an absolute private directory without commas or control characters")
	}
	if !digestImage.MatchString(config.Image) {
		return errors.New("SESSION_IMAGE must be a local sha256 image ID or repository@sha256 digest")
	}
	if strings.TrimSpace(config.Namespace) == "" {
		return errors.New("session isolation requires a stable Core connection namespace")
	}
	if network := config.Network; network != "" && network != "none" {
		if !bridgeNetwork.MatchString(network) || network == "host" || network == "default" {
			return errors.New("SESSION_NETWORK must be none or an explicitly selected Docker bridge network")
		}
	}
	return nil
}
