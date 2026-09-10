package providerpreflight

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/codexhome"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/provideroutput"
)

const fixturePrefix = "LC_OPENLINKER_PREFLIGHT_"

// The probe executes this test binary, never an installed provider. LC_-prefixed
// fixture controls survive the same environment filter used by the real probe.
func TestMain(m *testing.M) {
	if os.Getenv(fixturePrefix+"FIXTURE") != "1" {
		os.Exit(m.Run())
	}
	args := strings.Join(os.Args[1:], " ")
	if path := os.Getenv(fixturePrefix + "LOG"); path != "" {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			os.Exit(91)
		}
		_, _ = fmt.Fprintln(file, args)
		_ = file.Close()
	}
	if os.Getenv("LC_ALL") != "C" || os.Getenv("LANG") != "C" || os.Getenv("USER") == "spoofed" {
		os.Exit(92)
	}
	for _, key := range []string{"CODEX_API_KEY", "ANTHROPIC_API_KEY", "OPENLINKER_AGENT_TOKEN", "UNDECLARED_ENV"} {
		if os.Getenv(key) != "" {
			os.Exit(93)
		}
	}
	switch os.Getenv(fixturePrefix + "MODE") {
	case "hang":
		time.Sleep(time.Hour)
	case "stdout-overflow":
		_, _ = io.WriteString(os.Stdout, strings.Repeat("x", provideroutput.MaxBytes+1))
		os.Exit(0)
	case "stderr-overflow":
		_, _ = io.WriteString(os.Stderr, strings.Repeat("x", provideroutput.MaxBytes+1))
		os.Exit(0)
	}
	if args == os.Getenv(fixturePrefix+"FAIL_ARGS") {
		_, _ = fmt.Fprintln(os.Stderr, "fixture-private-diagnostic")
		os.Exit(17)
	}
	switch args {
	case "--version":
		_, _ = fmt.Fprintln(os.Stdout, os.Getenv(fixturePrefix+"VERSION"))
	case "app-server --help", "--help":
		_, _ = fmt.Fprintln(os.Stdout, os.Getenv(fixturePrefix+"HELP"))
	default:
		os.Exit(94)
	}
	os.Exit(0)
}

func fixtureConfig(t *testing.T, provider string) (Config, string) {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "probes")
	version, help := "codex-cli 0.153.0", "app-server generate-json-schema --listen --config --disable"
	if provider == "claude" {
		version = "2.1.259 (Claude Code)"
		help = "--safe-mode --bare --no-chrome --disable-slash-commands --permission-mode --resume stream-json --verbose --include-partial-messages --strict-mcp-config"
	}
	return Config{Provider: provider, Bin: bin, Env: []string{
		fixturePrefix + "FIXTURE=1", fixturePrefix + "VERSION=" + version,
		fixturePrefix + "HELP=" + help, fixturePrefix + "LOG=" + log,
		"LC_ALL=discard", "LANG=discard", "USER=spoofed", "UNDECLARED_ENV=private",
		"CODEX_API_KEY=private", "ANTHROPIC_API_KEY=private", "OPENLINKER_AGENT_TOKEN=private",
	}}, log
}

func TestCheckProbesOnlyFixedReadOnlyArguments(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			config, log := fixtureConfig(t, provider)
			if provider == "codex" && !codexhome.Supported {
				_, err := Check(context.Background(), config)
				if err == nil || err.Error() != "isolated Codex app-server requires a POSIX host; Windows DACL isolation is not implemented" {
					t.Fatalf("unsupported isolation diagnostic = %v", err)
				}
				if _, err := os.Stat(log); !os.IsNotExist(err) {
					t.Fatal("unsupported Codex host started a probe")
				}
				return
			}
			config.Provider = " " + strings.ToUpper(provider) + " "
			config.Bin = " " + config.Bin + " "
			version, err := Check(context.Background(), config)
			wantVersion, wantProbes := MinimumCodexVersion, "--version\napp-server --help\n"
			if provider == "claude" {
				wantVersion, wantProbes = MinimumClaudeVersion, "--version\n--help\n"
			}
			if err != nil || version != wantVersion {
				t.Fatalf("Check = %q, %v; want %q", version, err, wantVersion)
			}
			probes, err := os.ReadFile(log)
			if err != nil || string(probes) != wantProbes {
				t.Fatalf("probes = %q, %v; want %q", probes, err, wantProbes)
			}
		})
	}
}

