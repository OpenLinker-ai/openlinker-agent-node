// Package providerpreflight checks CLI compatibility using bounded, read-only
// probes. It does not run model tasks or own product execution policy.
package providerpreflight

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/codexhome"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/provideroutput"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerprocess"
)

// These are tested compatibility baselines, not claims about when individual
// flags were introduced. The Provider images must pin these versions or newer.
const MinimumCodexVersion = "0.153.0"
const MinimumClaudeVersion = "2.1.259"

// Config contains only the executable identity and candidate environment needed
// for a compatibility probe. Environment filtering remains fail-closed.
type Config struct {
	Provider string
	Bin      string
	Env      []string
}

var providerVersionPattern = regexp.MustCompile(`^(?:codex-cli )?(\d+)\.(\d+)\.(\d+)([-+][^ ]+)?(?: \(Claude Code\))?$`)

// Check runs once at doctor/Worker startup, before accepting leases.
// No model request is made. Capability checks also catch removed flags in newer
// releases and wrappers that merely print a supported version.
func Check(ctx context.Context, config Config) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(config.Provider))
	if provider != "claude" && provider != "codex" {
		return "", fmt.Errorf("unknown provider %q", provider)
	}
	if provider == "codex" && !codexhome.Supported {
		return "", fmt.Errorf("isolated Codex app-server requires a POSIX host; Windows DACL isolation is not implemented")
	}
	bin := strings.TrimSpace(config.Bin)
	if bin == "" {
		bin = provider
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("%s provider CLI was not found: %w", provider, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	probe := func(args ...string) (string, error) {
		command := exec.CommandContext(ctx, path, args...) // #nosec G204 -- operator-configured CLI, fixed read-only probe arguments.
		providerprocess.Configure(command)
		environment := config.Env
		if environment == nil {
			environment = os.Environ()
		}
		command.Env = append(providerprocess.Environment(environment, nil), "LC_ALL=C", "LANG=C")
		stdout, stderr := provideroutput.NewLimitedBuffer(cancel), provideroutput.NewLimitedBuffer(cancel)
		command.Stdout, command.Stderr = stdout, stderr
		if err := command.Run(); err != nil {
			return "", fmt.Errorf("%s CLI compatibility probe %v failed: %w", provider, args, err)
		}
		if err := provideroutput.LimitError(provider, stdout, stderr); err != nil {
			return "", err
		}
		return strings.TrimSpace(stdout.String()), nil
	}
	return CheckProbe(ctx, provider, func(_ context.Context, args ...string) (string, error) { return probe(args...) })
}

// CheckProbe validates the same native version/capability contract through a
// caller-owned isolated transport. The probe must bound output and never run a
// model task. Native Check continues to use its existing local process policy.
func CheckProbe(ctx context.Context, provider string, probe func(context.Context, ...string) (string, error)) (string, error) {
	minimum := MinimumCodexVersion
	if provider == "claude" {
		minimum = MinimumClaudeVersion
	} else if provider != "codex" {
		return "", fmt.Errorf("unknown provider %q", provider)
	}
	raw, err := probe(ctx, "--version")
	if err != nil {
		return "", err
	}
	version := providerVersionPattern.FindStringSubmatch(raw)
	if version == nil || (provider == "codex" && !strings.HasPrefix(raw, "codex-cli ")) ||
		(provider == "claude" && !strings.HasSuffix(raw, " (Claude Code)")) ||
		!versionAtLeast(version[1]+"."+version[2]+"."+version[3]+version[4], minimum) {
		return "", fmt.Errorf("%s CLI requires a stable version >= %s; installed version is unsupported or unrecognized", provider, minimum)
	}
	if provider == "codex" {
		// Version alone cannot identify distributions or wrappers that omit the
		// app-server surface. Probe help without starting a server or a session.
		help, err := probe(ctx, "app-server", "--help")
		if err != nil {
			return "", err
		}
		if !strings.Contains(help, "app-server") || !strings.Contains(help, "generate-json-schema") || !strings.Contains(help, "--listen") || !strings.Contains(help, "--config") || !strings.Contains(help, "--disable") {
			return "", fmt.Errorf("codex CLI app-server help is missing required generate-json-schema/stdio/config capabilities")
		}
		return version[1] + "." + version[2] + "." + version[3] + version[4], nil
	}
	// Claude remains a one-shot process with a streaming JSON transport.
	args := []string{"--help"}
	flags := []string{"--safe-mode", "--bare", "--no-chrome", "--disable-slash-commands", "--permission-mode", "--resume", "stream-json", "--verbose", "--include-partial-messages", "--strict-mcp-config"}

	help, err := probe(ctx, args...)
	if err != nil {
		return "", err
	}
	for _, flag := range flags {
		if !strings.Contains(help, flag) {
			return "", fmt.Errorf("%s CLI is missing required capability %s", provider, flag)
		}
	}
	return version[1] + "." + version[2] + "." + version[3] + version[4], nil
}

func versionAtLeast(version, minimum string) bool {
	// Reject prerelease builds; a higher major/minor must still be stable.
	version, _, _ = strings.Cut(version, "+")
	if strings.Contains(version, "-") {
		return false
	}
	actual, required := strings.Split(version, "."), strings.Split(minimum, ".")
	if len(actual) != 3 || len(required) != 3 {
		return false
	}
	for index := range actual {
		a, err := strconv.Atoi(actual[index])
		if err != nil {
			return false
		}
		b, _ := strconv.Atoi(required[index])
		if a != b {
			return a > b
		}
	}
	return true
}
