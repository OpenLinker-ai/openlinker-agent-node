package agentnode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
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
	client, err := c.sdkClient()
	if err != nil {
		return err
	}
	lastHeartbeat := time.Time{}
	for c.ctx.Err() == nil && (c.MaxRuns == 0 || c.processed < c.MaxRuns) {
		if time.Since(lastHeartbeat) >= c.Heartbeat {
			_, _ = client.HeartbeatAgent(c.ctx)
			lastHeartbeat = time.Now()
		}

		claimResult, err := client.ClaimRuntimeRunDetailed(c.ctx, openlinker.ClaimRuntimeRunParams{
			WaitSeconds: int32(c.Wait.Seconds()),
		})
		if err == nil && claimResult != nil && claimResult.Run != nil {
			if c.handlers.OnAssigned != nil {
				c.handlers.OnAssigned(assignmentFromClaim(claimResult.Run))
			}
			c.processed++
			continue
		}
		if err == nil {
			if c.StopOnEmpty {
				return nil
			}
			if err := sleepContext(c.ctx, retryAfterFromClaimResult(claimResult, c.EmptyRetry)); err != nil {
				return err
			}
			continue
		}

		var sdkErr *openlinker.Error
		if errors.As(err, &sdkErr) {
			if sdkErr.StatusCode == http.StatusTooManyRequests {
				if err := sleepContext(c.ctx, retryAfterFromSDKError(sdkErr, c.EmptyRetry)); err != nil {
					return err
				}
				continue
			}
			if c.handlers.OnError != nil {
				c.handlers.OnError(fmt.Errorf("runtime pull claim returned %d: %s", sdkErr.StatusCode, sdkErr.Message))
			}
		} else if c.handlers.OnError != nil {
			c.handlers.OnError(err)
		}
		if err := sleepContext(c.ctx, c.EmptyRetry); err != nil {
			return err
		}
	}
	return nil
}

func (c *RuntimePullConnector) SendRunEvent(ctx context.Context, runID string, event RunEvent) error {
	return nil
}

func (c *RuntimePullConnector) CompleteRun(ctx context.Context, runID string, result RunResult) error {
	client, err := c.sdkClient()
	if err != nil {
		return err
	}
	_, err = client.CompleteRuntimeRun(ctx, runID, openlinker.RuntimePullResultRequest{
		Status:     result.Status,
		Output:     result.Output,
		Events:     sdkEventsFromRunEvents(result.Events),
		Error:      sdkErrorFromAgentError(result.Error),
		DurationMS: int32(result.DurationMS),
	})
	return err
}

func (c *RuntimePullConnector) sdkClient() (*openlinker.Client, error) {
	return openlinker.NewClient(
		c.APIBase,
		openlinker.WithRuntimeToken(c.RuntimeToken),
		openlinker.WithHTTPClient(c.HTTPClient),
	)
}

func assignmentFromClaim(claim *openlinker.RuntimePullRunResponse) Assignment {
	return Assignment{
		Type:           "run.assigned",
		RunID:          claim.RunID,
		AgentID:        claim.AgentID,
		Input:          claim.Input,
		Metadata:       jsonMapFromAny(claim.Metadata),
		Source:         claim.Source,
		ResultEndpoint: claim.ResultEndpoint,
		ResultMethod:   claim.ResultMethod,
		ResultRequired: claim.ResultRequired,
		A2A:            jsonMapFromA2A(claim.A2A),
	}
}

func jsonMapFromA2A(a2a *openlinker.AgentA2AContext) JSONMap {
	if a2a == nil {
		return nil
	}
	out := JSONMap{}
	if a2a.CurrentRunID != "" {
		out["current_run_id"] = a2a.CurrentRunID
	}
	if a2a.ParentRunID != "" {
		out["parent_run_id"] = a2a.ParentRunID
	}
	if a2a.CallerAgentID != "" {
		out["caller_agent_id"] = a2a.CallerAgentID
	}
	if a2a.CallAgentEndpoint != "" {
		out["call_agent_endpoint"] = a2a.CallAgentEndpoint
	}
	if a2a.CallAgentMethod != "" {
		out["call_agent_method"] = a2a.CallAgentMethod
	}
	if a2a.RuntimeTokenType != "" {
		out["runtime_token_type"] = a2a.RuntimeTokenType
	}
	if len(a2a.RuntimeScopes) > 0 {
		out["runtime_scopes"] = a2a.RuntimeScopes
	}
	return out
}

func jsonMapFromAny(value any) JSONMap {
	switch typed := value.(type) {
	case nil:
		return nil
	case JSONMap:
		return typed
	case map[string]any:
		return JSONMap(typed)
	case openlinker.JSON:
		return JSONMap(typed)
	default:
		return nil
	}
}

func sdkEventsFromRunEvents(events []RunEvent) []openlinker.AgentEvent {
	if len(events) == 0 {
		return nil
	}
	out := make([]openlinker.AgentEvent, 0, len(events))
	for _, event := range events {
		out = append(out, openlinker.AgentEvent{
			EventType: event.EventType,
			Payload:   event.Payload,
		})
	}
	return out
}

func sdkErrorFromAgentError(agentErr *AgentError) *openlinker.AgentError {
	if agentErr == nil {
		return nil
	}
	return &openlinker.AgentError{
		Code:    agentErr.Code,
		Message: agentErr.Message,
	}
}

func retryAfterFromSDKError(err *openlinker.Error, fallback time.Duration) time.Duration {
	if err != nil && err.RetryAfter > 0 {
		return err.RetryAfter
	}
	return fallback
}

func retryAfterFromClaimResult(result *openlinker.ClaimRuntimeRunResult, fallback time.Duration) time.Duration {
	if result != nil && result.RetryAfter > 0 {
		return result.RetryAfter
	}
	return fallback
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
