package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerprocess"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/sessionsandbox"
)

// Native clients are trusted credential owners. Only their model-controlled
// tools run inside the client's OS sandbox; Node never parses login tokens.
type nativeToolPolicy struct {
	codex           []string
	codexFilesystem map[string]string
	claude          string
}

func nativeClaudeTools(c ProviderConfig) []string {
	// File tools execute in the authenticated host client, outside the OS
	// sandbox. An operator may opt in for trusted workloads explicitly.
	tools := []string{"Bash"}
	if len(c.AllowedTools) > 0 {
		tools = append([]string(nil), c.AllowedTools...)
	}
	if c.WebSearch {
		tools = appendUniqueString(tools, "WebSearch")
	}
	return tools
}

func nativeClientEnvironment(c ProviderConfig) ([]string, error) {
	env := c.Env
	if env == nil {
		env = os.Environ()
	}
	allowed := map[string]bool{}
	for _, k := range []string{"HOME", "PATH", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "SSL_CERT_FILE"} {
		allowed[k] = true
	}
	keys := []string{"CODEX_HOME", "CODEX_API_KEY", "OPENAI_API_KEY", "CODEX_CA_CERTIFICATE"}
	if c.Provider == "claude" {
		keys = []string{"CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "NODE_EXTRA_CA_CERTS"}
	}
	for _, k := range keys {
		allowed[k] = true
	}
	values := map[string]string{}
	for _, item := range env {
		k, v, ok := strings.Cut(item, "=")
		if ok && allowed[k] {
			if strings.ContainsAny(v, "\r\n\x00") {
				return nil, errors.New("invalid native client environment")
			}
			values[k] = v
		}
	}
	// Explicit empty environments must not fall back to the operator's account.
	if !filepath.IsAbs(values["HOME"]) {
		return nil, errors.New("host-auth isolation requires the existing client's absolute HOME")
	}
	if values["PATH"] == "" {
		values["PATH"] = "/usr/bin:/bin:/usr/sbin:/sbin"
	}
	for _, k := range []string{"CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		if v := values[k]; v != "" && !filepath.IsAbs(v) {
			return nil, errors.New("native client configuration directories must be absolute")
		}
	}
	if c.Provider == "claude" && c.ClaudeBaseURL != "" {
		values["ANTHROPIC_BASE_URL"] = c.ClaudeBaseURL
	}
	values["LANG"], values["LC_ALL"] = "C", "C"
	return environmentMap(values), nil
}

