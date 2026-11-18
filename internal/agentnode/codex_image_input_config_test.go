package agentnode

import (
	"strings"
	"testing"
)

func TestCodexImageInputEnvironment(t *testing.T) {
	for value, want := range map[string]bool{
		"": false, "false": false, "0": false, "off": false, " NO ": false,
		"true": true, "1": true, "yes": true, "On": true,
	} {
		node, err := NewFromEnvMap(Env{
			"OPENLINKER_AGENT_NODE_ADAPTER": "codex", "OPENLINKER_AGENT_NODE_SESSION_ISOLATION": "off",
			"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT": value,
		})
		if err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		if got := node.Adapter.(*CodexAdapter).native().Config.CodexImageInput; got != want {
			t.Fatalf("%q: CodexImageInput=%t, want %t", value, got, want)
		}
	}
	_, err := NewFromEnvMap(Env{
		"OPENLINKER_AGENT_NODE_ADAPTER": "codex", "OPENLINKER_AGENT_NODE_SESSION_ISOLATION": "off",
		"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT": "maybe-secret",
	})
	if err == nil || strings.Contains(err.Error(), "maybe-secret") {
		t.Fatalf("invalid value: %v", err)
	}
}

func TestCodexImageInputDoHEnvironment(t *testing.T) {
	base := func(extra Env) Env {
		env := Env{"OPENLINKER_AGENT_NODE_ADAPTER": "codex", "OPENLINKER_AGENT_NODE_SESSION_ISOLATION": "off"}
		for k, v := range extra {
			env[k] = v
		}
		return env
	}
	node, err := NewFromEnvMap(base(Env{"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT": "true", "OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT_DOH_URL": " https://223.5.5.5/dns-query "}))
	if err != nil {
		t.Fatal(err)
	}
	if got := node.Adapter.(*CodexAdapter).native().Config.CodexImageInputDoH; got != "https://223.5.5.5/dns-query" {
		t.Fatalf("DoH = %q", got)
	}
	if node, err := NewFromEnvMap(base(nil)); err != nil || node.Adapter.(*CodexAdapter).native().Config.CodexImageInputDoH != "" {
		t.Fatalf("default DoH must be empty: %v", err)
	}
	for _, env := range []Env{
		{"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT_DOH_URL": "https://1.1.1.1/dns-query"},
		{"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT": "true", "OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT_DOH_URL": "https://dns.google/dns-query"},
		{"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT": "true", "OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT_DOH_URL": "https://192.168.1.1/dns-query"},
		{"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT": "true", "OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT_DOH_URL": "http://1.1.1.1/dns-query"},
	} {
		if _, err := NewFromEnvMap(base(env)); err == nil {
			t.Fatalf("accepted %v", env)
		}
	}
}

func TestCodexImageInputTrustedSourcesEnvironment(t *testing.T) {
	base := Env{"OPENLINKER_AGENT_NODE_ADAPTER": "codex", "OPENLINKER_AGENT_NODE_SESSION_ISOLATION": "off"}
	with := func(extra Env) Env {
		env := Env{}
		for k, v := range base {
			env[k] = v
		}
		for k, v := range extra {
			env[k] = v
		}
		return env
	}
	node, err := NewFromEnvMap(with(Env{
		"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT":                 "true",
		"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT_TRUSTED_SOURCES": `["https://files.corp","http://10.20.0.5:9000"]`,
		"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT_TRUSTED_ONLY":    "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	config := node.Adapter.(*CodexAdapter).native().Config
	if len(config.CodexImageInputTrustedSources) != 2 || !config.CodexImageInputTrustedOnly {
		t.Fatalf("config %+v", config.CodexImageInputTrustedSources)
	}
	for _, env := range []Env{
		{"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT_TRUSTED_SOURCES": `["https://files.corp"]`},
		{"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT": "true", "OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT_TRUSTED_ONLY": "true"},
		{"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT": "true", "OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT_TRUSTED_SOURCES": `["files.corp"]`},
		{"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT": "true", "OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT_TRUSTED_SOURCES": `not json`},
		{"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT_CA_FILE": "/tmp/ca.pem"},
	} {
		if _, err := NewFromEnvMap(with(env)); err == nil {
			t.Fatalf("accepted %v", env)
		}
	}
}

func TestCodexFileInputEnvironment(t *testing.T) {
	base := func(extra Env) Env {
		env := Env{"OPENLINKER_AGENT_NODE_ADAPTER": "codex", "OPENLINKER_AGENT_NODE_SESSION_ISOLATION": "off"}
		for k, v := range extra {
			env[k] = v
		}
		return env
	}
	for value, want := range map[string]bool{"": false, "off": false, "0": false, "true": true, " YES ": true, "1": true} {
		node, err := NewFromEnvMap(base(Env{"OPENLINKER_AGENT_NODE_CODEX_FILE_INPUT": value}))
		if err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		config := node.Adapter.(*CodexAdapter).native().Config
		if config.CodexFileInput != want || config.CodexImageInput {
			t.Fatalf("%q: file=%t image=%t", value, config.CodexFileInput, config.CodexImageInput)
		}
	}
	if _, err := NewFromEnvMap(base(Env{"OPENLINKER_AGENT_NODE_CODEX_FILE_INPUT": "maybe-secret"})); err == nil || strings.Contains(err.Error(), "maybe-secret") {
		t.Fatalf("invalid value: %v", err)
	}
	// The shared download options apply with file input alone.
	node, err := NewFromEnvMap(base(Env{
		"OPENLINKER_AGENT_NODE_CODEX_FILE_INPUT":                  "true",
		"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT_DOH_URL":         "https://223.5.5.5/dns-query",
		"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT_TRUSTED_SOURCES": `["http://storage.internal:9000"]`,
		"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT_TRUSTED_ONLY":    "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	config := node.Adapter.(*CodexAdapter).native().Config
	if config.CodexImageInputDoH == "" || len(config.CodexImageInputTrustedSources) != 1 || !config.CodexImageInputTrustedOnly {
		t.Fatalf("options not applied: %+v", config)
	}
	for _, env := range []Env{
		{"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT_DOH_URL": "https://223.5.5.5/dns-query"},
		{"OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT_TRUSTED_SOURCES": `["https://storage.internal"]`},
	} {
		if _, err := NewFromEnvMap(base(env)); err == nil {
			t.Fatalf("accepted %v without image or file input", env)
		}
	}
}
