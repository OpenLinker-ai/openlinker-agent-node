package agentnode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type AgentA2AClient struct {
	APIBase      string
	RuntimeToken string
	HTTPClient   *http.Client
}

func (c AgentA2AClient) CallAgent(ctx context.Context, currentRunID, targetAgentID string, input any, options CallAgentOptions) (any, error) {
	if currentRunID == "" {
		return nil, fmt.Errorf("currentRunID is required")
	}
	if targetAgentID == "" {
		return nil, fmt.Errorf("targetAgentID is required")
	}
	endpoint := options.Endpoint
	if endpoint == "" {
		endpoint = "/api/v1/agent-runtime/call-agent"
	}
	body := JSONMap{
		"current_run_id":  currentRunID,
		"target_agent_id": targetAgentID,
		"reason":          options.Reason,
		"input":           input,
	}
	if options.Metadata != nil {
		body["metadata"] = options.Metadata
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinAPIPath(c.APIBase, endpoint), bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", "Bearer "+c.RuntimeToken)
	req.Header.Set("content-type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	jsonBody, _ := readJSONResponse(res)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("A2A call failed with HTTP %d: %v", res.StatusCode, jsonBody)
	}
	return jsonBody, nil
}

type PublicA2AClient struct {
	APIBase    string
	Token      string
	HTTPClient *http.Client
}

func (c PublicA2AClient) SendMessage(ctx context.Context, slug, text string) (any, error) {
	body := JSONMap{
		"jsonrpc": "2.0",
		"id":      "msg-openlinker-agent-node",
		"method":  "SendMessage",
		"params": JSONMap{
			"message": JSONMap{
				"messageId": "msg-openlinker-agent-node",
				"role":      "user",
				"parts": []JSONMap{{
					"kind": "text",
					"text": text,
				}},
			},
			"configuration": JSONMap{
				"blocking":            true,
				"acceptedOutputModes": []string{"application/json", "text/plain"},
			},
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinAPIPath(c.APIBase, "/api/v1/a2a/agents/"+slug), bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", "Bearer "+c.Token)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("a2a-version", "1.0")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	jsonBody, _ := readJSONResponse(res)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("A2A SendMessage failed with HTTP %d: %v", res.StatusCode, jsonBody)
	}
	if bodyMap, ok := jsonBody.(map[string]any); ok && bodyMap["error"] != nil {
		return nil, fmt.Errorf("A2A SendMessage returned error: %v", bodyMap["error"])
	}
	return jsonBody, nil
}