func environmentMap(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for k, v := range values {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func nativeHostPaths(env []string) []string {
	values := map[string]string{}
	for _, entry := range env {
		k, v, _ := strings.Cut(entry, "=")
		values[k] = v
	}
	home := values["HOME"]
	paths := []string{hostAuthStatePath(home), filepath.Join(home, ".codex"), filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json"), filepath.Join(home, "Library/Keychains"), filepath.Join(home, ".ssh"), filepath.Join(home, ".aws")}
	for _, k := range []string{"CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		if values[k] != "" {
			paths = append(paths, values[k])
		}
	}
	return paths
}

// Canonicalize existing ancestors even when a credential store has not been
// created yet, so /var aliases cannot bypass overlap checks before first login.
func canonicalProspectivePath(p string) string {
	parent := filepath.Clean(p)
	for {
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			rel, _ := filepath.Rel(parent, p)
			return filepath.Join(resolved, rel)
		}
		next := filepath.Dir(parent)
		if next == parent {
			return filepath.Clean(p)
		}
		parent = next
	}
}

func pathsOverlap(a, b string) bool {
	contains := func(a, b string) bool {
		r, e := filepath.Rel(a, b)
		return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
	}
	return contains(a, b) || contains(b, a)
}

func newNativeToolPolicy(c ProviderConfig, s *sessionsandbox.Session) (*nativeToolPolicy, error) {
	if c.Provider == "claude" && strings.ContainsAny(s.Workspace(), "(),") {
		return nil, errors.New("Claude SESSION_ROOT must not contain permission-rule delimiters")
	}
	protected := append(nativeHostPaths(c.Env), c.SessionIsolation.Root, c.SessionIsolation.TempRoot)
	read := []string{"/bin", "/sbin", "/usr/bin", "/usr/sbin", "/usr/lib", "/usr/libexec", "/lib", "/lib64", "/etc/ssl/certs", "/etc/ssl/openssl.cnf", "/etc/hosts", "/etc/resolv.conf", "/etc/nsswitch.conf", "/etc/ld.so.cache", "/etc/localtime", "/dev/null", "/dev/urandom", "/dev/random"}
	if runtime.GOOS == "darwin" {
		read = append(read, "/System/Library", "/usr/share/icu", "/private/var/select/sh", "/private/etc/ssl/cert.pem")
	} else {
		read = append(read, "/usr/share/nodejs", "/usr/share/icu")
		if c.Provider == "claude" {
			bin := c.Bin
			if bin == "" {
				bin = "claude"
			}
			if path, e := exec.LookPath(bin); e == nil {
				if resolved, e := filepath.EvalSymlinks(path); e == nil {
					read = append(read, resolved)
				}
			}
		}
	}
	for _, p := range c.SessionIsolation.ReadPaths {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			return nil, errors.New("SESSION_READ_PATHS must exist")
		}
		for _, blocked := range protected {
			if blocked == "" {
				continue
			}
			blocked = canonicalProspectivePath(blocked)
			if pathsOverlap(r, blocked) {
				return nil, errors.New("SESSION_READ_PATHS cannot expose authentication or session/control directories")
			}
		}
		read = append(read, r)
	}
	// Canonical paths prevent duplicate mounts through /bin -> /usr/bin and
	// /var -> /private/var. Custom paths were checked against protected roots.
	resolved := []string{}
	for _, p := range read {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			resolved = appendUniqueString(resolved, r)
		}
	}
	read = resolved
	checkRoots := append([]string(nil), read...)
	if c.Provider == "codex" && runtime.GOOS == "linux" {
		// Codex :minimal adds these platform code/config roots implicitly.
		checkRoots = append(checkRoots, "/usr", "/etc", "/nix/store", "/run/current-system/sw")
	}
	for _, blocked := range protected {
		if blocked == "" {
			continue
		}
		blocked = canonicalProspectivePath(blocked)
		for _, grant := range checkRoots {
			grant = canonicalProspectivePath(grant)
			if blocked == grant || strings.HasPrefix(blocked, grant+"/") {
				return nil, errors.New("authentication and session roots must be outside readable system/code paths")
			}
		}
	}
	writes := []string{s.Workspace(), s.ToolHome(), s.Temp()}
	// Tools must not replace or unlink the coordination inode, including when
	// an operator accidentally places session storage under this reserved root.
	for _, entry := range c.Env {
		if home, ok := strings.CutPrefix(entry, "HOME="); ok {
			state := canonicalProspectivePath(hostAuthStatePath(home))
			for _, grant := range writes {
				if pathsOverlap(canonicalProspectivePath(grant), state) {
					return nil, errors.New("session writable paths cannot overlap host-auth coordination state")
				}
			}
		}
	}

	filesystem := map[string]string{":minimal": "read"}
	fs := []string{`":minimal"="read"`}
	for _, p := range read {
		// Codex mounts individual read roots; file symlink destinations cannot
		// be rebound. Its :minimal profile already supplies system dir aliases.
		r, err := filepath.EvalSymlinks(p)
		if err != nil || filesystem[r] != "" {
			continue
		}
		fs = append(fs, jsonString(r)+`="read"`)
		filesystem[r] = "read"
	}
	for _, p := range writes {
		fs = append(fs, jsonString(p)+`="write"`)
		filesystem[p] = "write"
	}
	domains := []string{}
	for _, domain := range c.SessionIsolation.AllowedDomains {
		domains = append(domains, jsonString(strings.TrimSuffix(domain, ":443"))+`="allow"`)
	}
	network := `enabled=false`
	if len(domains) > 0 {
		network = `enabled=true,mode="limited",allow_local_binding=false,allow_upstream_proxy=false,enable_socks5=false,enable_socks5_udp=false,dangerously_allow_all_unix_sockets=false,dangerously_allow_non_loopback_proxy=false,domains={` + strings.Join(domains, ",") + `}`
	}
	toolEnv := []string{"HOME=" + s.ToolHome(), "TMPDIR=" + s.Temp(), "TMP=" + s.Temp(), "TEMP=" + s.Temp(), "LANG=C"}
	for _, item := range c.Env {
		if strings.HasPrefix(item, "PATH=") {
			toolEnv = append(toolEnv, item)
		}
	}
	codex := []string{"-c", `default_permissions="openlinker_session"`, "-c", `permissions={openlinker_session={filesystem={` + strings.Join(fs, ",") + `},network={` + network + `}}}`,
		"-c", `approval_policy="never"`, "-c", `shell_environment_policy.inherit="none"`, "-c", "shell_environment_policy.set=" + codexCommandEnvironment(toolEnv),
		"-c", `project_doc_max_bytes=0`, "-c", `mcp_servers={}`, "-c", `developer_instructions=""`, "-c", `notify=[]`}
	for _, feature := range []string{"view_image", "multi_agent", "multi_agent_v2", "memories", "browser_use", "computer_use", "in_app_browser", "shell_snapshot", "shell_snapshot_v2", "shell_zsh_fork", "external_agent_memory_import", "in_app_local_automation", "goals"} {
		codex = append(codex, "--disable", feature)
	}
	if len(domains) > 0 {
		codex = append(codex, "--enable", "network_proxy")
	} else {
		codex = append(codex, "--disable", "network_proxy")
	}
	read = append(read, writes...)
	denied := append([]string{"/", "/tmp", "/private/tmp", os.TempDir()}, nativeHostPaths(c.Env)...)
	if runtime.GOOS == "linux" {
		// Expand and canonicalize a root deny ourselves. Repeated aliases such
		// as /bin and /usr can otherwise mask a previous narrow code re-bind.
		roots, err := nativeLinuxReadDenies()
		if err != nil {
			return nil, err
		}
		denied = append(roots, nativeHostPaths(c.Env)...)
	}
	for _, item := range c.Env {
		if strings.HasPrefix(item, "HOME=") {
			denied = append(denied, strings.TrimPrefix(item, "HOME="))
		}
	}
	for _, p := range append([]string(nil), denied...) {
		if r, e := filepath.EvalSymlinks(p); e == nil {
			denied = appendUniqueString(denied, r)
		}
	}
	settings := map[string]any{
		"permissions": map[string]any{"defaultMode": "default", "blockReadsOutsideWorkingDirectories": true, "disableBypassPermissionsMode": "disable"},
		"sandbox": map[string]any{
			"enabled": true, "failIfUnavailable": true, "allowUnsandboxedCommands": false, "autoAllowBashIfSandboxed": true, "excludedCommands": []string{}, "enableWeakerNestedSandbox": false,
			"credentials": map[string]any{"envVars": []map[string]string{
				{"name": "ANTHROPIC_API_KEY", "mode": "deny"}, {"name": "ANTHROPIC_AUTH_TOKEN", "mode": "deny"},
			}},
			"filesystem": map[string]any{"disabled": false, "denyRead": denied, "allowRead": read, "allowWrite": writes, "denyWrite": nativeHostPaths(c.Env)},
			"network":    map[string]any{"strictAllowlist": true, "allowedDomains": append([]string{}, c.SessionIsolation.AllowedDomains...), "allowLocalBinding": false, "allowAllUnixSockets": false, "allowUnixSockets": []string{}, "deniedResolvedAddresses": []string{"0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "::/128", "::1/128", "fe80::/10", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fc00::/7", "224.0.0.0/4", "ff00::/8"}},
		},
	}
	return &nativeToolPolicy{codex: codex, codexFilesystem: filesystem, claude: jsonObject(settings)}, nil
}

