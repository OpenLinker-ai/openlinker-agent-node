package agentnode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type HTTPAdapter struct {
	URL        string
	Headers    map[string]string
	HTTPClient *http.Client
	Timeout    time.Duration
}

func (a HTTPAdapter) Run(ctx context.Context, input any, runCtx RunContext) (any, error) {
	if a.URL == "" {
		return nil, fmt.Errorf("OPENLINKER_AGENT_NODE_HTTP_URL is required")
	}
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(buildAdapterEnvelope(input, runCtx))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, a.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	for key, value := range a.Headers {
		req.Header.Set(key, value)
	}

	client := a.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	jsonBody, _ := readJSONResponse(res)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP adapter returned %d: %v", res.StatusCode, jsonBody)
	}
	if bodyMap, ok := jsonBody.(map[string]any); ok {
		if output, ok := bodyMap["output"]; ok {
			return output, nil
		}
	}
	return jsonBody, nil
}
