// Package codexrpc implements the bounded stdio transport of a single app-server
// process. Process ownership and Attempt/session policy stay with agentexec.
package codexrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// MaxOutputBytes bounds an individual protocol message, not process lifetime.
const MaxOutputBytes = 4 << 20
const maxQueuedMessages = 128

// Error deliberately excludes the provider's diagnostic text from Error().
// Message/Data are untrusted protocol data, never a tool authorization.
type Error struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("Codex RPC rejected the request (code %d)", e.Code)
}

type Message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

type Client struct {
	mu      sync.Mutex
	next    uint64
	pending map[string]chan Message
	err     error
	done    chan struct{}
	events  chan Message
	writes  chan Message
	stop    func()
	once    sync.Once
	wg      sync.WaitGroup
}

// New starts exactly two bounded I/O pumps. stop must terminate the process and
// close its pipes, including a writer blocked on an unresponsive child.
func New(in io.Writer, out io.Reader, stop func()) *Client {
	c := &Client{pending: map[string]chan Message{}, done: make(chan struct{}), events: make(chan Message, maxQueuedMessages), writes: make(chan Message, maxQueuedMessages), stop: stop}
	c.wg.Add(2)
	go func() { defer c.wg.Done(); c.read(out) }()
	go func() { defer c.wg.Done(); c.write(in) }()
	return c
}
func (c *Client) Events() <-chan Message { return c.events }
func (c *Client) Done() <-chan struct{}  { return c.done }
func (c *Client) Err() error             { c.mu.Lock(); defer c.mu.Unlock(); return c.err }
func (c *Client) fail(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
		c.stop()
	})
}
func (c *Client) Close() { c.fail(io.EOF); c.wg.Wait() }
func (c *Client) send(ctx context.Context, message Message) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.Err()
	case c.writes <- message:
		return nil
	}
}
func (c *Client) Notify(ctx context.Context, method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.send(ctx, Message{Method: method, Params: raw})
}
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if len(raw) > MaxOutputBytes {
		return errors.New("Codex RPC request exceeds size limit")
	}
	c.mu.Lock()
	if len(c.pending) >= maxQueuedMessages {
		c.mu.Unlock()
		return errors.New("Codex RPC pending request limit exceeded")
	}
	c.next++
	id := strconv.FormatUint(c.next, 10)
	response := make(chan Message, 1)
	c.pending[id] = response
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err = c.send(ctx, Message{ID: json.RawMessage(id), Method: method, Params: raw}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.Err()
	case message := <-response:
		if message.Error != nil {
			return message.Error
		}
		if result == nil {
			return nil
		}
		if len(message.Result) == 0 {
			return errors.New("Codex RPC response is missing result")
		}
		if err := json.Unmarshal(message.Result, result); err != nil {
			return errors.New("Codex RPC response has an invalid shape")
		}
		return nil
	}
}
func (c *Client) write(in io.Writer) {
	encoder := json.NewEncoder(in)
	for {
		select {
		case <-c.done:
			return
		case message := <-c.writes:
			if err := encoder.Encode(message); err != nil {
				c.fail(errors.New("Codex RPC write failed"))
				return
			}
		}
	}
}
func (c *Client) read(out io.Reader) {
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), MaxOutputBytes+1)
	for scanner.Scan() {
		if len(scanner.Bytes()) > MaxOutputBytes {
			c.fail(errors.New("Codex RPC output exceeds size limit"))
			return
		}
		var message Message
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			c.fail(errors.New("Codex RPC emitted malformed JSON"))
			return
		}
		if message.Method != "" {
			if len(message.ID) > 0 {
				// Worker has no interactive user. Never grant a permission, invoke an
				// arbitrary callback, or allow an unanswered request to stall the turn.
				reply := denyRequest(message)
				select {
				case c.writes <- reply:
				case <-c.done:
					return
				default:
					c.fail(errors.New("Codex RPC reply queue exceeded"))
					return
				}
			} else {
				select {
				case c.events <- message:
				case <-c.done:
					return
				default:
					// Do not block response routing while the consumer waits in Call.
					c.fail(errors.New("Codex RPC notification queue exceeded"))
					return
				}
			}
			continue
		}
		if len(message.ID) == 0 || (message.Error == nil && len(message.Result) == 0) {
			c.fail(errors.New("Codex RPC emitted an invalid response"))
			return
		}
		c.mu.Lock()
		pending := c.pending[string(message.ID)]
		c.mu.Unlock()
		// Late responses to canceled requests are harmless. They cannot be consumed
		// by another call: request IDs are monotonic for the process lifetime.
		if pending != nil {
			select {
			case pending <- message:
			default:
				c.fail(errors.New("Codex RPC emitted a duplicate response"))
				return
			}
		}
	}
	if scanner.Err() != nil {
		c.fail(errors.New("Codex RPC stream read failed"))
	} else {
		c.fail(io.EOF)
	}
}
func denyRequest(request Message) Message {
	reply := Message{ID: request.ID}
	switch request.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		reply.Result = json.RawMessage(`{"decision":"decline"}`)
	case "mcpServer/elicitation/request":
		reply.Result = json.RawMessage(`{"action":"decline","content":null}`)
	case "item/permissions/requestApproval":
		reply.Result = json.RawMessage(`{"permissions":{},"scope":"turn"}`)
	case "item/tool/requestUserInput":
		reply.Result = json.RawMessage(`{"answers":{}}`)
	default:
		reply.Error = &Error{Code: -32601, Message: "Interactive requests and dynamic tools are unavailable in this Worker"}
	}
	return reply
}