func nativeLinuxReadDenies() ([]string, error) {
	entries, err := os.ReadDir("/")
	if err != nil || len(entries) > 1024 {
		return nil, errors.New("cannot enumerate native tool filesystem boundary")
	}
	paths := []string{}
	for _, entry := range entries {
		switch entry.Name() {
		case "proc", "dev", "sys": // The client replaces proc/dev inside its namespaces.
			continue
		}
		p := canonicalProspectivePath("/" + entry.Name())
		paths = appendUniqueString(paths, p)
	}
	result := []string{}
	for _, p := range paths {
		covered := false
		for _, parent := range paths {
			if p != parent && strings.HasPrefix(p, parent+"/") {
				covered = true
				break
			}
		}
		if !covered {
			result = append(result, p)
		}
	}
	return result, nil
}

func nativeHostCommand(ctx context.Context, c ProviderConfig, bin string, args []string) *exec.Cmd {
	command := exec.CommandContext(ctx, bin, args...)
	command.Dir, command.Env = c.Workspace, append([]string(nil), c.Env...)
	command.Env = append(command.Env, "TMPDIR="+c.sandbox.Temp(), "TMP="+c.sandbox.Temp(), "TEMP="+c.sandbox.Temp())
	if c.Provider == "claude" {
		scrub := "1"
		if runtime.GOOS == "linux" {
			// The global scrub switch in 2.1.259 adds hardcoded writable roots
			// (/home, /tmp, ...), reopening denied reads. Use the ordinary Bash
			// sandbox's credential deny rules + PID/proc isolation instead.
			scrub = "0"
		}
		command.Env = append(command.Env, "CLAUDE_CODE_TMPDIR="+c.sandbox.Temp(), "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB="+scrub, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "CLAUDE_CODE_PROJECT_DIR_NAME=openlinker-"+sessionsandbox.Scope(c.SessionStore))
	}
	providerprocess.Configure(command)
	return command
}

func jsonObject(v any) string { b, _ := json.Marshal(v); return string(b) }
