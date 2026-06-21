package agentnode

import (
	"context"
	"fmt"
	"net/http"
	"time"

	openlinker "github.com/kinzhi/openlinker-go"
)

type RuntimePullConnector struct {
	APIBase      string
	RuntimeToken string
	Wait         time.Duration
	Heartbeat    time.Duration
	EmptyRetry   time.Duration
	MaxRuns      int
	StopOnEmpty  bool
	HTTPClient   *http.Client

	connector *openlinker.RuntimePullConnector
}

func (c *RuntimePullConnector) SupportsLiveEvents() bool {
	return false
}

func (c *RuntimePullConnector) Start(ctx context.Context, handlers ConnectorHandlers) error {
	if c.APIBase == "" {
		return fmt.Errorf("api base is required")
	}
	if c.RuntimeToken == "" {
		return fmt.Errorf("runtime token is required")
	}
	client, err := c.sdkClient()
	if err != nil {
		return err
	}
	connector := openlinker.NewRuntimePullConnector(client)
	connector.Wait = c.Wait
	connector.Heartbeat = c.Heartbeat
	connector.EmptyRetry = c.EmptyRetry
	connector.MaxRuns = c.MaxRuns
	connector.StopOnEmpty = c.StopOnEmpty
	if err := connector.Start(ctx, sdkRuntimeHandlers(handlers)); err != nil {
		return err
	}
	c.Wait = connector.Wait
	c.Heartbeat = connector.Heartbeat
	c.EmptyRetry = connector.EmptyRetry
	c.connector = connector
	return nil
}

func (c *RuntimePullConnector) Stop(ctx context.Context) error {
	if c.connector == nil {
		return nil
	}
	return c.connector.Stop(ctx)
}

func (c *RuntimePullConnector) SendRunEvent(ctx context.Context, runID string, event RunEvent) error {
	return nil
}

func (c *RuntimePullConnector) CompleteRun(ctx context.Context, runID string, result RunResult) error {
	if c.connector == nil {
		return fmt.Errorf("runtime pull connector is not started")
	}
	return c.connector.CompleteRun(ctx, runID, sdkRunResult(result))
}

func (c *RuntimePullConnector) sdkClient() (*openlinker.Client, error) {
	return openlinker.NewClient(
		c.APIBase,
		openlinker.WithRuntimeToken(c.RuntimeToken),
		openlinker.WithHTTPClient(c.HTTPClient),
	)
}
