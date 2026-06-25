package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// BiDiClient is a WebDriver BiDi WebSocket client.
// It sends commands and receives responses/events from a browser debugging endpoint.
type BiDiClient struct {
	conn     *websocket.Conn
	mu       sync.Mutex // protects conn writes
	pending  map[int64]*bidiPending
	pmu      sync.Mutex // protects pending map
	nextID   atomic.Int64
	handlers map[string][]EventHandler
	hmu      sync.RWMutex // protects handlers
	closed   chan struct{}
}

type bidiPending struct {
	ch    chan *bidiResponse
	timer *time.Timer
}

type bidiResponse struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *bidiError      `json:"error,omitempty"`
}

type bidiError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// EventHandler is called when a subscribed BiDi event arrives.
type EventHandler func(method string, params json.RawMessage)

// Global BiDi client — nil if not connected.
var (
	bidiClient *BiDiClient
	bidiMu     sync.RWMutex
)

func getBiDi() *BiDiClient {
	bidiMu.RLock()
	defer bidiMu.RUnlock()
	return bidiClient
}

func setBiDi(c *BiDiClient) {
	bidiMu.Lock()
	defer bidiMu.Unlock()
	bidiClient = c
}

func requireBiDi() (*BiDiClient, error) {
	c := getBiDi()
	if c == nil {
		return nil, errors.New("BiDi not connected. Launch browser with --remote-debugging-port or call launch_browser.")
	}
	return c, nil
}

// ConnectBiDi establishes a BiDi WebSocket connection.
func ConnectBiDi(wsURL string) (*BiDiClient, error) {
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("BiDi dial failed: %w", err)
	}

	c := &BiDiClient{
		conn:     conn,
		pending:  make(map[int64]*bidiPending),
		handlers: make(map[string][]EventHandler),
		closed:   make(chan struct{}),
	}

	go c.readLoop()
	return c, nil
}

// Close shuts down the BiDi connection.
func (c *BiDiClient) Close() {
	select {
	case <-c.closed:
		return // already closed
	default:
	}
	close(c.closed)
	c.conn.Close()

	// Cancel all pending requests
	c.pmu.Lock()
	for id, p := range c.pending {
		p.timer.Stop()
		close(p.ch)
		delete(c.pending, id)
	}
	c.pmu.Unlock()
}

// Send sends a BiDi command and waits for the response.
// BiDi command format: {"id": N, "method": "...", "params": {...}}
// BiDi response format: {"id": N, "type": "success", "result": {...}}
//
//	or: {"id": N, "type": "error", "error": "...", "message": "..."}
func (c *BiDiClient) Send(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)

	msg := map[string]any{
		"id":     id,
		"method": method,
	}
	if params != nil {
		msg["params"] = params
	}

	ch := make(chan *bidiResponse, 1)
	timer := time.AfterFunc(30*time.Second, func() {
		c.pmu.Lock()
		if p, ok := c.pending[id]; ok {
			delete(c.pending, id)
			p.ch <- &bidiResponse{
				Error: &bidiError{
					Error:   "timeout",
					Message: fmt.Sprintf("timeout after 30s: %s", method),
				},
			}
		}
		c.pmu.Unlock()
	})

	c.pmu.Lock()
	c.pending[id] = &bidiPending{ch: ch, timer: timer}
	c.pmu.Unlock()

	data, _ := json.Marshal(msg)
	c.mu.Lock()
	err := c.conn.WriteMessage(websocket.TextMessage, data)
	c.mu.Unlock()
	if err != nil {
		timer.Stop()
		c.pmu.Lock()
		delete(c.pending, id)
		c.pmu.Unlock()
		return nil, fmt.Errorf("BiDi write error: %w", err)
	}

	select {
	case <-ctx.Done():
		timer.Stop()
		c.pmu.Lock()
		delete(c.pending, id)
		c.pmu.Unlock()
		return nil, ctx.Err()
	case resp, ok := <-ch:
		timer.Stop()
		if !ok {
			return nil, errors.New("BiDi connection closed")
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("BiDi error (%s): %s", resp.Error.Error, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

// Subscribe subscribes to BiDi events.
func (c *BiDiClient) Subscribe(ctx context.Context, events []string) error {
	_, err := c.Send(ctx, "session.subscribe", map[string]any{
		"events": events,
	})
	return err
}

// OnEvent registers a handler for a BiDi event method.
func (c *BiDiClient) OnEvent(method string, handler EventHandler) {
	c.hmu.Lock()
	defer c.hmu.Unlock()
	c.handlers[method] = append(c.handlers[method], handler)
}

// readLoop reads messages from the BiDi WebSocket and dispatches them.
func (c *BiDiClient) readLoop() {
	defer func() {
		select {
		case <-c.closed:
		default:
			close(c.closed)
		}
		c.conn.Close()
	}()

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				select {
				case <-c.closed:
				default:
					logger.Printf("BiDi read error: %v", err)
				}
			}
			return
		}

		// Parse the top-level envelope to decide how to dispatch
		var envelope struct {
			ID     *int64          `json:"id,omitempty"`
			Type   string          `json:"type"`
			Method string          `json:"method,omitempty"`
			Result json.RawMessage `json:"result,omitempty"`
			Error  string          `json:"error,omitempty"`
			Msg    string          `json:"message,omitempty"`
			Params json.RawMessage `json:"params,omitempty"`
		}
		if json.Unmarshal(message, &envelope) != nil {
			continue
		}

		if envelope.ID != nil {
			// Response to a command
			c.pmu.Lock()
			p, ok := c.pending[*envelope.ID]
			if ok {
				delete(c.pending, *envelope.ID)
			}
			c.pmu.Unlock()

			if ok {
				p.timer.Stop()
				resp := &bidiResponse{Result: envelope.Result}
				if envelope.Type == "error" {
					resp.Error = &bidiError{
						Error:   envelope.Error,
						Message: envelope.Msg,
					}
				}
				p.ch <- resp
			}
		} else if envelope.Type == "event" && envelope.Method != "" {
			// Event dispatch
			c.hmu.RLock()
			handlers := c.handlers[envelope.Method]
			c.hmu.RUnlock()
			for _, h := range handlers {
				go h(envelope.Method, envelope.Params)
			}
		}
	}
}

