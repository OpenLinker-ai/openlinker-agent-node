package agentnode

import (
	"bufio"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters"
)

// This reader only supplies the checked-in example to the production Env parser.
// It neither sources a shell file nor reads/interpolates the machine environment.
func claudeFreshRegistrationExample(t *testing.T) Env {
	t.Helper()
	file, err := os.Open(filepath.Join("..", "..", "examples", "claude-fresh-registration.env.example"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	env := Env{}
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, value, ok := strings.Cut(text, "=")
		if !ok || key == "" || strings.TrimSpace(key) != key {
			t.Fatalf("invalid example assignment at line %d", line)
		}
		if _, exists := env[key]; exists {
			t.Fatalf("duplicate example key at line %d", line)
		}
		env[key] = value
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return env
}

func loadClaudeFreshRegistration(t *testing.T, env Env) (*Node, *NativeAdapter) {
	t.Helper()
	node, err := NewFromEnvMap(env)
	if err != nil {
		t.Fatal(err)
	}
	adapter, ok := node.Adapter.(*NativeAdapter)
	if !ok {
		t.Fatalf("expected native Claude bridge, got %T", node.Adapter)
	}
	if adapter.Config.Provider != "claude" {
		t.Fatalf("wrong native provider: %q", adapter.Config.Provider)
	}
	if node.started || node.worker != nil || node.Helper != nil || node.PublicA2A != nil {
		t.Fatal("configuration must not start a Worker or configure an auxiliary server")
	}
	return node, adapter
}

func TestClaudeFreshRegistrationExampleUsesNativeConfiguration(t *testing.T) {
	env := claudeFreshRegistrationExample(t)
	node, adapter := loadClaudeFreshRegistration(t, env)
	if node.Capacity != 1 || node.Transport != "auto" {
		t.Fatalf("unexpected capacity/transport: %d/%q", node.Capacity, node.Transport)
	}
	for name, pair := range map[string][2]string{
		"platform": {node.OpenLinkerURL, env["OPENLINKER_URL"]},
		"agent":    {node.AgentID, env["OPENLINKER_AGENT_ID"]},
		"node":     {node.NodeID, env["OPENLINKER_NODE_ID"]},
		"token":    {node.AgentToken, env["OPENLINKER_AGENT_TOKEN"]},
		"data":     {node.DataDir, env["OPENLINKER_AGENT_NODE_DATA_DIR"]},
		"binary":   {adapter.Config.Bin, env["OPENLINKER_AGENT_NODE_CLAUDE_BIN"]},
		"model":    {adapter.Config.Model, env["OPENLINKER_AGENT_NODE_CLAUDE_MODEL"]},
		"workspace": {adapter.Config.Workspace,
			env["OPENLINKER_AGENT_NODE_CLAUDE_WORKSPACE"]},
		"session store": {adapter.Config.SessionStore,
			env["OPENLINKER_AGENT_NODE_CLAUDE_SESSION_STORE"]},
		"permission": {adapter.Config.Permission,
			env["OPENLINKER_AGENT_NODE_CLAUDE_PERMISSION"]},
	} {
		if pair[0] != pair[1] || !strings.Contains(pair[0], "REPLACE_WITH_") {
			t.Fatalf("%s must preserve its explicit example placeholder", name)
		}
	}
	if !adapter.Config.SessionReuse || env["OPENLINKER_AGENT_NODE_CLAUDE_SESSION_REUSE"] != "true" {
		t.Fatal("this example must explicitly opt into retained Claude sessions")
	}
	for _, retained := range []string{adapter.Config.Workspace, filepath.Dir(adapter.Config.SessionStore)} {
		if node.DataDir == retained || strings.HasPrefix(retained, node.DataDir+string(filepath.Separator)) ||
			strings.HasPrefix(node.DataDir, retained+string(filepath.Separator)) {
			t.Fatal("the example must keep SDK and Claude directories separate, not nested")
		}
	}
	if !reflect.DeepEqual(adapter.Config.AllowedTools, []string{"REPLACE_WITH_PREVIOUS_ALLOWED_TOOL"}) {
		t.Fatal("example tools must remain an explicit review placeholder, not an implicit permission grant")
	}
	if node.RuntimeURL != "" || node.MTLSCertFile != "" || node.MTLSKeyFile != "" || node.MTLSCAFile != "" {
		t.Fatal("example must not inject a private Runtime endpoint or credential paths")
	}
	if len(adapter.Config.DelegationTargets) != 0 || len(adapter.Config.EnvAllowlist) != 0 {
		t.Fatal("registration must not implicitly add delegation or environment inheritance")
	}
	// backend only constructs the provider. Never call Preflight, Run, or Start:
	// this is a config test, not registration or an installed-Claude acceptance.
	provider, err := adapter.backend()
	if err != nil {
		t.Fatal(err)
	}
	claude, ok := provider.(adapters.ClaudeProvider)
	if !ok || !reflect.DeepEqual(claude.Config, adapter.Config) {
		t.Fatalf("expected unchanged Node ClaudeProvider config without a Plugin wrapper, got %T", provider)
	}
}

func TestClaudeFreshRegistrationKeepsClaudePathsSeparateFromNewSDKStore(t *testing.T) {
	env := claudeFreshRegistrationExample(t)
	root := t.TempDir()
	workspace := filepath.Join(root, "retained-workspace")
	store := filepath.Join(root, "retained-claude-state", "sessions.json")
	env["OPENLINKER_AGENT_ID"] = "10000000-0000-4000-8000-000000000001"
	env["OPENLINKER_AGENT_NODE_CLAUDE_WORKSPACE"] = workspace
	env["OPENLINKER_AGENT_NODE_CLAUDE_SESSION_STORE"] = store
	for index, nodeID := range []string{
		"20000000-0000-4000-8000-000000000001",
		"20000000-0000-4000-8000-000000000002",
	} {
		candidate := maps.Clone(env)
		candidate["OPENLINKER_NODE_ID"] = nodeID
		candidate["OPENLINKER_AGENT_NODE_DATA_DIR"] = filepath.Join(root, nodeID, "sdk-runtime")
		node, adapter := loadClaudeFreshRegistration(t, candidate)
		if node.NodeID != nodeID || node.AgentID != env["OPENLINKER_AGENT_ID"] ||
			node.DataDir != candidate["OPENLINKER_AGENT_NODE_DATA_DIR"] ||
			adapter.Config.Workspace != workspace || adapter.Config.SessionStore != store {
			t.Fatalf("candidate %d rewrote the explicit identity or retained Claude paths", index)
		}
		if node.DataDir == workspace || node.DataDir == store || node.DataDir == filepath.Dir(store) {
			t.Fatal("SDK DataDir and retained Claude data must stay separate")
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("loading configuration must not create session files or a Runtime store")
	}
}

func TestClaudeFreshRegistrationReuseDoesNotChangeGeneralDefault(t *testing.T) {
	for _, test := range []struct {
		name  string
		set   bool
		value string
		want  bool
	}{
		{name: "explicit migration override", set: true, value: "true", want: true},
		{name: "omitted remains false"},
		{name: "explicit false remains false", set: true, value: "false"},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := claudeFreshRegistrationExample(t)
			delete(env, "OPENLINKER_AGENT_NODE_CLAUDE_SESSION_REUSE")
			if test.set {
				env["OPENLINKER_AGENT_NODE_CLAUDE_SESSION_REUSE"] = test.value
			}
			_, adapter := loadClaudeFreshRegistration(t, env)
			if adapter.Config.SessionReuse != test.want {
				t.Fatalf("session reuse=%t, want %t", adapter.Config.SessionReuse, test.want)
			}
		})
	}
}

func TestClaudeFreshRegistrationPreservesReviewedModelAndPermissions(t *testing.T) {
	for _, permission := range []string{"dontAsk", "plan"} {
		t.Run(permission, func(t *testing.T) {
			env := claudeFreshRegistrationExample(t)
			env["OPENLINKER_AGENT_NODE_CLAUDE_MODEL"] = "synthetic-reviewed-model"
			env["OPENLINKER_AGENT_NODE_CLAUDE_PERMISSION"] = permission
			env["OPENLINKER_AGENT_NODE_CLAUDE_ALLOWED_TOOLS"] = `["Read","Glob"]`
			_, adapter := loadClaudeFreshRegistration(t, env)
			if adapter.Config.Model != "synthetic-reviewed-model" || adapter.Config.Permission != permission ||
				!reflect.DeepEqual(adapter.Config.AllowedTools, []string{"Read", "Glob"}) {
				t.Fatal("fresh registration changed the explicitly reviewed provider options")
			}
		})
	}
	t.Run("omitted options retain general defaults", func(t *testing.T) {
		env := claudeFreshRegistrationExample(t)
		delete(env, "OPENLINKER_AGENT_NODE_CLAUDE_MODEL")
		delete(env, "OPENLINKER_AGENT_NODE_CLAUDE_PERMISSION")
		delete(env, "OPENLINKER_AGENT_NODE_CLAUDE_ALLOWED_TOOLS")
		_, adapter := loadClaudeFreshRegistration(t, env)
		if adapter.Config.Model != "" || adapter.Config.Permission != "dontAsk" || len(adapter.Config.AllowedTools) != 0 {
			t.Fatal("migration example must not change general model, permission, or tool defaults")
		}
	})
}
