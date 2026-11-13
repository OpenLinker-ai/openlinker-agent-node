package adapters

import (
	"context"
	"errors"
	"net/netip"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/imageinput"
	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

// codexImageFetcher is replaced only by tests that serve images locally.
var codexImageFetcher = imageinput.Fetcher{}

// codexDoHResolver is replaced only by tests.
var codexDoHResolver = func(endpoint string) (func(context.Context, string) ([]netip.Addr, error), error) {
	resolver, err := imageinput.NewDoHResolver(endpoint)
	if err != nil {
		return nil, err
	}
	return resolver.Resolve, nil
}

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
		fetcher := codexImageFetcher
		if config.CodexImageInputDoH != "" && fetcher.Resolve == nil {
			// No fallback to host DNS: a fake-IP or filtered answer must not be used.
			if fetcher.Resolve, err = codexDoHResolver(config.CodexImageInputDoH); err != nil {
				return nil, err
			}
		}
		return fetcher.Materialize(ctx, workspace, fields)
	}()
	if err == nil {
		return set, nil, nil
	}
	// A provider timeout or Run cancellation is not an image failure.
	if ctx.Err() != nil {
		return nil, nil, errors.Join(ctx.Err(), err)
	}
	var classified *imageinput.Error
	if errors.As(err, &classified) {
		result := failedResult(classified.Code, boundedText(classified.Error(), 500, "image input failed"))
		return nil, &result, nil
	}
	return nil, nil, err
}
