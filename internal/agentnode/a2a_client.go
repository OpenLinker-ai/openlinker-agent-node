package agentnode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
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
	client, err := openlinker.NewClient(
		c.APIBase,
		openlinker.WithRuntimeToken(c.RuntimeToken),
		openlinker.WithHTTPClient(c.HTTPClient),
	)
	if err != nil {
		return nil, err
	}
	resp, err := client.CallAgentAt(ctx, options.Endpoint, openlinker.CallAgentRequest{
		CurrentRunID:     currentRunID,
		TargetAgentID:    targetAgentID,
		Reason:           options.Reason,
		Input:            input,
		Metadata:         options.Metadata,
		ContextID:        options.ContextID,
		TraceID:          options.TraceID,
		ReferenceTaskIDs: append([]string{}, options.ReferenceTaskIDs...),
		TaskCallback:     openlinkerTaskCallback(options.TaskCallback),
	})
	if err != nil {
		return nil, err
	}
	return runResponseToJSONMap(resp)
}

func openlinkerTaskCallback(cfg *TaskCallbackConfig) *openlinker.TaskCallbackConfig {
	if cfg == nil {
		return nil
	}
	var auth *openlinker.TaskCallbackAuthentication
	if cfg.Authentication != nil {
		auth = &openlinker.TaskCallbackAuthentication{
			Scheme:      cfg.Authentication.Scheme,
			Credentials: cfg.Authentication.Credentials,
		}
	}
	return &openlinker.TaskCallbackConfig{
		URL:            cfg.URL,
		Token:          cfg.Token,
		Secret:         cfg.Secret,
		Authentication: auth,
		Metadata:       cfg.Metadata,
		EventTypes:     append([]string{}, cfg.EventTypes...),
	}
}

func runResponseToJSONMap(resp *openlinker.RunResponse) (map[string]any, error) {
	encoded, err := json.Marshal(resp)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		return nil, err
	}
	return body, nil
}

type PublicA2AClient struct {
	APIBase    string
	Token      string
	HTTPClient *http.Client
}

func (c PublicA2AClient) SendMessage(ctx context.Context, slug, text string) (any, error) {
	client, err := openlinker.NewA2AClient(
		joinAPIPath(c.APIBase, "/api/v1/a2a/agents/"+url.PathEscape(slug)),
		openlinker.WithA2AToken(c.Token),
		openlinker.WithA2AHTTPClient(c.HTTPClient),
	)
	if err != nil {
		return nil, err
	}
	return client.SendMessage(ctx, openlinker.NewA2ATextMessageParams("msg-openlinker-agent-node", text, []string{"application/json", "text/plain"}))
}
