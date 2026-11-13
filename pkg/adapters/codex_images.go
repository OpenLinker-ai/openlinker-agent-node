package adapters

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/netip"
	"os"

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
		fetcher, err := codexImageFetcherFor(config, true)
		if err != nil {
			return nil, err
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

// codexImageFetcherFor validates and applies the image-input options. Every
// option requires image input, so a stray setting cannot go unnoticed.
// connect=false only validates (provider construction).
func codexImageFetcherFor(config ProviderConfig, connect bool) (imageinput.Fetcher, error) {
	fetcher := codexImageFetcher
	configured := config.CodexImageInputDoH != "" || len(config.CodexImageInputTrustedSources) != 0 ||
		config.CodexImageInputTrustedOnly || config.CodexImageInputCAFile != ""
	if !configured {
		return fetcher, nil
	}
	if !config.CodexImageInput {
		return fetcher, errors.New("image input DoH, trusted sources and CA file require Codex image input")
	}
	if config.CodexImageInputTrustedOnly && len(config.CodexImageInputTrustedSources) == 0 {
		return fetcher, errors.New("image input trusted-only mode requires trusted sources")
	}
	for _, raw := range config.CodexImageInputTrustedSources {
		origin, err := imageinput.ParseOrigin(raw)
		if err != nil {
			return fetcher, err
		}
		fetcher.Trusted = append(fetcher.Trusted, origin)
	}
	fetcher.TrustedOnly = config.CodexImageInputTrustedOnly
	if config.CodexImageInputCAFile != "" {
		pem, err := os.ReadFile(config.CodexImageInputCAFile)
		if err != nil {
			return fetcher, errors.New("image input CA file is not readable")
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if fetcher.TLS != nil && fetcher.TLS.RootCAs != nil {
			pool = fetcher.TLS.RootCAs.Clone()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return fetcher, errors.New("image input CA file contains no PEM certificate")
		}
		tlsConfig := &tls.Config{}
		if fetcher.TLS != nil {
			tlsConfig = fetcher.TLS.Clone()
		}
		tlsConfig.RootCAs = pool
		fetcher.TLS = tlsConfig
	}
	if config.CodexImageInputDoH != "" {
		if _, _, err := imageinput.ValidateDoHURL(config.CodexImageInputDoH); err != nil {
			return fetcher, err
		}
		if connect && fetcher.Resolve == nil {
			// Public hosts never fall back to host DNS; trusted hosts keep
			// using host DNS (TrustedResolve) so internal names resolve.
			resolve, err := codexDoHResolver(config.CodexImageInputDoH)
			if err != nil {
				return fetcher, err
			}
			fetcher.Resolve = resolve
		}
	}
	return fetcher, nil
}

// redactCodexInlineImages keeps inline image data out of the prompt text,
// including Core history that may repeat an earlier order input. The images
// themselves reach the model only as localImage inputs. Off: unchanged.
func redactCodexInlineImages(run RunContext, config ProviderConfig) RunContext {
	if !config.CodexImageInput {
		return run
	}
	generic := func(value any) any {
		raw, err := json.Marshal(value)
		if err != nil {
			return value
		}
		var decoded any
		if json.Unmarshal(raw, &decoded) != nil {
			return value
		}
		return imageinput.Redact(decoded)
	}
	run.Input = generic(run.Input)
	if run.Metadata != nil {
		if redacted, ok := generic(run.Metadata).(map[string]any); ok {
			run.Metadata = redacted
		}
	}
	if run.A2A != nil {
		if redacted, ok := generic(run.A2A).(map[string]any); ok {
			run.A2A = redacted
		}
	}
	if run.Conversation != nil {
		raw, _ := json.Marshal(generic(run.Conversation))
		var conversation ConversationContext
		if json.Unmarshal(raw, &conversation) == nil {
			run.Conversation = &conversation
		}
	}
	return run
}
