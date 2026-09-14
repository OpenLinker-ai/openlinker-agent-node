//go:build unix

package adapters

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// This is Node's fixed coordination-directory policy, not a session store.
// Validate parents before creating anything, and never chmod an existing path.
// HOME aliases (including macOS /var) resolve to one directory; symlinks beneath
// HOME are rejected. Host administrators and same-UID host processes are trusted.
func prepareHostAuthLockPath(env []string, provider string) (string, error) {
	fail := errors.New("native host-auth state requires a safe HOME and private owned state directory; no temporary-directory fallback")
	home := ""
	for _, entry := range env {
		if v, ok := strings.CutPrefix(entry, "HOME="); ok {
			home = v
		}
	}
	home, err := filepath.EvalSymlinks(home)
	if err != nil || !filepath.IsAbs(home) || home == "/" {
		return "", fail
	}
	for p := home; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() {
			return "", fail
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0) {
			return "", fail
		}
		if p == home && stat.Uid != uint32(os.Geteuid()) {
			return "", fail
		}
		// Root-owned sticky system temp ancestors are needed by isolated fixtures.
		if info.Mode().Perm()&0022 != 0 && (p == home || info.Mode()&os.ModeSticky == 0 || stat.Uid != 0) {
			return "", fail
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	dir := home
	for _, name := range []string{".local", "state", "openlinker-agent-node"} {
		dir = filepath.Join(dir, name)
		if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", fail
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fail
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 {
			return "", fail
		}
		if name == "openlinker-agent-node" && info.Mode().Perm()&0077 != 0 {
			return "", fail
		}
	}
	return filepath.Join(dir, "host-auth-"+provider+".lock"), nil
}
