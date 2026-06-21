package agentnode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type RuntimeWSConnector struct {
	APIBase      string
	RuntimeToken string
	Reconnect    bool
	ReconnectMin time.Duration
	ReconnectMax time.Duration
	Dialer       *websocket.Dialer

	mu       sync.Mutex
	conn     *websocket.Conn
	handlers ConnectorHandlers
	ctx      context.Context
	cancel   context.CancelFunc
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
	if c.ReconnectMin <= 0 {
		c.ReconnectMin = 500 * time.Millisecond
	}
	if c.ReconnectMax <= 0 {
		c.ReconnectMax = 10 * time.Second
	}
	if c.Dialer == nil {
		c.Dialer = websocket.DefaultDialer
	}
	c.handlers = handlers
	c.ctx, c.cancel = context.WithCancel(ctx)
	if err := c.connect(c.ctx); err != nil {
		return err
	}
	go c.readLoop()
	return nil
}

func (c *RuntimeWSConnector) Stop(ctx context.Context) error {
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn != nil {
		_ = conn.WriteControl(websocket.CloseMessage, []byte{}, time.Now().Add(time.Second))
		return conn.Close()
	}
	return nil
}

func (c *RuntimeWSConnector) connect(ctx context.Context) error {
	wsURL, err := websocketURL(c.APIBase, "/api/v1/agent-runtime/ws")
	if err != nil {
		return err
	}
	header := http.Header{}
	header.Set("authorization", "Bearer "+c.RuntimeToken)
	conn, _, err := c.Dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	return nil
}

func (c *RuntimeWSConnector) readLoop() {
	for {
		conn := c.currentConn()
		if conn == nil {
			return
		}
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				if c.ctx.Err() == nil && c.handlers.OnError != nil {
					c.handlers.OnError(err)
				}
				break
			}
			c.handleMessage(data)
		}
		if c.ctx.Err() != nil || !c.Reconnect {
			return
		}
		delay := c.ReconnectMin
		for c.ctx.Err() == nil {
			timer := time.NewTimer(delay)
			select {
			case <-c.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if err := c.connect(c.ctx); err != nil {
				if c.handlers.OnError != nil {
					c.handlers.OnError(err)
				}
				delay *= 2
				if delay > c.ReconnectMax {
					delay = c.ReconnectMax
				}
				continue
			}
			break
		}
	}
}

func (c *RuntimeWSConnector) handleMessage(data []byte) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		if c.handlers.OnError != nil {
			c.handlers.OnError(err)
		}
		return
	}
	switch envelope.Type {
	case "runtime.ready":
		var message JSONMap
		if err := json.Unmarshal(data, &message); err == nil && c.handlers.OnReady != nil {
			c.handlers.OnReady(message)
		}
	case "run.assigned":
		var assignment Assignment
		if err := json.Unmarshal(data, &assignment); err != nil {
			if c.handlers.OnError != nil {
				c.handlers.OnError(err)
			}
			return
		}
		if c.handlers.OnAssigned != nil {
			c.handlers.OnAssigned(assignment)
		}
	case "error":
		var message struct {
			Error any `json:"error"`
		}
		_ = json.Unmarshal(data, &message)
		if c.handlers.OnError != nil {
			c.handlers.OnError(fmt.Errorf("runtime websocket error: %v", message.Error))
		}
	default:
	}
}

func (c *RuntimeWSConnector) SendRunEvent(ctx context.Context, runID string, event RunEvent) error {
	return c.send(ctx, JSONMap{
		"type":       "run.event",
		"id":         fmt.Sprintf("event-%s-%d", runID, time.Now().UnixMilli()),
		"run_id":     runID,
		"event_type": event.EventType,
		"payload":    event.Payload,
	})
}

func (c *RuntimeWSConnector) CompleteRun(ctx context.Context, runID string, result RunResult) error {
	return c.send(ctx, JSONMap{
		"type":        "run.result",
		"id":          fmt.Sprintf("result-%s-%d", runID, time.Now().UnixMilli()),
		"run_id":      runID,
		"status":      result.Status,
		"output":      result.Output,
		"events":      result.Events,
		"error":       result.Error,
		"duration_ms": result.DurationMS,
	})
}

func (c *RuntimeWSConnector) send(ctx context.Context, message any) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return fmt.Errorf("runtime websocket is not open")
	}
	done := make(chan error, 1)
	go func(conn *websocket.Conn) {
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		done <- conn.WriteMessage(websocket.TextMessage, encoded)
	}(c.conn)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func (c *RuntimeWSConnector) currentConn() *websocket.Conn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn
}
