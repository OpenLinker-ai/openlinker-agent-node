// Package agenthost defines the embedding executable's transport contract.
// It has no CLI, SDK, Browser runtime, or provider dependencies.
package agenthost

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

//go:embed contract.json
var contractJSON []byte

var contract = func() struct {
	Protocol        string   `json:"protocol"`
	BrowserProxy    []string `json:"browser_proxy"`
	DelegationProxy []string `json:"delegation_proxy"`
} {
	var value struct {
		Protocol        string   `json:"protocol"`
		BrowserProxy    []string `json:"browser_proxy"`
		DelegationProxy []string `json:"delegation_proxy"`
	}
	if err := json.Unmarshal(contractJSON, &value); err != nil {
		panic(err)
	}
	return value
}()

func BrowserProxyArguments(provider string) []string {
	return append(append([]string(nil), contract.BrowserProxy...), provider)
}
func DelegationProxyArguments(provider string) []string {
	return append(append([]string(nil), contract.DelegationProxy...), provider)
}

type Capabilities struct {
	Protocol        string `json:"protocol"`
	BrowserProxy    bool   `json:"browser_proxy"`
	DelegationProxy bool   `json:"delegation_proxy"`
}

func SupportedCapabilities() Capabilities { return Capabilities{contract.Protocol, true, true} }

// Resolve verifies the executable contract rather than assuming that the
// current executable implements hidden CLI commands. Explicit alternate hosts
// must perform the same handshake. The probe never receives credentials.
func Resolve(ctx context.Context, configured, capability string) (string, error) {
	bin := strings.TrimSpace(configured)
	var err error
	if bin == "" {
		bin, err = os.Executable()
		if err != nil {
			return "", err
		}
	}
	bin, err = exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("Agent transport host is unavailable: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, bin, "plugin", "capabilities") // #nosec G204 -- operator-selected executable, fixed read-only arguments.
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if key == "PATH" || key == "SystemRoot" {
			command.Env = append(command.Env, item)
		}
	}
	if command.Env == nil {
		command.Env = []string{}
	}
	output := &probeOutput{}
	command.WaitDelay = time.Second
	command.Stdout = output
	if err := command.Run(); err != nil {
		return "", errors.New("Agent transport host lacks plugin capabilities; configure a compatible OpenLinker CLI binary")
	}
	var capabilities Capabilities
	if json.Unmarshal(output.Bytes(), &capabilities) != nil || capabilities.Protocol != contract.Protocol ||
		(capability != "browser_proxy" && capability != "delegation_proxy") ||
		(capability == "browser_proxy" && !capabilities.BrowserProxy) ||
		(capability == "delegation_proxy" && !capabilities.DelegationProxy) {
		return "", errors.New("Agent transport host does not support the required protocol/capability")
	}
	return bin, nil
}

type probeOutput struct{ buffer bytes.Buffer }

func (output *probeOutput) Bytes() []byte { return output.buffer.Bytes() }

func (output *probeOutput) Write(value []byte) (int, error) {
	if output.buffer.Len()+len(value) > 8192 {
		return 0, errors.New("host capability response exceeds limit")
	}
	return output.buffer.Write(value)
}
