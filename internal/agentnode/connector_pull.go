package agentnode

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

type RuntimePullConnector struct {
	APIBase     string
	AgentToken  string
	Wait        time.Duration
	Heartbeat   time.Duration
	EmptyRetry  time.Duration
	MaxRuns     int
	StopOnEmpty bool
	HTTPClient  *http.Client

	mu        sync.RWMutex
	connector *openlinker.RuntimePullConnector
}

func (c *RuntimePullConnector) SupportsLiveEvents() bool {
	return false
}

func (c *RuntimePullConnector) Start(ctx context.Context, handlers ConnectorHandlers) error {
	if c.APIBase == "" {
		return fmt.Errorf("api base is required")
	}
	if c.AgentToken == "" {
		return fmt.Errorf("agent token is required")
	}
	runtime, err := c.sdkRuntime()
	if err != nil {
		return err
	}
	connector := openlinker.NewRuntimePullConnector(runtime)
	c.applyDefaults()
	connector.Wait = c.Wait
	connector.Heartbeat = c.Heartbeat
	connector.EmptyRetry = c.EmptyRetry
	connector.MaxRuns = c.MaxRuns
	connector.StopOnEmpty = c.StopOnEmpty
	c.setConnector(connector)
	if err := connector.Start(ctx, sdkRuntimeHandlers(handlers)); err != nil {
		c.setConnector(nil)
		return err
	}
	return nil
}

func (c *RuntimePullConnector) Stop(ctx context.Context) error {
	connector := c.currentConnector()
	if connector == nil {
		return nil
	}
	return connector.Stop(ctx)
}

func (c *RuntimePullConnector) SendRunEvent(ctx context.Context, runID string, event RunEvent) error {
	return nil
}

func (c *RuntimePullConnector) CompleteRun(ctx context.Context, runID string, result RunResult) error {
	connector := c.currentConnector()
	if connector == nil {
		return fmt.Errorf("runtime pull connector is not started")
	}
	return connector.CompleteRun(ctx, runID, sdkRunResult(result))
}

func (c *RuntimePullConnector) sdkRuntime() (*openlinker.Runtime, error) {
	return openlinker.NewRuntime(
		c.APIBase,
		openlinker.WithAgentToken(c.AgentToken),
		openlinker.WithHTTPClient(c.HTTPClient),
	)
}

func (c *RuntimePullConnector) applyDefaults() {
	if c.Wait <= 0 {
		c.Wait = 25 * time.Second
	}
	if c.Heartbeat <= 0 {
		c.Heartbeat = 60 * time.Second
	}
	if c.EmptyRetry <= 0 {
		c.EmptyRetry = 5 * time.Second
	}
}

func (c *RuntimePullConnector) setConnector(connector *openlinker.RuntimePullConnector) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connector = connector
}

func (c *RuntimePullConnector) currentConnector() *openlinker.RuntimePullConnector {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connector
}
