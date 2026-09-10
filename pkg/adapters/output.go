package adapters

import "github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/provideroutput"

const maxProviderOutputBytes = provideroutput.MaxBytes
const maxProviderDiagnosticBytes = provideroutput.MaxDiagnosticBytes

type outputTail = provideroutput.Tail

func boundedText(value string, maximum int, fallback string) string {
	return provideroutput.BoundedText(value, maximum, fallback)
}
