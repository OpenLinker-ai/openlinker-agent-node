package agentnode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providertest"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/skillpackages"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

// Exercise the environment-configured product entry, without setting any
// isolation/session options. Protocol fixtures never use real credentials.
func TestDefaultIsolatedAdaptersLoadSelectedSkills(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			env := defaultIsolationEnv(t, provider)
			bin := filepath.Join(t.TempDir(), provider)
			const readReference = "set -eu\ntest \"$(cat .openlinker-skills/*/*/references/input.txt)\" = 'pinned reference'\n"
			if provider == "codex" {
				providertest.WriteCodexRPCFixture(t, bin, "ephemeral")
				script, err := os.ReadFile(bin)
				if err != nil {
					t.Fatal(err)
				}
				// Official app-server creates its Linux helper before initialize.
				setup := readReference + "helper_dir=\"$HOME/.codex/tmp/arg0/codex-arg0-fixture\"\nmkdir -p \"$helper_dir\"\nln -sf \"$0\" \"$helper_dir/codex-linux-sandbox\"\n"
				if err := os.WriteFile(bin, []byte(strings.Replace(string(script), "#!/bin/sh\n", "#!/bin/sh\n"+setup, 1)), 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				script := "#!/bin/sh\n" + readReference + "cat > prompt\nprintf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"answer\",\"session_id\":\"11111111-aaaa-4111-8111-111111111111\"}'\n"
				if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			env["OPENLINKER_AGENT_NODE_"+strings.ToUpper(provider)+"_BIN"] = bin
			node, err := NewFromEnvMap(env)
			if err != nil {
				t.Fatal(err)
			}
			// Supply only synthetic process environment; keep all parsed product
			// defaults, including isolation, reuse and the automatically chosen root.
			processEnv := []string{"HOME=" + env["HOME"], "PATH=" + os.Getenv("PATH")}
			switch a := node.Adapter.(type) {
			case *CodexAdapter:
				a.Env = processEnv
			case *NativeAdapter:
				a.Config.Env = processEnv
			default:
				t.Fatalf("unexpected adapter: %T", node.Adapter)
			}
			payload, _ := json.Marshal(skillpackages.Contents{Name: "selected", Providers: []string{provider}, Files: map[string]string{"SKILL.md": "DEFAULT-ISOLATION-SKILL", "references/input.txt": "pinned reference"}})
			digest := sha256.Sum256(payload)
			version := skillpackages.Version{BindingID: "11111111-1111-4111-8111-111111111111", PackageID: "22222222-2222-4222-8222-222222222222", VersionID: "33333333-3333-4333-8333-333333333333", Digest: hex.EncodeToString(digest[:]), Payload: string(payload)}
			loaded := 0
			for i, principal := range []string{"caller-a", "caller-a", "caller-b"} {
				id := "run-" + strconv.Itoa(i)
				run := RunContext{RunID: id, AgentID: "55555555-5555-4555-8555-555555555555", Authority: &openlinker.RuntimeAuthorityContext{PrincipalScopeID: principal}, Conversation: &ConversationContext{Source: "core", SessionKey: "same-conversation", CurrentRunID: id}, PackageSnapshot: skillpackages.Snapshot{Schema: 1, Bundles: []skillpackages.Version{version}}, emitChecked: func(kind string, payload any) error {
					if kind == "run.skill_packages.loaded" {
						loaded++
					}
					return nil
				}}
				result, err := node.Adapter.Run(context.Background(), "use the selected skill", run)
				if err != nil {
					t.Fatal(err)
				}
				out := normalizeAdapterResult(result).Output.(JSONMap)
				if got := out[provider+"_session_resumed"]; got != (i == 1) {
					t.Fatalf("run %d resumed=%v; same caller must resume, another caller must start separately", i, got)
				}
			}
			root := filepath.Join(env["HOME"], ".local", "state", "openlinker-agent-node-sessions")
			packages := 0
			if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.Name() == "SKILL.md" {
					packages++
					data, err := os.ReadFile(path)
					if err != nil || string(data) != "DEFAULT-ISOLATION-SKILL" || !strings.Contains(filepath.ToSlash(path), "/data/workspace/.openlinker-skills/") {
						t.Fatalf("invalid isolated package %s: %v", path, err)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if loaded != 3 || packages != 2 {
				t.Fatalf("receipts=%d isolated package copies=%d", loaded, packages)
			}
		})
	}
}
