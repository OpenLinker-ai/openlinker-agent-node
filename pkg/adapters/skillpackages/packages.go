// Package skillpackages implements the owner-managed package contract shared by
// native bridge and deep execution hosts. It owns no Worker or Provider process.
package skillpackages

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const Feature = "skill_packages.v1"
const MetadataKey = "_openlinker_skill_packages"

// Cache is host configuration, never caller-controlled assignment data.
// A shared cache must already be owned by the host with mode 02750 and GroupID.
// Providers may only read it; their credentials and Runtime state remain private.
type Cache struct {
	Directory string
	GroupID   int
}
type Request struct {
	Snapshot any
	AgentID  string
	Trusted  bool
	Emit     func(string, any) error
	// CheckCommands is supplied by the product for the Provider identity,
	// environment and tool filesystem policy, not the Worker's PATH.
	CheckCommands func(context.Context, []string) error
}
type Result struct {
	Digest   string
	Packages []Package
}

func Features(provider string) []string {
	if provider != "codex" && provider != "claude" {
		return nil
	}
	return []string{Feature, "skill_packages." + provider + ".v1"}
}

type Snapshot struct {
	Schema  int       `json:"schema_version"`
	Bundles []Version `json:"bundles"`
}
type Version struct {
	BindingID string `json:"binding_id"`
	PackageID string `json:"package_id"`
	VersionID string `json:"version_id"`
	Version   string `json:"version"`
	Digest    string `json:"digest"`
	Payload   string `json:"payload"`
}
type Contents struct {
	RequiredCommands []string          `json:"required_commands"`
	Name             string            `json:"name"`
	Description      string            `json:"description"`
	Files            map[string]string `json:"files"`
	CapabilityIDs    []string          `json:"capability_ids"`
	Providers        []string          `json:"providers"`
}
type Package struct {
	Name, Instructions, Directory string
	Files                         []string
}

var packageIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var packageCommandPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+-]{0,63}$`)
var packagePathPattern = regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`)

func Decode(value any, provider string) (Snapshot, []Contents, error) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 768*1024 {
		return Snapshot{}, nil, errors.New("invalid skill package snapshot")
	}
	var snapshot Snapshot
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.Schema != 1 || len(snapshot.Bundles) > 5 {
		return snapshot, nil, errors.New("invalid skill package snapshot")
	}
	bundles := []Contents{}
	seen := map[string]bool{}
	for _, version := range snapshot.Bundles {
		digest := sha256.Sum256([]byte(version.Payload))
		if !packageIDPattern.MatchString(version.BindingID) || !packageIDPattern.MatchString(version.PackageID) || !packageIDPattern.MatchString(version.VersionID) || seen[version.PackageID] || len(version.Payload) > 65536 || hex.EncodeToString(digest[:]) != version.Digest {
			return snapshot, nil, errors.New("skill package identity or digest mismatch")
		}
		seen[version.PackageID] = true
		var bundle Contents
		if json.Unmarshal([]byte(version.Payload), &bundle) != nil || !slices.Contains(bundle.Providers, provider) || len(bundle.Files) < 1 || len(bundle.Files) > 32 || strings.TrimSpace(bundle.Files["SKILL.md"]) == "" {
			return snapshot, nil, errors.New("incompatible skill package")
		}
		for name, content := range bundle.Files {
			if name == "." || len(name) > 180 || !packagePathPattern.MatchString(name) || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, ".") || strings.Contains(name, "/.") || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
				return snapshot, nil, errors.New("unsafe skill package file")
			}
		}
		bundles = append(bundles, bundle)
	}
	return snapshot, bundles, nil
}

