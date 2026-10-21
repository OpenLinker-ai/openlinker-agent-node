package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"strings"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

// This offline administrative entry only delegates durable key preparation to
// the SDK. It does not initialize a Provider, register an Agent or start a Worker.
func prepareCredentialCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "prepare" {
		return errors.New("usage: runtime-credential prepare --data-dir PATH --node-id ID --agent-id ID --worker-id ID")
	}
	options := openlinker.RuntimeCredentialPreparation{}
	flags := flag.NewFlagSet("runtime-credential prepare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&options.DataDir, "data-dir", "", "existing stopped Runtime data directory")
	flags.StringVar(&options.NodeID, "node-id", "", "existing Core Node ID")
	flags.StringVar(&options.AgentID, "agent-id", "", "existing Core Agent ID")
	flags.StringVar(&options.WorkerID, "worker-id", "", "existing Worker ID in that directory")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("runtime-credential prepare does not accept positional arguments")
	}
	options.ConfiguredNodeID = strings.TrimSpace(os.Getenv("OPENLINKER_NODE_ID"))
	options.AgentToken = strings.TrimSpace(os.Getenv("OPENLINKER_AGENT_TOKEN"))
	if configuredAgent := strings.TrimSpace(os.Getenv("OPENLINKER_AGENT_ID")); configuredAgent != "" && configuredAgent != options.AgentID {
		return errors.New("credential preparation Agent ID differs from installation configuration")
	}
	prepared, err := openlinker.PrepareRuntimeCredential(options)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(prepared)
}
