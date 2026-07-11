package agentnode

import (
	"context"
	"net/http"
	"net/url"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type PublicA2AClient struct {
	APIBase    string
	Token      string
	HTTPClient *http.Client
}

func (client PublicA2AClient) SendMessage(ctx context.Context, slug, text string) (any, error) {
	a2aClient, err := openlinker.NewA2AClient(
		joinAPIPath(client.APIBase, "/api/v1/a2a/agents/"+url.PathEscape(slug)),
		openlinker.WithA2AToken(client.Token),
		openlinker.WithA2AHTTPClient(client.HTTPClient),
	)
	if err != nil {
		return nil, err
	}
	return a2aClient.SendMessage(ctx, openlinker.NewA2ATextMessageParams("msg-openlinker-agent-node", text, []string{"application/json", "text/plain"}))
}