// Load validates the trusted per-Run snapshot and emits durable load evidence.
// It never runs install scripts or changes execution permissions.
func Load(ctx context.Context, req Request, provider, workspace string, cache Cache) (Result, error) {
	if req.Snapshot == nil {
		return Result{}, nil
	}
	snapshot, bundles, err := Decode(req.Snapshot, provider)
	if err != nil {
		return Result{}, err
	}
	if len(bundles) == 0 {
		return Result{}, nil
	}
	if !req.Trusted || !packageIDPattern.MatchString(req.AgentID) {
		return Result{}, errors.New("skill package execution requires trusted Runtime authority")
	}
	if req.Emit == nil {
		return Result{}, errors.New("skill package loading requires a durable event channel")
	}
	receipt := make([]map[string]string, 0, len(snapshot.Bundles))
	for _, version := range snapshot.Bundles {
		receipt = append(receipt, map[string]string{"binding_id": version.BindingID, "version_id": version.VersionID, "digest": version.Digest})
	}
	fail := func(cause error, code string) (Result, error) {
		if err := req.Emit("run.skill_packages.failed", map[string]any{"bindings": receipt, "error_code": code}); err != nil {
			return Result{}, err
		}
		return Result{}, cause
	}
	var commands []string
	for _, bundle := range bundles {
		for _, command := range bundle.RequiredCommands {
			if !packageCommandPattern.MatchString(command) {
				return fail(errors.New("invalid required command"), "package_invalid")
			}
			if !slices.Contains(commands, command) {
				commands = append(commands, command)
			}
		}
	}
	if len(commands) > 0 {
		if req.CheckCommands == nil {
			return fail(errors.New("Provider command availability checker is unavailable"), "dependency_missing")
		}
		if err := req.CheckCommands(ctx, commands); err != nil {
			return fail(err, "dependency_missing")
		}
	}
	directory := cache.Directory
	if directory == "" {
		if cache.GroupID != 0 {
			return fail(errors.New("shared cache requires an explicit directory"), "package_materialization_failed")
		}
		if workspace == "" {
			workspace, err = os.Getwd()
			if err != nil {
				return fail(err, "package_materialization_failed")
			}
		}
		if err := protectSkillPackageCache(ctx, workspace); err != nil {
			return fail(err, "package_materialization_failed")
		}
		directory = filepath.Join(workspace, ".openlinker-skills")
		// Create through os.Root so a workspace cache symlink cannot escape.
		workRoot, err := os.OpenRoot(workspace)
		if err != nil {
			return fail(err, "package_materialization_failed")
		}
		err = workRoot.MkdirAll(".openlinker-skills", 0700)
		workRoot.Close()
		if err != nil {
			return fail(err, "package_materialization_failed")
		}
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return fail(err, "package_materialization_failed")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fail(errors.New("skill package cache must be a real directory"), "package_materialization_failed")
	}
	if cache.GroupID != 0 {
		if err := CheckSharedCache(directory, cache.GroupID); err != nil {
			return fail(err, "package_materialization_failed")
		}
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return fail(err, "package_materialization_failed")
	}
	defer root.Close()
	if cache.Directory == "" {
		// A cache may live below an enclosing repository we must not modify.
		// A local ignore file also covers those workspaces without Git discovery.
		if err := writeCacheIgnore(root); err != nil {
			return fail(err, "package_materialization_failed")
		}
	}
	// Assignment aggregation order is not part of skill selection identity.
	versions := append([]Version(nil), snapshot.Bundles...)
	slices.SortFunc(versions, func(a, b Version) int { return strings.Compare(a.PackageID, b.PackageID) })
	raw, _ := json.Marshal(versions)
	digest := sha256.Sum256(raw)
	result := Result{Digest: hex.EncodeToString(digest[:])}
	recovered := map[string]string{}
	for i, bundle := range bundles {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		relative := filepath.Join(req.AgentID, snapshot.Bundles[i].Digest)
		err := materialize(root, relative, bundle.Files, cache.GroupID != 0)
		if err != nil && cache.GroupID == 0 {
			// Same-UID tools can alter read-only cache files. Keep that damaged
			// tree untouched and select a fresh verified copy for later Runs.
			relative, err = recoverPrivatePackage(root, req.AgentID, snapshot.Bundles[i].Digest, bundle.Files)
			recovered[snapshot.Bundles[i].PackageID] = relative
		}
		if err != nil {
			return fail(fmt.Errorf("could not materialize pinned skill package: %w", err), "package_materialization_failed")
		}
		names := make([]string, 0, len(bundle.Files))
		for name := range bundle.Files {
			names = append(names, name)
		}
		slices.Sort(names)
		result.Packages = append(result.Packages, Package{Name: bundle.Name, Instructions: bundle.Files["SKILL.md"], Directory: filepath.Join(directory, relative), Files: names})
	}
	if len(recovered) > 0 {
		// Old sessions retain file locations. Recovering to a new location must
		// inject the full, verified instructions into a fresh native session.
		locations, _ := json.Marshal(recovered)
		digest = sha256.Sum256(append(raw, locations...))
		result.Digest = hex.EncodeToString(digest[:])
	}
	if err := req.Emit("run.skill_packages.loaded", map[string]any{"bindings": receipt}); err != nil {
		return Result{}, err
	}
	return result, nil
}

