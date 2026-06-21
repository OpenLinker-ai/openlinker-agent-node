package agentnode

import (
	"context"
	"fmt"
	"net/http"
	"time"

	openlinker "github.com/kinzhi/openlinker-go"
)

type RuntimeWSConnector struct {
	APIBase      string
	RuntimeToken string
	Reconnect    bool
	ReconnectMin time.Duration
	ReconnectMax time.Duration
	Dialer       openlinker.WebSocketDialer
	HTTPClient   *http.Client

	connector *openlinker.RuntimeWSConnector
}

func (c *RuntimeWSConnector) SupportsLiveEvents() bool {
	return true
}

func (c *RuntimeWSConnector) Start(ctx context.Context, handlers ConnectorHandlers) error {
	if c.APIBase == "" {
		return fmt.Errorf("api base is required")
	}
	if c.RuntimeToken == "" {
		return fmt.Errorf("runtime token is required")
	}
	client, err := openlinker.NewClient(
		c.APIBase,
		openlinker.WithRuntimeToken(c.RuntimeToken),
		openlinker.WithHTTPClient(c.HTTPClient),
	)
	if err != nil {
		return err
	}
	connector := openlinker.NewRuntimeWSConnector(client)
	connector.Reconnect = c.Reconnect
	connector.ReconnectMin = c.ReconnectMin
	connector.ReconnectMax = c.ReconnectMax
	connector.Dialer = c.Dialer
	if err := connector.Start(ctx, sdkRuntimeHandlers(handlers)); err != nil {
		return err
	}
	c.ReconnectMin = connector.ReconnectMin
	c.ReconnectMax = connector.ReconnectMax
	c.connector = connector
	return nil
}

func (c *RuntimeWSConnector) Stop(ctx context.Context) error {
	if c.connector == nil {
		return nil
	}
	return c.connector.Stop(ctx)
}

func (c *RuntimeWSConnector) SendRunEvent(ctx context.Context, runID string, event RunEvent) error {
	if c.connector == nil {
		return fmt.Errorf("runtime websocket connector is not started")
	}
	return c.connector.SendRunEvent(ctx, runID, openlinker.AgentEvent{
		EventType: event.EventType,
		Payload:   event.Payload,
	})
}

func (c *RuntimeWSConnector) CompleteRun(ctx context.Context, runID string, result RunResult) error {
	if c.connector == nil {
		return fmt.Errorf("runtime websocket connector is not started")
	}
	return c.connector.CompleteRun(ctx, runID, sdkRunResult(result))
}
