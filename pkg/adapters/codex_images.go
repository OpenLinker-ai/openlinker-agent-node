package adapters

import (
	"context"
	"errors"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/imageinput"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

// codexImageFetcher is replaced only by tests that serve images locally.
var codexImageFetcher = imageinput.Fetcher{}

// materializeCodexImages returns a nil-safe set, a classified failure result,
// or an unclassified error (for example cancellation). With the switch off it
// never inspects the input.
func materializeCodexImages(ctx context.Context, run RunContext, workspace string, config ProviderConfig) (*imageinput.Set, *openlinker.RuntimeResult, error) {
	if !config.CodexImageInput {
		return nil, nil, nil
	}
	set, err := func() (*imageinput.Set, error) {
		fields, err := imageinput.Fields(run.Input)
		if err != nil || len(fields) == 0 {
			return nil, err
		}
		return codexImageFetcher.Materialize(ctx, workspace, fields)
	}()
	if err == nil {
		return set, nil, nil
	}
	var classified *imageinput.Error
	if errors.As(err, &classified) {
		result := failedResult(classified.Code, boundedText(classified.Error(), 500, "image input failed"))
		return nil, &result, nil
	}
	return nil, nil, err
}
