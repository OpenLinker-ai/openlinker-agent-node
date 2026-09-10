// Package codexhome separates app-server configuration from native credentials
// and OpenLinker's persistent native rollouts. No personal config is copied.
package codexhome

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Supported reports whether the native owner/permission checks are implemented.
const Supported = runtime.GOOS != "windows"

const LauncherEnvironment = "OPENLINKER_CODEX_RPC_LAUNCHER"
const PrepareEnvironment = "OPENLINKER_CODEX_RPC_PREPARE"

// Prepare must execute as the Provider UID, never as the cross-UID Runtime.
// Only two owned session directories and a validated native auth file are linked
// into the fresh home. Native auth files are not read or copied by Go;
// environment API keys use a private file. Auth refresh remains Codex's job.
// Pinned Codex 0.153.0 writes FileAuthStorage in place, following this symlink;
// refresh_test.go verifies native refresh survives cleanup with fake credentials.
// Revalidate that contract when upgrading (rename-based persistence differs).
// Native keyring-only logins require a file-backed login for this isolated home.
func Prepare(environment []string) ([]string, func(), error) {
	if !Supported {
		return nil, nil, errors.New("isolated Codex app-server requires a POSIX host; Windows DACL isolation is not implemented")
	}
	values := map[string]string{}
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	source := values["CODEX_HOME"]
	if source == "" {
		home := values["HOME"]
		if home == "" {
			home = values["USERPROFILE"]
		}
		if home == "" {
			return nil, nil, errors.New("Codex isolation requires HOME or CODEX_HOME")
		}
		source = filepath.Join(home, ".codex")
	}
	if !filepath.IsAbs(source) {
		return nil, nil, errors.New("Codex home must be absolute")
	}
	// The source is used only for authentication. Config, hooks, rules, plugins,
	// skills, and existing native sessions are intentionally not imported.
	if err := os.MkdirAll(source, 0o700); err != nil {
		return nil, nil, errors.New("create native Codex home")
	}
	sourceInfo, err := os.Lstat(source)
	if err != nil || !sourceInfo.IsDir() || sourceInfo.Mode()&os.ModeSymlink != 0 || sourceInfo.Mode().Perm()&0o022 != 0 || !owned(sourceInfo) {
		return nil, nil, errors.New("native Codex home must be an owned directory without shared write access")
	}
	root := filepath.Join(source, "openlinker-rpc-v1")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, nil, errors.New("create isolated Codex state directory")
	}
	if err := privateDirectory(root); err != nil {
		return nil, nil, err
	}
	for _, name := range []string{"sessions", "archived_sessions"} {
		p := filepath.Join(root, name)
		if err := os.Mkdir(p, 0o700); err != nil && !os.IsExist(err) {
			return nil, nil, err
		}
		if err := privateDirectory(p); err != nil {
			return nil, nil, err
		}
	}
	home, err := os.MkdirTemp(root, "attempt-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(home) }
	fail := func(err error) ([]string, func(), error) { cleanup(); return nil, nil, err }
	for _, name := range []string{"sessions", "archived_sessions"} {
		if err := os.Symlink(filepath.Join(root, name), filepath.Join(home, name)); err != nil {
			return fail(errors.New("link isolated Codex rollouts (symlink support is required)"))
		}
	}
	if apiKey := values["CODEX_API_KEY"]; apiKey != "" {
		// Unlike exec, app-server does not read CODEX_API_KEY for built-in
		// authentication. Give the native CLI an Attempt-private auth file,
		// without changing the operator's login or owning any HTTP transport.
		if len(apiKey) > 32768 {
			return fail(errors.New("Codex API key exceeds size limit"))
		}
		raw, _ := json.Marshal(map[string]string{"OPENAI_API_KEY": apiKey})
		if err := os.WriteFile(filepath.Join(home, "auth.json"), raw, 0o600); err != nil {
			return fail(errors.New("prepare private Codex API-key authentication"))
		}
	} else {
		auth := filepath.Join(source, "auth.json")
		if info, err := os.Lstat(auth); err == nil {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !owned(info) || info.Size() > 1<<20 {
				return fail(errors.New("native Codex auth must be an owner-only regular non-symlink file"))
			}
			if err := os.Symlink(auth, filepath.Join(home, "auth.json")); err != nil {
				return fail(errors.New("link native Codex authentication"))
			}
		} else if !os.IsNotExist(err) {
			return fail(errors.New("inspect native Codex authentication"))
		}
	}
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if key != "CODEX_HOME" && key != PrepareEnvironment && key != LauncherEnvironment {
			result = append(result, entry)
		}
	}
	result = append(result, "CODEX_HOME="+home)
	return result, cleanup, nil
}
func privateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || !owned(info) {
		return errors.New("isolated Codex state must be an owner-only non-symlink directory")
	}
	return nil
}
