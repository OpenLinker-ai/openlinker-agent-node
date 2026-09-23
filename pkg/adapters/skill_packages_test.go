package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/skillpackages"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type packageSnapshot = skillpackages.Snapshot
type packageVersion = skillpackages.Version
type packageContents = skillpackages.Contents

var decodePackageSnapshot = skillpackages.Decode

func testPackageSnapshot(provider, marker string) packageSnapshot {
	payload, _ := json.Marshal(packageContents{Name: "report", Description: "Report", Providers: []string{provider}, Files: map[string]string{"SKILL.md": "---\nname: report\ndescription: Report\n---\n" + marker, "references/example.txt": "versioned reference"}})
	digest := sha256.Sum256(payload)
	return packageSnapshot{Schema: 1, Bundles: []packageVersion{{BindingID: "11111111-1111-4111-8111-111111111111", PackageID: "22222222-2222-4222-8222-222222222222", VersionID: "33333333-3333-4333-8333-333333333333", Version: "1.0.0", Digest: hex.EncodeToString(digest[:]), Payload: string(payload)}}}
}

func TestSkillPackagesReachBothProviderProcesses(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			var bin, dir, log string
			if name == "codex" {
				dir = t.TempDir()
				bin = filepath.Join(dir, "codex")
				log = filepath.Join(dir, "calls")
				writeCodexRPCFixture(t, bin, "ephemeral")
			} else {
				bin, dir = reviewFakeCLI(t, `cat >> prompts
printf '%s\n' "$*" >> arguments
printf '%s\n' '{"type":"result","subtype":"success","result":"answer","session_id":"11111111-aaaa-4111-8111-111111111111"}'
`)
			}
			config := ProviderConfig{Provider: name, Bin: bin, Workspace: dir, SessionStore: filepath.Join(dir, "sessions.json"), SessionReuse: true, Env: append(os.Environ(), "TEST_LOG="+log), EnvAllowlist: []string{"TEST_LOG"}}
			provider, err := NewProvider(config)
			if err != nil {
				t.Fatal(err)
			}
			loaded := 0
			run := RunContext{RunID: "44444444-4444-4444-8444-444444444444", AgentID: "55555555-5555-4555-8555-555555555555", Authority: &openlinker.RuntimeAuthorityContext{PrincipalScopeID: "scope"}, Input: "write report", Conversation: &ConversationContext{SessionKey: "same-conversation"}, Emit: func(kind string, _ any) error {
				if kind == "run.skill_packages.loaded" {
					loaded++
				}
				return nil
			}}
			run.PackageSnapshot = testPackageSnapshot(name, "FIRST-PRIVATE-INSTRUCTION")
			if _, err := provider.Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			run.PackageSnapshot = testPackageSnapshot(name, "SECOND-PRIVATE-INSTRUCTION")
			if _, err := provider.Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			if _, err := provider.Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			run.PackageSnapshot = nil
			if _, err := provider.Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			if loaded != 3 {
				t.Fatalf("loaded receipts=%d", loaded)
			}
			promptFile := filepath.Join(dir, "prompts")
			if name == "codex" {
				promptFile = log + ".requests"
			}
			prompts, err := os.ReadFile(promptFile)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"FIRST-PRIVATE-INSTRUCTION", "SECOND-PRIVATE-INSTRUCTION", ".openlinker-skills", "references/example.txt"} {
				if !strings.Contains(string(prompts), want) {
					t.Fatalf("provider process did not receive %q", want)
				}
			}
			for _, marker := range []string{"FIRST-PRIVATE-INSTRUCTION", "SECOND-PRIVATE-INSTRUCTION"} {
				if strings.Count(string(prompts), marker) != 1 {
					t.Fatalf("resumed session repeated package instructions: %s", marker)
				}
			}

			if name == "codex" {
				calls := prompts
				if strings.Count(string(calls), " thread/start\n") != 3 || strings.Count(string(calls), " thread/resume\n") != 1 {
					t.Fatalf("version change reused an incompatible session: %s", calls)
				}
			} else {
				arguments, _ := os.ReadFile(filepath.Join(dir, "arguments"))
				if strings.Count(string(arguments), "--resume") != 1 {
					t.Fatalf("version change reused an incompatible session: %s", arguments)
				}
			}
		})
	}
}

