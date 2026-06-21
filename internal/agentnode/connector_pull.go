package agentnode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
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

	ctx       context.Context
	cancel    context.CancelFunc
	handlers  ConnectorHandlers
	wg        sync.WaitGroup
	processed int
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
	if c.Wait <= 0 {
		c.Wait = 25 * time.Second
	}
	if c.Heartbeat <= 0 {
		c.Heartbeat = 60 * time.Second
	}
	if c.EmptyRetry <= 0 {
		c.EmptyRetry = 5 * time.Second
	}
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.handlers = handlers
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		if err := c.loop(); err != nil && c.ctx.Err() == nil && c.handlers.OnError != nil {
			c.handlers.OnError(err)
		}
	}()
	return nil
}

func (c *RuntimePullConnector) Stop(ctx context.Context) error {
	if c.cancel != nil {
		c.cancel()
	}
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (c *RuntimePullConnector) loop() error {
	lastHeartbeat := time.Time{}
	for c.ctx.Err() == nil && (c.MaxRuns == 0 || c.processed < c.MaxRuns) {
		if time.Since(lastHeartbeat) >= c.Heartbeat {
			_, _, _ = c.request(c.ctx, http.MethodPost, "/api/v1/agent-runtime/heartbeat", nil)
			lastHeartbeat = time.Now()
		}
		status, body, res := c.request(c.ctx, http.MethodGet, fmt.Sprintf("/api/v1/agent-runtime/runs/claim?wait=%d", int(c.Wait.Seconds())), nil)
		switch status {
		case http.StatusOK:
			assignment, err := assignmentFromClaim(body)
			if err != nil {
				return err
			}
			if c.handlers.OnAssigned != nil {
				c.handlers.OnAssigned(assignment)
			}
			c.processed++
		case http.StatusNoContent:
			if c.StopOnEmpty {
				return nil
			}
			if err := sleepContext(c.ctx, retryAfterDuration(res, c.EmptyRetry)); err != nil {
				return err
			}
		case http.StatusTooManyRequests:
			if err := sleepContext(c.ctx, retryAfterDuration(res, c.EmptyRetry)); err != nil {
				return err
			}
		default:
			if c.handlers.OnError != nil {
				c.handlers.OnError(fmt.Errorf("runtime pull claim returned %d: %v", status, body))
			}
			if err := sleepContext(c.ctx, c.EmptyRetry); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *RuntimePullConnector) SendRunEvent(ctx context.Context, runID string, event RunEvent) error {
	return nil
}

func (c *RuntimePullConnector) CompleteRun(ctx context.Context, runID string, result RunResult) error {
	body := JSONMap{
		"status":      result.Status,
		"output":      result.Output,
		"events":      result.Events,
		"error":       result.Error,
		"duration_ms": result.DurationMS,
	}
	status, jsonBody, _ := c.request(ctx, http.MethodPost, "/api/v1/agent-runtime/runs/"+url.PathEscape(runID)+"/result", body)
	if status != http.StatusOK {
		return fmt.Errorf("runtime pull result returned %d: %v", status, jsonBody)
	}
	return nil
}

func (c *RuntimePullConnector) request(ctx context.Context, method, pathName string, body any) (int, any, *http.Response) {
	var reader *bytes.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, joinAPIPath(c.APIBase, pathName), reader)
	if err != nil {
		return 0, JSONMap{"error": err.Error()}, nil
	}
	req.Header.Set("authorization", "Bearer "+c.RuntimeToken)
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, JSONMap{"error": err.Error()}, nil
	}
	jsonBody, _ := readJSONResponse(res)
	return res.StatusCode, jsonBody, res
}

func assignmentFromClaim(body any) (Assignment, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return Assignment{}, err
	}
	var assignment Assignment
	if err := json.Unmarshal(encoded, &assignment); err != nil {
		return Assignment{}, err
	}
	assignment.Type = "run.assigned"
	return assignment, nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
