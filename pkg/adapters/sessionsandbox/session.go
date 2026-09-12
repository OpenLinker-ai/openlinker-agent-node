package sessionsandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/appfiles"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providersession"
)

// Session owns a cross-process lock for an invocation. Cleanup precedes release;
// the next owner fences any leftovers even if that cleanup failed. Private
// native-ID mappings are siblings of data, never inside it.
type Session struct {
	config Config
	root   string
	data   string
	name   string
	owner  string
	lock   *appfiles.Lock
	docker string
	engine string
	env    []string
}

func Scope(parts ...string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("openlinker/session-sandbox/v1"))
	for _, part := range parts {
		// Length framing prevents ambiguity even in unusual trusted identifiers.
		_, _ = fmt.Fprintf(hash, "/%d:", len(part))
		_, _ = hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func Open(ctx context.Context, config Config, scope string) (_ *Session, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if !config.Enabled() || scope == "" {
		return nil, errors.New("a Docker sandbox and trusted session scope are required")
	}
	root, err := privateRoot(config.Root)
	if err != nil {
		return nil, err
	}
	identity := Scope(config.Namespace, scope)
	control, err := privateDirectory(root, identity)
	if err != nil {
		return nil, err
	}
	lock, err := appfiles.AcquireLock(filepath.Join(control, "session.lock"))
	if err != nil {
		return nil, fmt.Errorf("session sandbox is busy or its private lock is unsafe: %w", err)
	}
	session := &Session{config: config, root: control, lock: lock}
	defer func() {
		if err != nil {
			_ = lock.Release()
		}
	}()
	session.owner = Scope(root, identity)
	session.name = "openlinker-session-" + session.owner
	if err = session.connect(ctx); err != nil {
		return nil, err
	}
	if err = session.bindEngine(); err != nil {
		return nil, err
	}
	// A killed Node can leave a container behind. Fence that exact owned
	// container before touching native files or accepting another turn.
	if err = session.removeContainer(); err != nil {
		return nil, err
	}
	session.data, err = privateDirectory(control, "data")
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"workspace", "home", "codex", "claude"} {
		if _, err = privateDirectory(session.data, name); err != nil {
			return nil, err
		}
	}
	return session, nil
}

func (session *Session) Store() string { return filepath.Join(session.root, "native-session.json") }

func (session *Session) bindEngine() error {
	// A context switch after Node death must not bypass orphan fencing by
	// starting a second container on another daemon over the same host files.
	path := filepath.Join(session.root, "docker-engine")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return appfiles.WritePrivateFile(path, []byte(session.engine+"\n"))
	} else if err != nil {
		return errors.New("cannot read session Docker ownership")
	}
	identity, err := appfiles.ReadPrivateSecret(path)
	if err != nil || identity != session.engine {
		return errors.New("session belongs to a different Docker daemon or has unsafe ownership state; restore its original local Docker context")
	}
	return nil
}

func (session *Session) Close() error {
	if session == nil || session.lock == nil {
		return nil
	}
	err := session.removeContainer()
	unlockErr := session.lock.Release()
	session.lock = nil
	return errors.Join(err, unlockErr)
}

func privateDirectory(parent, name string) (string, error) {
	path := filepath.Join(parent, name)
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", errors.New("cannot create private session directory")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 || !providersession.OwnedByCurrentUser(info) {
		return "", errors.New("session directory must be owned by Node, owner-only, and not a symlink")
	}
	return path, nil
}

func privateRoot(path string) (string, error) {
	// Require an existing parent; never chmod, copy, or migrate existing state.
	clean := filepath.Clean(path)
	if _, err := privateDirectory(filepath.Dir(clean), filepath.Base(clean)); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil || strings.ContainsAny(resolved, ",\r\n\x00") {
		return "", errors.New("cannot resolve private session root")
	}
	// Writable shared ancestors are unsafe unless protected by the sticky bit
	// (e.g. /tmp). Resolve system symlinks such as macOS /var before this check.
	for dir := filepath.Dir(resolved); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return "", errors.New("session root has an unsafe writable ancestor")
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	return resolved, nil
}