func TestSkillPackagesUseIsolatedSessionWorkspace(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			config := isolationConfig(t, name)
			operatorWorkspace := t.TempDir()
			config.Workspace = operatorWorkspace
			config.Bin = filepath.Join(t.TempDir(), name)
			if name == "codex" {
				writeCodexRPCFixture(t, config.Bin, "ephemeral")
				// The real app-server creates these aliases before initialize.
				// Linux validates them when restricting the model's filesystem.
				script, err := os.ReadFile(config.Bin)
				if err != nil {
					t.Fatal(err)
				}
				setup := `set -eu
test -r .openlinker-skills/55555555-5555-4555-8555-555555555555/*/references/example.txt
helper_dir="${CODEX_HOME:-$HOME/.codex}/tmp/arg0/codex-arg0-fixture"
mkdir -p "$helper_dir"
ln -s "$0" "$helper_dir/codex-linux-sandbox"
`
				if err := os.WriteFile(config.Bin, []byte(strings.Replace(string(script), "#!/bin/sh\n", "#!/bin/sh\n"+setup, 1)), 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				bin, _ := reviewFakeCLI(t, `cat >/dev/null
test -r .openlinker-skills/55555555-5555-4555-8555-555555555555/*/references/example.txt || exit 4
printf '%s\n' '{"type":"result","subtype":"success","result":"readable","session_id":"11111111-aaaa-4111-8111-111111111111"}'
`)
				config.Bin = bin
			}
			provider, err := NewProvider(config)
			if err != nil {
				t.Fatal(err)
			}
			run := isolationRun("skill-session")
			run.AgentID = "55555555-5555-4555-8555-555555555555"
			run.PackageSnapshot = testPackageSnapshot(name, "ISOLATED-PRIVATE-SKILL")
			run.Emit = func(string, any) error { return nil }
			if _, err := provider.Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(operatorWorkspace, ".openlinker-skills")); !os.IsNotExist(err) {
				t.Fatal("materialized into operator workspace before isolation")
			}
			found := 0
			if err := filepath.WalkDir(config.SessionIsolation.Root, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.Name() == "SKILL.md" {
					if !strings.Contains(filepath.ToSlash(path), "/data/workspace/.openlinker-skills/") {
						t.Errorf("outside session workspace: %s", path)
					}
					found++
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if found != 1 {
				t.Fatalf("materialized packages=%d", found)
			}
		})
	}
}

func TestProviderSkillRecoveryStartsFreshSession(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			var bin string
			if name == "codex" {
				bin = filepath.Join(t.TempDir(), "codex")
				writeCodexRPCFixture(t, bin, "ephemeral")
			} else {
				bin, _ = reviewFakeCLI(t, `cat >/dev/null
printf '%s\n' '{"type":"result","subtype":"success","result":"ok","session_id":"11111111-aaaa-4111-8111-111111111111"}'
`)
			}
			config := ProviderConfig{Provider: name, Bin: bin, Workspace: workspace, SessionStore: filepath.Join(workspace, "sessions.json"), SessionReuse: true}
			provider, err := NewProvider(config)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := testPackageSnapshot(name, "PINNED-INSTRUCTION")
			run := RunContext{AgentID: "55555555-5555-4555-8555-555555555555", Authority: &openlinker.RuntimeAuthorityContext{PrincipalScopeID: "scope"}, Conversation: &ConversationContext{SessionKey: "conversation"}, PackageSnapshot: snapshot, Emit: func(string, any) error { return nil }}
			if _, err := provider.Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			original := filepath.Join(workspace, ".openlinker-skills", run.AgentID, snapshot.Bundles[0].Digest, "SKILL.md")
			if err := os.Chmod(original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(original, []byte("TAMPERED"), 0600); err != nil {
				t.Fatal(err)
			}
			for i, wantResume := range []bool{false, true} {
				result, err := provider.Run(context.Background(), run)
				if err != nil {
					t.Fatal(err)
				}
				out := result.Output.(map[string]any)
				if got := out[name+"_session_resumed"]; got != wantResume {
					t.Fatalf("attempt %d resumed=%v want=%v", i, got, wantResume)
				}
			}
		})
	}
}

func TestNativeSkillPrerequisitesFollowConfiguredPATHAndReadPolicy(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			c := isolationConfig(t, name)
			toolsDir := t.TempDir()
			command := filepath.Join(toolsDir, "skill-test-tool")
			if err := os.WriteFile(command, []byte("not executed"), 0700); err != nil {
				t.Fatal(err)
			}
			c.Env = append(c.Env, "PATH="+toolsDir)
			snapshot := testPackageSnapshot(name, "test")
			var contents packageContents
			if err := json.Unmarshal([]byte(snapshot.Bundles[0].Payload), &contents); err != nil {
				t.Fatal(err)
			}
			contents.RequiredCommands = []string{"skill-test-tool"}
			raw, _ := json.Marshal(contents)
			digest := sha256.Sum256(raw)
			snapshot.Bundles[0].Payload = string(raw)
			snapshot.Bundles[0].Digest = hex.EncodeToString(digest[:])
			run := isolationRun("prerequisites")
			run.AgentID = "55555555-5555-4555-8555-555555555555"
			run.PackageSnapshot = snapshot
			failed := false
			run.Emit = func(kind string, payload any) error {
				if kind == "run.skill_packages.failed" {
					failed = true
				}
				return nil
			}
			provider, err := NewProvider(c)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.Run(context.Background(), run); err == nil || !failed {
				t.Fatal("command outside tool read policy accepted")
			}
			c.SessionIsolation.ReadPaths = []string{toolsDir}
			configured, closeSandbox, err := prepareIsolatedSession(context.Background(), c, run)
			if err != nil {
				t.Fatal(err)
			}
			defer closeSandbox()
			// The exact post-isolation loader used by both production adapters succeeds
			// with the explicitly allowed Provider path, even if the Worker PATH differs.
			if _, err := loadSkillPackages(context.Background(), run, name, configured.Workspace, configured); err != nil {
				t.Fatal(err)
			}
		})
	}
}