// DiscoverBiDiEndpoint finds the BiDi WebSocket URL from a browser's debugging port.
// Tries the standard /json/version endpoint first (Chrome), then falls back to
// direct WebSocket connection (Firefox BiDi).
func DiscoverBiDiEndpoint(port int) (string, error) {
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	// Try Chrome-style discovery: GET /json/version → webSocketDebuggerUrl
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(base + "/json/version")
	if err == nil {
		defer resp.Body.Close()
		var info struct {
			WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		}
		if json.NewDecoder(resp.Body).Decode(&info) == nil && info.WebSocketDebuggerURL != "" {
			return info.WebSocketDebuggerURL, nil
		}
	}

	// Fall back to direct WebSocket at /session (Firefox BiDi style)
	return fmt.Sprintf("ws://127.0.0.1:%d/session", port), nil
}

// activateBiDi installs a freshly-connected client as the global BiDi client,
// subscribes to the standard events, and watches for disconnect.
func activateBiDi(client *BiDiClient) {
	setBiDi(client)

	ctx := context.Background()
	client.Subscribe(ctx, []string{
		"log.entryAdded",
		"browsingContext.contextCreated",
		"browsingContext.contextDestroyed",
	})

	go func() {
		<-client.closed
		logger.Printf("BiDi connection lost")
		setBiDi(nil)
	}()
}

// tryConnectBiDi makes a single discover+connect attempt on the given port,
// activating the client on success and returning the ws URL it connected to.
func tryConnectBiDi(port int) (string, error) {
	wsURL, err := DiscoverBiDiEndpoint(port)
	if err != nil {
		return "", err
	}
	client, err := ConnectBiDi(wsURL)
	if err != nil {
		return "", err
	}
	// Firefox/Zen BiDi-only: connecting to the /session WebSocket gives a live
	// socket but NO WebDriver session — context commands then fail with "invalid
	// session id". Chrome's CDP-derived endpoint already carries a session, so
	// this handshake only applies to the Firefox fallback URL.
	if strings.HasSuffix(wsURL, "/session") {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := ensureBiDiSession(sctx, client); err != nil {
			client.Close()
			return "", err
		}
	}
	activateBiDi(client)
	return wsURL, nil
}

// ensureBiDiSession makes a Firefox/Zen BiDi connection usable. Firefox allows
// only ONE active WebDriver session and a session orphaned by a since-closed
// connection lingers — so blindly calling session.new fails with "Maximum
// number of active sessions". Instead: probe with getTree (reuse a live
// session), create one only when absent, and if creation is blocked by a stale
// session, end it and retry once.
func ensureBiDiSession(ctx context.Context, client *BiDiClient) error {
	if _, err := client.Send(ctx, "browsingContext.getTree", map[string]any{}); err == nil {
		return nil // a usable session already exists — reuse it
	}
	_, err := client.Send(ctx, "session.new", map[string]any{"capabilities": map[string]any{}})
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "Maximum number of active sessions") {
		// A stale session blocks creation; clear it and retry once.
		_, _ = client.Send(ctx, "session.end", map[string]any{})
		if _, e2 := client.Send(ctx, "session.new", map[string]any{"capabilities": map[string]any{}}); e2 != nil {
			return fmt.Errorf("a stale BiDi session is blocking connection and could not be reset (%w) — restart the browser to clear it", e2)
		}
		return nil
	}
	return fmt.Errorf("session.new failed: %w", err)
}

// connectBiDiWithRetry discovers the endpoint and connects, retrying on failure.
func connectBiDiWithRetry(port int) {
	backoff := 500 * time.Millisecond
	maxBackoff := 5 * time.Second

	for i := 0; i < 20; i++ {
		time.Sleep(backoff)

		wsURL, err := tryConnectBiDi(port)
		if err != nil {
			logger.Printf("BiDi connect failed: %v (retry %d)", err, i+1)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		logger.Printf("BiDi connected to %s", wsURL)
		return
	}
	logger.Printf("BiDi: gave up after 20 retries on port %d", port)
}