func TestCheckRejectsUnsupportedOrUnrecognizedVersions(t *testing.T) {
	for _, test := range []struct {
		provider, raw, want string
	}{
		{"codex", "codex-cli 0.152.9", ""},
		{"codex", "codex-cli 0.154.0-alpha.1", ""},
		{"codex", "other-cli 0.153.0", ""},
		{"codex", "0.153.0", ""},
		{"codex", "codex-cli 1.0.0", "1.0.0"},
		{"codex", "codex-cli 0.153.0+build.7", "0.153.0+build.7"},
		{"claude", "2.1.258 (Claude Code)", ""},
		{"claude", "3.0.0-beta (Claude Code)", ""},
		{"claude", "2.1.259", ""},
		{"claude", "3.0.0 (Claude Code)", "3.0.0"},
	} {
		t.Run(test.provider+"/"+test.raw, func(t *testing.T) {
			if test.provider == "codex" && !codexhome.Supported {
				t.Skip("Codex isolation is unavailable on this host")
			}
			config, log := fixtureConfig(t, test.provider)
			config.Env = append(config.Env, fixturePrefix+"VERSION="+test.raw)
			version, err := Check(context.Background(), config)
			if test.want != "" {
				if err != nil || version != test.want {
					t.Fatalf("Check = %q, %v; want %q", version, err, test.want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "requires a stable version >= ") {
				t.Fatalf("unsupported version diagnostic = %v", err)
			}
			probes, readErr := os.ReadFile(log)
			if readErr != nil || string(probes) != "--version\n" {
				t.Fatalf("unsupported version continued probing: %q, %v", probes, readErr)
			}
		})
	}
}

func TestCheckRequiresEveryCapability(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		if provider == "codex" && !codexhome.Supported {
			continue
		}
		baseline, _ := fixtureConfig(t, provider)
		var flags []string
		for _, entry := range baseline.Env {
			if help, ok := strings.CutPrefix(entry, fixturePrefix+"HELP="); ok {
				flags = strings.Fields(help)
			}
		}
		for _, missing := range flags {
			t.Run(provider+"/"+missing, func(t *testing.T) {
				config, _ := fixtureConfig(t, provider)
				help := strings.Replace(strings.Join(flags, " "), missing, "", 1)
				config.Env = append(config.Env, fixturePrefix+"HELP="+help)
				_, err := Check(context.Background(), config)
				want := "codex CLI app-server help is missing required generate-json-schema/stdio/config capabilities"
				if provider == "claude" {
					want = "claude CLI is missing required capability " + missing
				}
				if err == nil || err.Error() != want {
					t.Fatalf("missing capability diagnostic = %v, want %q", err, want)
				}
			})
		}
	}
}

func TestCheckFiltersInheritedEnvironment(t *testing.T) {
	config, _ := fixtureConfig(t, "claude")
	for _, entry := range config.Env {
		key, value, _ := strings.Cut(entry, "=")
		t.Setenv(key, value)
	}
	config.Env = nil
	if _, err := Check(context.Background(), config); err != nil {
		t.Fatalf("inherited environment was not filtered: %v", err)
	}
}

func TestCheckPreservesLookupAndProbeFailures(t *testing.T) {
	if _, err := Check(context.Background(), Config{Provider: " Unknown "}); err == nil || err.Error() != `unknown provider "unknown"` {
		t.Fatalf("unknown provider diagnostic = %v", err)
	}
	if _, err := Check(context.Background(), Config{Provider: "claude", Bin: filepath.Join(t.TempDir(), "missing")}); err == nil || !strings.HasPrefix(err.Error(), "claude provider CLI was not found: ") {
		t.Fatalf("missing CLI diagnostic = %v", err)
	}
	for _, args := range []string{"--version", "--help"} {
		t.Run(args, func(t *testing.T) {
			config, _ := fixtureConfig(t, "claude")
			config.Env = append(config.Env, fixturePrefix+"FAIL_ARGS="+args)
			_, err := Check(context.Background(), config)
			if err == nil || !strings.HasPrefix(err.Error(), "claude CLI compatibility probe ["+args+"] failed: ") || strings.Contains(err.Error(), "fixture-private-diagnostic") {
				t.Fatalf("failed probe diagnostic = %v", err)
			}
		})
	}
}

func TestCheckBoundsBothOutputPipesAndHonorsContext(t *testing.T) {
	for _, mode := range []string{"stdout-overflow", "stderr-overflow", "hang"} {
		t.Run(mode, func(t *testing.T) {
			config, _ := fixtureConfig(t, "claude")
			config.Env = append(config.Env, fixturePrefix+"MODE="+mode)
			timeout := 5 * time.Second
			if mode == "hang" {
				timeout = 100 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			_, err := Check(ctx, config)
			if err == nil || !strings.HasPrefix(err.Error(), "claude CLI compatibility probe [--version] failed: ") {
				t.Fatalf("unbounded or successful invalid probe: %v", err)
			}
			if mode != "hang" && ctx.Err() != nil {
				t.Fatal("overflow was only stopped by the caller deadline")
			}
			if mode == "hang" && ctx.Err() != context.DeadlineExceeded {
				t.Fatalf("probe did not honor caller deadline: %v", ctx.Err())
			}
		})
	}
}

func TestVersionAtLeastRejectsPrereleasesAndMalformedNumbers(t *testing.T) {
	for _, test := range []struct {
		version string
		want    bool
	}{
		{"0.153.0", true}, {"0.153.0+build.7", true}, {"1.0.0", true},
		{"0.152.9", false}, {"1.0.0-alpha", false}, {"0.153", false},
		{"0.153.bad", false}, {"0.bad.0", false}, {"bad.0.0", false},
	} {
		if got := versionAtLeast(test.version, MinimumCodexVersion); got != test.want {
			t.Errorf("versionAtLeast(%q) = %t, want %t", test.version, got, test.want)
		}
	}
}