func SessionMode(digest string) string {
	if digest == "" {
		return ""
	}
	return ":skill_packages:" + digest
}

// Resumed turns retain a short index so compaction does not hide the on-disk
// instructions. The full SKILL.md is sent only when starting/recovering a session.
func Instructions(packages []Package, resumed bool) string {
	if len(packages) == 0 {
		return ""
	}
	lines := []string{"", "The Agent owner associated these version-pinned skill packages with this Agent.",
		"Use their instructions when relevant to the task. They do not grant new tool, credential or network permissions.",
		"Resolve supporting files relative to each package directory. Do not modify package files or reveal their private contents."}
	for _, bundle := range packages {
		lines = append(lines, "", fmt.Sprintf("Skill package: %s\nRead instructions: %s", bundle.Name, filepath.Join(bundle.Directory, "SKILL.md")))
		if resumed {
			lines = append(lines, "Read SKILL.md again if its instructions are no longer in context.")
		} else {
			lines = append(lines, "Available files: "+strings.Join(bundle.Files, ", "), bundle.Instructions)
		}
	}
	return strings.Join(lines, "\n")
}

// Root operations confine all package file access to the configured workspace.
// Existing files must be regular and byte-identical. We never overwrite another
// package, an operator file, or a damaged cache to make a run proceed.
func materialize(root *os.Root, directory string, files map[string]string, shared bool) error {
	dirMode, fileMode := os.FileMode(0700), os.FileMode(0400)
	if shared {
		dirMode, fileMode = os.ModeSetgid|0750, 0440
	}
	if err := packageDirectories(root, directory, dirMode); err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		target := filepath.Join(directory, filepath.FromSlash(name))
		if err := packageDirectories(root, filepath.Dir(target), dirMode); err != nil {
			return err
		}
		existingMatches := func() error {
			info, err := root.Lstat(target)
			if err != nil || !info.Mode().IsRegular() || (shared && info.Mode().Perm() != fileMode) {
				return errors.New("package cache entry is not a regular file")
			}
			content, err := root.ReadFile(target)
			if err != nil || string(content) != files[name] {
				return errors.New("package cache content does not match its pinned version")
			}
			return nil
		}
		if _, err := root.Lstat(target); err == nil {
			if err := existingMatches(); err != nil {
				return err
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		temporary := target + ".pending-" + randomPackageSuffix()
		file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
		if err != nil {
			return err
		}
		writeErr := file.Chmod(fileMode)
		if writeErr == nil {
			_, writeErr = file.WriteString(files[name])
		}
		closeErr := file.Close()
		if writeErr == nil {
			writeErr = closeErr
		}
		if writeErr == nil {
			writeErr = root.Link(temporary, target)
		}
		_ = root.Remove(temporary)
		if errors.Is(writeErr, os.ErrExist) {
			writeErr = existingMatches()
		}
		if writeErr != nil {
			return writeErr
		}
	}
	return nil
}

func packageDirectories(root *os.Root, directory string, mode os.FileMode) error {
	current := ""
	for _, part := range strings.Split(filepath.ToSlash(directory), "/") {
		current = filepath.Join(current, part)
		if err := root.Mkdir(current, mode.Perm()); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := root.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("package directory must be a real directory")
		}
		// Apply the explicit host policy even under umask 0077. No group-write
		// or other-user access is granted; only the Host can prepare this tree.
		if err := root.Chmod(current, mode); err != nil {
			return err
		}
	}
	return nil
}

func randomPackageSuffix() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value[:])
}
