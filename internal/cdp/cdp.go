package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
)

// Target describes a single CDP debuggable page or worker target from /json/list.
type Target struct {
	ID                   string `json:"id"`
	Title                string `json:"title"`
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// AssertLoopback validates that a URL points strictly to local loopback without credentials.
func AssertLoopback(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.User != nil {
		return nil, errors.New("credentials in loopback URL are forbidden")
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "::1" && host != "localhost" {
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("non-loopback CDP host rejected: %s", host)
		}
	}
	return u, nil
}

// ListTargets fetches /json/list from a loopback CDP endpoint.
func ListTargets(ctx context.Context, port int) ([]Target, error) {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/json/list", port)
	if _, err := AssertLoopback(endpoint); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected CDP HTTP status: %d", resp.StatusCode)
	}
	var targets []Target
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return nil, err
	}
	return targets, nil
}

// Client is a lightweight pure-Go CDP WebSocket client.
type Client struct {
	ws      *websocket.Conn
	seq     atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan rpcResponse
	events  chan RPCEvent
	closed  chan struct{}
	once    sync.Once
}

type rpcResponse struct {
	Result json.RawMessage
	Err    error
}

// RPCEvent represents an asynchronous CDP notification event from Chromium.
type RPCEvent struct {
	Method string
	Params json.RawMessage
}

type wireMsg struct {
	ID     *int64          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Dial connects to a loopback CDP WebSocket debugger URL.
func Dial(ctx context.Context, wsURL string) (*Client, error) {
	u, err := AssertLoopback(wsURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "ws" {
		return nil, fmt.Errorf("unsupported CDP scheme: %s", u.Scheme)
	}
	cfg, err := websocket.NewConfig(wsURL, "http://127.0.0.1")
	if err != nil {
		return nil, err
	}
	cfg.Dialer = &net.Dialer{Timeout: 3 * time.Second}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		return nil, err
	}

	c := &Client{
		ws:      ws,
		pending: make(map[int64]chan rpcResponse),
		events:  make(chan RPCEvent, 256),
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	go func() {
		<-ctx.Done()
		_ = c.Close()
	}()
	return c, nil
}

func (c *Client) readLoop() {
	defer c.Close()
	for {
		var raw string
		if err := websocket.Message.Receive(c.ws, &raw); err != nil {
			return
		}
		var msg wireMsg
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			continue
		}
		if msg.ID != nil {
			c.mu.Lock()
			ch := c.pending[*msg.ID]
			delete(c.pending, *msg.ID)
			c.mu.Unlock()
			if ch != nil {
				if msg.Error != nil {
					ch <- rpcResponse{Err: fmt.Errorf("CDP %d: %s", msg.Error.Code, msg.Error.Message)}
				} else {
					ch <- rpcResponse{Result: msg.Result}
				}
			}
			continue
		}
		if msg.Method != "" {
			select {
			case c.events <- RPCEvent{Method: msg.Method, Params: msg.Params}:
			default:
			}
		}
	}
}

// Call sends a CDP command and waits for its reply or timeout.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.seq.Add(1)
	ch := make(chan rpcResponse, 1)

	c.mu.Lock()
	select {
	case <-c.closed:
		c.mu.Unlock()
		return nil, errors.New("cdp connection closed")
	default:
		c.pending[id] = ch
	}
	c.mu.Unlock()

	payload, err := json.Marshal(map[string]any{
		"id":     id,
		"method": method,
		"params": params,
	})
	if err != nil {
		c.removePending(id)
		return nil, err
	}
	if err := websocket.Message.Send(c.ws, string(payload)); err != nil {
		c.removePending(id)
		return nil, err
	}

	timer := time.NewTimer(6 * time.Second)
	defer timer.Stop()
	select {
	case resp := <-ch:
		return resp.Result, resp.Err
	case <-timer.C:
		c.removePending(id)
		return nil, fmt.Errorf("CDP call %s timed out", method)
	case <-ctx.Done():
		c.removePending(id)
		return nil, ctx.Err()
	case <-c.closed:
		return nil, errors.New("cdp connection closed")
	}
}

func (c *Client) removePending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// Events returns the asynchronous CDP event stream.
func (c *Client) Events() <-chan RPCEvent {
	return c.events
}

// Closed returns a channel that closes when the WebSocket session closes.
func (c *Client) Closed() <-chan struct{} {
	return c.closed
}

// Evaluate runs JavaScript in the optional isolated execution context and checks JS exceptions.
func (c *Client) Evaluate(ctx context.Context, expression string, contextID *int64) (json.RawMessage, error) {
	params := map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
	}
	if contextID != nil {
		params["contextId"] = *contextID
	}
	raw, err := c.Call(ctx, "Runtime.evaluate", params)
	if err != nil {
		return nil, err
	}
	var chk struct {
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if json.Unmarshal(raw, &chk) == nil && chk.ExceptionDetails != nil {
		msg := chk.ExceptionDetails.Text
		if chk.ExceptionDetails.Exception != nil && chk.ExceptionDetails.Exception.Description != "" {
			msg = chk.ExceptionDetails.Exception.Description
		}
		return raw, fmt.Errorf("CDP JS exception: %s", msg)
	}
	return raw, nil
}

// Close shuts down the WebSocket connection and rejects pending calls.
func (c *Client) Close() error {
	var err error
	c.once.Do(func() {
		close(c.closed)
		if c.ws != nil {
			err = c.ws.Close()
		}
		c.mu.Lock()
		for id, ch := range c.pending {
			ch <- rpcResponse{Err: errors.New("cdp connection closed")}
			delete(c.pending, id)
		}
		c.mu.Unlock()
	})
	return err
}
