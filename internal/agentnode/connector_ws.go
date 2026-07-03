package agentnode

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type RuntimeWSConnector struct {
	APIBase      string
	AgentToken   string
	Reconnect    bool
	ReconnectMin time.Duration
	ReconnectMax time.Duration
	Heartbeat    time.Duration
	Dialer       openlinker.WebSocketDialer
	HTTPClient   *http.Client

	mu        sync.RWMutex
	connector *openlinker.RuntimeWSConnector
}

func (c *RuntimeWSConnector) SupportsLiveEvents() bool {
	return true
}

func (c *RuntimeWSConnector) Start(ctx context.Context, handlers ConnectorHandlers) error {
	if c.APIBase == "" {
		return fmt.Errorf("api base is required")
	}
	if c.AgentToken == "" {
		return fmt.Errorf("agent token is required")
	}
	client, err := openlinker.NewClient(
		c.APIBase,
		openlinker.WithAgentToken(c.AgentToken),
		openlinker.WithHTTPClient(c.HTTPClient),
	)
	if err != nil {
		return err
	}
	connector := openlinker.NewRuntimeWSConnector(client)
	connector.Reconnect = c.Reconnect
	connector.ReconnectMin = c.ReconnectMin
	connector.ReconnectMax = c.ReconnectMax
	connector.Heartbeat = c.Heartbeat
	connector.Dialer = c.Dialer
	c.setConnector(connector)
	if err := connector.Start(ctx, sdkRuntimeHandlers(handlers)); err != nil {
		c.setConnector(nil)
		return err
	}
	c.mu.Lock()
	c.ReconnectMin = connector.ReconnectMin
	c.ReconnectMax = connector.ReconnectMax
	c.Heartbeat = connector.Heartbeat
	c.mu.Unlock()
	return nil
}

func (c *RuntimeWSConnector) Stop(ctx context.Context) error {
	connector := c.currentConnector()
	if connector == nil {
		return nil
	}
	return connector.Stop(ctx)
}

func (c *RuntimeWSConnector) SendRunEvent(ctx context.Context, runID string, event RunEvent) error {
	connector := c.currentConnector()
	if connector == nil {
		return fmt.Errorf("runtime websocket connector is not started")
	}
	return connector.SendRunEvent(ctx, runID, openlinker.AgentEvent{
		EventType: event.EventType,
		Payload:   event.Payload,
	})
}

func (c *RuntimeWSConnector) CompleteRun(ctx context.Context, runID string, result RunResult) error {
	connector := c.currentConnector()
	if connector == nil {
		return fmt.Errorf("runtime websocket connector is not started")
	}
	return connector.CompleteRun(ctx, runID, sdkRunResult(result))
}

func (c *RuntimeWSConnector) setConnector(connector *openlinker.RuntimeWSConnector) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connector = connector
}

func (c *RuntimeWSConnector) currentConnector() *openlinker.RuntimeWSConnector {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connector
}
