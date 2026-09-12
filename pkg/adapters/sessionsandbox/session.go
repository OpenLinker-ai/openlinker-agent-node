package sessionsandbox

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/appfiles"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providersession"
)

type Session struct {
	config                                  Config
	control, data, policy, runtimeBin, temp string
	lock                                    *appfiles.Lock
}

//go:embed runner.mjs
var runnerSource []byte

func Scope(parts ...string) string {
	h := sha256.New()
	_, _ = h.Write([]byte("openlinker/native-session/v1"))
	for _, p := range parts {
		_, _ = fmt.Fprintf(h, "/%d:%s", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func Open(ctx context.Context, c Config, scope string) (_ *Session, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if !c.Enabled() || scope == "" {
		return nil, errors.New("native sandbox and trusted scope are required")
	}
	bin, err := resolveRuntime(c.RuntimeBin)
	if err != nil {
		return nil, err
	}
	root, err := privateRoot(c.Root)
	if err != nil {
		return nil, err
	}
	c.Root = root
	tempRoot := "/tmp"
	if c.TempRoot != "" {
		tempRoot, err = privateRoot(c.TempRoot)
		if err != nil {
			return nil, fmt.Errorf("invalid SESSION_TEMP_ROOT: %w", err)
		}
		// Reserve room for olns-<random>/claude-socks-<random>.sock under
		// the platform's short AF_UNIX path limit. Count bytes, not runes.
		if len(tempRoot) > 40 {
			return nil, errors.New("resolved SESSION_TEMP_ROOT must be at most 40 bytes for sandbox sockets")
		}
		c.TempRoot = tempRoot
	}
	control, err := privateDirectory(root, Scope(c.Namespace, scope))
	if err != nil {
		return nil, err
	}
	lock, err := appfiles.AcquireLock(filepath.Join(control, "session.lock"))
	if err != nil {
		return nil, errors.New("native session is busy or its ownership lock is unsafe")
	}
	defer func() {
		if err != nil {
			_ = lock.Release()
		}
	}()
	data, err := privateDirectory(control, "data")
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"workspace", "home", "codex", "claude", "tmp"} {
		if _, err := privateDirectory(data, name); err != nil {
			return nil, err
		}
	}
	// Each invocation gets an immutable policy outside the client's readable
	// tree. A different session cannot change it through an allowed workspace.
	policy, err := os.CreateTemp(control, "policy-*.json")
	if err != nil {
		return nil, errors.New("cannot prepare sandbox policy")
	}
	_ = policy.Close()
	if err := appfiles.WritePrivateFile(filepath.Join(control, "runner.mjs"), runnerSource); err != nil {
		_ = os.Remove(policy.Name())
		return nil, errors.New("cannot prepare native sandbox runner")
	}
	// AF_UNIX socket paths are limited to about 100 bytes. Persistent roots
	// can be much longer, so each invocation gets a short, private temp root.
	temp, err := os.MkdirTemp(tempRoot, "olns-")
	if err != nil {
		_ = os.Remove(policy.Name())
		return nil, errors.New("cannot create private sandbox temporary directory")
	}
	createdTemp := temp
	temp, err = filepath.EvalSymlinks(temp)
	if err != nil {
		_ = os.Remove(policy.Name())
		_ = os.RemoveAll(createdTemp)
		return nil, errors.New("cannot resolve sandbox temporary directory")
	}
	return &Session{config: c, control: control, data: data, policy: policy.Name(), runtimeBin: bin, temp: temp, lock: lock}, nil
}

func (s *Session) Workspace() string { return filepath.Join(s.data, "workspace") }
func (s *Session) Store() string     { return filepath.Join(s.control, "native-session.json") }
func (s *Session) Close() error {
	if s == nil || s.lock == nil {
		return nil
	}
	err := os.Remove(s.policy)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	err = errors.Join(err, os.RemoveAll(s.temp), s.lock.Release())
	s.lock = nil
	return err
}

func privateDirectory(parent, name string) (string, error) {
	p := filepath.Join(parent, name)
	if err := os.Mkdir(p, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", errors.New("cannot create private session directory")
	}
	i, err := os.Lstat(p)
	if err != nil || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 || i.Mode().Perm()&0o077 != 0 || !providersession.OwnedByCurrentUser(i) {
		return "", errors.New("session directory must be owner-only, owned by Node and not a symlink")
	}
	return p, nil
}

func privateRoot(p string) (string, error) {
	clean := filepath.Clean(p)
	if _, err := privateDirectory(filepath.Dir(clean), filepath.Base(clean)); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil || !safePath(resolved) {
		return "", errors.New("cannot resolve private session root")
	}
	for dir := filepath.Dir(resolved); ; dir = filepath.Dir(dir) {
		i, err := os.Lstat(dir)
		if err != nil || !i.IsDir() || i.Mode().Perm()&0o022 != 0 && i.Mode()&os.ModeSticky == 0 {
			return "", errors.New("session root has a shared writable ancestor")
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	return resolved, nil
}

// Environment never inherits the operator's HOME, configuration hooks, SSH
// agent, platform credentials, proxy settings or client session directories.
func (s *Session) Environment(values []string) []string {
	result := make([]string, 0, len(values)+12)
	for _, entry := range values {
		name, _, ok := strings.Cut(entry, "=")
		if ok && (name == "PATH" || name == "CODEX_API_KEY" || name == "ANTHROPIC_API_KEY") {
			result = append(result, entry)
		}
	}
	for key, value := range map[string]string{
		"HOME": filepath.Join(s.data, "home"), "CODEX_HOME": filepath.Join(s.data, "codex"),
		"CLAUDE_CONFIG_DIR": filepath.Join(s.data, "claude"), "TMPDIR": s.temp,
		"TMP": s.temp, "TEMP": s.temp,
		"XDG_CONFIG_HOME": filepath.Join(s.data, "home/.config"), "XDG_CACHE_HOME": filepath.Join(s.data, "home/.cache"),
		"XDG_DATA_HOME": filepath.Join(s.data, "home/.local/share"), "LANG": "C", "LC_ALL": "C",
	} {
		filtered := result[:0]
		for _, entry := range result {
			if !strings.HasPrefix(entry, key+"=") {
				filtered = append(filtered, entry)
			}
		}
		result = append(filtered, key+"="+value)
	}
	return result
}
