package agentnode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	agentexec "github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providertest"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/skillpackages"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func TestRuntimeAdapterSeparatesPrivateSkillSnapshot(t *testing.T) {
	snapshot := map[string]any{"schema_version": 1, "bundles": []any{}}
	handler := runtimeAdapterHandler{node: &Node{Adapter: AdapterFunc(func(_ context.Context, _ any, run RunContext) (any, error) {
		if !reflect.DeepEqual(run.PackageSnapshot, snapshot) || run.Metadata[skillpackages.MetadataKey] != nil || run.Metadata["tenant"] != "one" {
			t.Fatalf("private snapshot projection: %#v", run)
		}
		return "ok", nil
	})}}
	_, err := handler.Handle(context.Background(), openlinker.RuntimeContext{Metadata: openlinker.RuntimeJSONMap{skillpackages.MetadataKey: snapshot, "tenant": "one"}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeAdaptersLoadPageSelectedSkillSnapshot(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			workspace := t.TempDir()
			bin := filepath.Join(workspace, provider)
			if provider == "codex" {
				providertest.WriteCodexRPCFixture(t, bin, "ephemeral")
			} else {
				if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat > prompt\nprintf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"answer\"}'\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			adapter := &NativeAdapter{Config: agentexec.ProviderConfig{Provider: provider, Bin: bin, Workspace: workspace}}
			if !slices.Contains(adapter.RuntimeFeatures(), "skill_packages."+provider+".v1") {
				t.Fatal("native adapter did not advertise package support")
			}
			payload, _ := json.Marshal(skillpackages.Contents{Name: "selected", Providers: []string{provider}, Files: map[string]string{"SKILL.md": "PAGE-SELECTED-PRIVATE-INSTRUCTION", "references/input.txt": "pinned reference"}})
			digest := sha256.Sum256(payload)
			version := skillpackages.Version{BindingID: "11111111-1111-4111-8111-111111111111", PackageID: "22222222-2222-4222-8222-222222222222", VersionID: "33333333-3333-4333-8333-333333333333", Digest: hex.EncodeToString(digest[:]), Payload: string(payload)}
			loaded := false
			run := RunContext{AgentID: "55555555-5555-4555-8555-555555555555", Authority: &openlinker.RuntimeAuthorityContext{PrincipalScopeID: "owner"}, PackageSnapshot: skillpackages.Snapshot{Schema: 1, Bundles: []skillpackages.Version{version}}, emitChecked: func(kind string, payload any) error {
				if kind == "run.skill_packages.loaded" {
					loaded = true
					raw, _ := json.Marshal(payload)
					if !strings.Contains(string(raw), version.Digest) {
						t.Error("receipt lost pinned digest")
					}
				}
				return nil
			}}
			if _, err := adapter.Run(context.Background(), "use the selected skill", run); err != nil {
				t.Fatal(err)
			}
			promptFile := filepath.Join(workspace, "prompt")
			if provider == "codex" {
				promptFile = bin + ".trace.prompt"
			}
			prompt, err := os.ReadFile(promptFile)
			if err != nil || !loaded || !strings.Contains(string(prompt), "PAGE-SELECTED-PRIVATE-INSTRUCTION") {
				t.Fatalf("adapter did not deliver skills: loaded=%v err=%v prompt=%s", loaded, err, prompt)
			}
			file := filepath.Join(workspace, ".openlinker-skills", run.AgentID, version.Digest, "references/input.txt")
			if raw, err := os.ReadFile(file); err != nil || string(raw) != "pinned reference" {
				t.Fatal("support file was not materialized", err)
			}
		})
	}
}

func TestCodexRPCFixtureProcess(t *testing.T) { providertest.CodexRPCFixtureProcess() }
