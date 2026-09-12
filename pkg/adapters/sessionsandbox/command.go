package sessionsandbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/appfiles"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerprocess"
)

func resolveRuntime(bin string) (string, error) {
	if bin == "" {
		bin = "srt"
	}
	p, err := exec.LookPath(bin)
	if err != nil {
		return "", errors.New("native isolation requires installed @anthropic-ai/sandbox-runtime@" + RuntimeVersion + " (srt)")
	}
	p, err = filepath.EvalSymlinks(p)
	if err != nil {
		return "", errors.New("cannot resolve sandbox runtime")
	}
	// srt's --version is not its package version. Verify the installed release
	// layout instead; executable paths and packages are trusted operator inputs.
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(p), "..", "package.json"))
	var manifest struct{ Name, Version string }
	if err != nil || json.Unmarshal(raw, &manifest) != nil || manifest.Name != "@anthropic-ai/sandbox-runtime" || manifest.Version != RuntimeVersion || filepath.Base(p) != "cli.js" {
		return "", errors.New("native isolation requires the pinned sandbox-runtime package version " + RuntimeVersion)
	}
	return p, nil
}

func (s *Session) settings(bin string) (map[string]any, error) {
	read := []string{s.data, s.temp, bin}
	if runtime.GOOS == "linux" {
		arch := runtime.GOARCH
		if arch == "amd64" {
			arch = "x64"
		}
		// The final filtered init executes *inside* bwrap, so its exact pinned
		// executable must remain readable under a deny-root policy.
		helper := filepath.Clean(filepath.Join(filepath.Dir(s.runtimeBin), "..", "vendor", "seccomp", arch, "apply-seccomp"))
		if info, err := os.Stat(helper); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return nil, errors.New("pinned Linux seccomp helper is missing or not executable")
		}
		read = append(read, helper)
	}
	// Code and OS libraries only. Do not grant /usr/local, /opt, /Library,
	// /etc or a user's HOME wholesale just to make a client start.
	system := []string{"/bin", "/sbin", "/usr/bin", "/usr/sbin", "/usr/lib", "/usr/libexec", "/lib", "/lib64", "/etc/ssl/certs", "/etc/ssl/openssl.cnf", "/etc/hosts", "/etc/resolv.conf", "/etc/nsswitch.conf", "/etc/ld.so.cache", "/etc/localtime", "/dev/null", "/dev/urandom", "/dev/random"}
	if runtime.GOOS == "darwin" {
		system = append(system, "/System/Library", "/usr/share/icu", "/private/var/select/sh", "/private/etc/ssl/cert.pem")
	} else {
		// Debian/Ubuntu Node packages externalize built-in JS modules here.
		system = append(system, "/usr/share/nodejs", "/usr/share/icu")
	}
	for _, p := range system {
		read = append(read, p)
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			read = append(read, resolved)
		}
	}
	home, _ := os.UserHomeDir()
	if p, err := filepath.EvalSymlinks(home); err == nil {
		home = p
	}
	for _, p := range s.config.ReadPaths {
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil || !safePath(resolved) || contains(resolved, s.config.Root) || contains(s.config.Root, resolved) || contains(resolved, home) {
			return nil, errors.New("SESSION_READ_PATHS must exist and cannot expose HOME, session/control state or their ancestors")
		}
		read = append(read, resolved)
	}
	domains := make([]string, 0, len(s.config.AllowedDomains))
	for _, d := range s.config.AllowedDomains {
		domains = append(domains, strings.TrimSuffix(d, ":443")+":443")
	}
	return map[string]any{
		"filesystem": map[string]any{"denyRead": []string{"/"}, "allowRead": read, "allowWrite": []string{s.data, s.temp}, "denyWrite": []string{"/tmp/claude", "/private/tmp/claude", "/dev/tty", "/dev/dtracehelper", "/dev/autofs_nowait"}},
		"network": map[string]any{"allowedDomains": domains, "deniedDomains": []string{}, "strictAllowlist": true, "allowLocalBinding": false, "allowAllUnixSockets": false, "allowUnixSockets": []string{},
			"deniedResolvedAddresses": []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fc00::/7"}},
		"enableWeakerNestedSandbox": false, "enableWeakerNetworkIsolation": false, "allowAppleEvents": false,
	}, nil
}

func (s *Session) Command(ctx context.Context, bin string, args, environment []string) (*exec.Cmd, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.lock == nil {
		return nil, errors.New("native session sandbox is closed")
	}
	p, err := exec.LookPath(bin)
	if err != nil {
		return nil, errors.New("native provider executable is unavailable")
	}
	p, err = filepath.EvalSymlinks(p)
	if err != nil || !safePath(p) {
		return nil, errors.New("cannot resolve native provider executable")
	}
	settings, err := s.settings(p)
	if err != nil {
		return nil, err
	}
	if err := appfiles.WritePrivateJSON(s.policy, settings); err != nil {
		return nil, errors.New("cannot write native sandbox policy")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return nil, errors.New("native sandbox runtime requires Node.js 20.11 or newer")
	}
	argv := []string{filepath.Join(s.control, "runner.mjs"), s.runtimeBin, s.policy, p}
	argv = append(argv, args...)
	command := exec.CommandContext(ctx, node, argv...)
	command.Dir = s.Workspace()
	command.Env = s.Environment(environment)
	providerprocess.Configure(command)
	return command, nil
}

// Probe executes an actual denied read and permitted write under the backend.
// Merely finding srt or compiling a policy is insufficient startup evidence.
func Probe(ctx context.Context, c Config) error {
	s, err := Open(ctx, c, Scope("startup-probe"))
	if err != nil {
		return err
	}
	defer s.Close()
	cmd, err := s.Command(ctx, "/bin/sh", []string{"-c", `if cat "$1" >/dev/null 2>&1; then exit 91; fi; echo verified > "$2"; test "$(cat "$2")" = verified`, "probe", s.policy, filepath.Join(s.Workspace(), "probe")}, []string{"PATH=" + os.Getenv("PATH")})
	if err != nil {
		return err
	}
	if err := cmd.Run(); err != nil {
		return errors.New("native sandbox enforcement probe failed; check runtime/OS dependencies and kernel sandbox support (no fallback)")
	}
	return nil
}
