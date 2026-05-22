package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mark3labs/mcp-go/mcp"
)

// startTestWSServer creates a WebSocket test server using the real handleWSConnection.
func startTestWSServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleWSConnection)
	srv := httptest.NewServer(mux)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	return srv, wsURL
}

// connectMockBrowser connects a mock browser extension that auto-responds to commands.
func connectMockBrowser(t *testing.T, wsURL string, handler func(msg map[string]any) map[string]any) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	go func() {
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg map[string]any
			if json.Unmarshal(message, &msg) != nil {
				continue
			}
			// Skip server-initiated push messages (e.g. mcp_clients) — those
			// are notifications, not commands awaiting a response.
			if _, isPush := msg["type"]; isPush {
				continue
			}
			if _, hasID := msg["id"]; !hasID {
				continue
			}
			resp := handler(msg)
			resp["id"] = msg["id"]
			respBytes, _ := json.Marshal(resp)
			conn.WriteMessage(websocket.TextMessage, respBytes)
		}
	}()

	// Wait for connection to be registered
	time.Sleep(50 * time.Millisecond)
	return conn
}

func TestWSConnection_Connect(t *testing.T) {
	// Clear global state
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	srv, wsURL := startTestWSServer(t)
	defer srv.Close()

	conn := connectMockBrowser(t, wsURL, func(msg map[string]any) map[string]any {
		return map[string]any{"result": "ok"}
	})
	defer conn.Close()

	open := getOpenBrowsers()
	if len(open) != 1 {
		t.Errorf("expected 1 browser, got %d", len(open))
	}
}

func TestWSConnection_Disconnect(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	srv, wsURL := startTestWSServer(t)
	defer srv.Close()

	conn := connectMockBrowser(t, wsURL, func(msg map[string]any) map[string]any {
		return map[string]any{"result": "ok"}
	})

	if len(getOpenBrowsers()) != 1 {
		t.Fatal("expected 1 browser before disconnect")
	}

	conn.Close()
	time.Sleep(100 * time.Millisecond)

	if len(getOpenBrowsers()) != 0 {
		t.Error("expected 0 browsers after disconnect")
	}
}

func TestWSConnection_MultipleBrowsers(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	srv, wsURL := startTestWSServer(t)
	defer srv.Close()

	handler := func(msg map[string]any) map[string]any {
		return map[string]any{"result": "ok"}
	}

	conn1 := connectMockBrowser(t, wsURL, handler)
	defer conn1.Close()
	conn2 := connectMockBrowser(t, wsURL, handler)
	defer conn2.Close()

	open := getOpenBrowsers()
	if len(open) != 2 {
		t.Errorf("expected 2 browsers, got %d", len(open))
	}
}

func TestSendTo_Success(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	srv, wsURL := startTestWSServer(t)
	defer srv.Close()

	conn := connectMockBrowser(t, wsURL, func(msg map[string]any) map[string]any {
		action := msg["action"].(string)
		if action == "test_action" {
			return map[string]any{"result": map[string]any{"status": "success"}}
		}
		return map[string]any{"error": "unknown action"}
	})
	defer conn.Close()

	open := getOpenBrowsers()
	if len(open) != 1 {
		t.Fatal("no browsers connected")
	}

	raw, err := sendTo(open[0], "test_action", map[string]any{"key": "value"}, 5000)
	if err != nil {
		t.Fatalf("sendTo error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result["status"] != "success" {
		t.Errorf("result = %v, want status=success", result)
	}
}

func TestSendTo_Error(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	srv, wsURL := startTestWSServer(t)
	defer srv.Close()

	conn := connectMockBrowser(t, wsURL, func(msg map[string]any) map[string]any {
		return map[string]any{"error": "tab not found"}
	})
	defer conn.Close()

	open := getOpenBrowsers()
	_, err := sendTo(open[0], "navigate", map[string]any{"tabId": 999}, 5000)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "tab not found") {
		t.Errorf("error = %q, want to contain 'tab not found'", err.Error())
	}
}

func TestSendTo_Timeout(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	srv, wsURL := startTestWSServer(t)
	defer srv.Close()

	// Mock browser that never responds
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Just consume messages but don't reply
	go func() {
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)

	open := getOpenBrowsers()
	if len(open) == 0 {
		t.Fatal("no browsers")
	}

	_, err = sendTo(open[0], "slow_action", nil, 100) // 100ms timeout
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error = %q, want timeout", err.Error())
	}
}

func TestSend_NoBrowsers(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	_, err := send("test", nil)
	if err == nil {
		t.Fatal("expected error with no browsers")
	}
	if !strings.Contains(err.Error(), "No browser extension connected") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestSend_ListTabs_MergesMultipleBrowsers(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	srv, wsURL := startTestWSServer(t)
	defer srv.Close()

	// Browser 1: returns 2 tabs
	conn1 := connectMockBrowser(t, wsURL, func(msg map[string]any) map[string]any {
		action := msg["action"].(string)
		if action == "list_tabs" {
			return map[string]any{"result": []map[string]any{
				{"id": 1, "title": "Tab 1"},
				{"id": 2, "title": "Tab 2"},
			}}
		}
		if action == "execute_js" {
			return map[string]any{"result": map[string]any{"result": "Mozilla/5.0 Chrome/120"}}
		}
		return map[string]any{"result": nil}
	})
	defer conn1.Close()

	// Browser 2: returns 1 tab
	conn2 := connectMockBrowser(t, wsURL, func(msg map[string]any) map[string]any {
		action := msg["action"].(string)
		if action == "list_tabs" {
			return map[string]any{"result": []map[string]any{
				{"id": 100, "title": "Arc Tab"},
			}}
		}
		if action == "execute_js" {
			return map[string]any{"result": map[string]any{"result": "Mozilla/5.0 Arc/1.0"}}
		}
		return map[string]any{"result": nil}
	})
	defer conn2.Close()

	raw, err := send("list_tabs", nil, 5000)
	if err != nil {
		t.Fatalf("send list_tabs error: %v", err)
	}

	var tabs []map[string]any
	if err := json.Unmarshal(raw, &tabs); err != nil {
		t.Fatalf("unmarshal tabs: %v", err)
	}

	if len(tabs) != 3 {
		t.Errorf("expected 3 merged tabs, got %d: %s", len(tabs), string(raw))
	}
}

func TestSend_RoutesToCorrectBrowser(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	srv, wsURL := startTestWSServer(t)
	defer srv.Close()

	var browser1Calls, browser2Calls int
	var mu sync.Mutex

	// Browser 1: only handles tab 1
	conn1 := connectMockBrowser(t, wsURL, func(msg map[string]any) map[string]any {
		mu.Lock()
		browser1Calls++
		mu.Unlock()
		params, _ := msg["params"].(map[string]any)
		tabID := 0
		if tid, ok := params["tabId"]; ok {
			tabID = int(tid.(float64))
		}
		if tabID == 1 {
			return map[string]any{"result": "from browser 1"}
		}
		return map[string]any{"error": "No tab"}
	})
	defer conn1.Close()

	// Browser 2: only handles tab 100
	conn2 := connectMockBrowser(t, wsURL, func(msg map[string]any) map[string]any {
		mu.Lock()
		browser2Calls++
		mu.Unlock()
		params, _ := msg["params"].(map[string]any)
		tabID := 0
		if tid, ok := params["tabId"]; ok {
			tabID = int(tid.(float64))
		}
		if tabID == 100 {
			return map[string]any{"result": "from browser 2"}
		}
		return map[string]any{"error": "No tab"}
	})
	defer conn2.Close()

	// First call discovers which browser owns tab 100
	raw, err := send("click", map[string]any{"tabId": float64(100)}, 5000)
	if err != nil {
		t.Fatalf("send error: %v", err)
	}
	var result string
	json.Unmarshal(raw, &result)
	if result != "from browser 2" {
		t.Errorf("expected from browser 2, got %q", result)
	}

	// Second call should use cached routing (browser 2 directly)
	mu.Lock()
	browser1Calls = 0
	browser2Calls = 0
	mu.Unlock()

	raw, err = send("click", map[string]any{"tabId": float64(100)}, 5000)
	if err != nil {
		t.Fatalf("send cached error: %v", err)
	}
	json.Unmarshal(raw, &result)
	if result != "from browser 2" {
		t.Errorf("cached: expected from browser 2, got %q", result)
	}
}

func TestSend_TabNotFoundInAnyBrowser(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	srv, wsURL := startTestWSServer(t)
	defer srv.Close()

	conn := connectMockBrowser(t, wsURL, func(msg map[string]any) map[string]any {
		return map[string]any{"error": "No tab with ID 999"}
	})
	defer conn.Close()

	_, err := send("click", map[string]any{"tabId": float64(999)}, 5000)
	if err == nil {
		t.Fatal("expected error for nonexistent tab")
	}
	if !strings.Contains(err.Error(), "Tab 999 not found") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestSend_DefaultTimeout(t *testing.T) {
	// Verify default timeout is 30000ms
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	srv, wsURL := startTestWSServer(t)
	defer srv.Close()

	conn := connectMockBrowser(t, wsURL, func(msg map[string]any) map[string]any {
		return map[string]any{"result": "ok"}
	})
	defer conn.Close()

	// This tests that send() works without explicit timeout
	raw, err := send("test", nil)
	if err != nil {
		t.Fatalf("send error: %v", err)
	}
	var result string
	json.Unmarshal(raw, &result)
	if result != "ok" {
		t.Errorf("result = %q", result)
	}
}

func TestSend_ConcurrentRequests(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	srv, wsURL := startTestWSServer(t)
	defer srv.Close()

	conn := connectMockBrowser(t, wsURL, func(msg map[string]any) map[string]any {
		action := msg["action"].(string)
		return map[string]any{"result": fmt.Sprintf("echo:%s", action)}
	})
	defer conn.Close()

	// Fire 10 concurrent requests
	var wg sync.WaitGroup
	errors := make([]error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			action := fmt.Sprintf("action_%d", idx)
			raw, err := send(action, nil, 5000)
			if err != nil {
				errors[idx] = err
				return
			}
			var result string
			json.Unmarshal(raw, &result)
			expected := fmt.Sprintf("echo:%s", action)
			if result != expected {
				errors[idx] = fmt.Errorf("got %q, want %q", result, expected)
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errors {
		if err != nil {
			t.Errorf("request %d: %v", i, err)
		}
	}
}

// TestSend_SharedParamMapNoRace guards the sendDirect param-clone: a caller
// may pass ONE map[string]any across several concurrent send() calls (the
// describe fan-out does exactly this). sendDirect must clone params before
// writing _clientLabel/_clientHue into it — otherwise concurrent writes to
// the shared map are a data race. This test fires send() from many
// goroutines with the SAME map pointer, so `go test -race` fires if the
// clone in sendDirect is ever removed.
func TestSend_SharedParamMapNoRace(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	srv, wsURL := startTestWSServer(t)
	defer srv.Close()

	conn := connectMockBrowser(t, wsURL, func(msg map[string]any) map[string]any {
		return map[string]any{"result": "ok"}
	})
	defer conn.Close()

	if len(getOpenBrowsers()) == 0 {
		t.Fatal("no browsers connected")
	}

	// One shared map, passed by pointer to every concurrent send().
	shared := map[string]any{"key": "value"}

	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := send("test_action", shared, 5000)
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("request %d: %v", i, err)
		}
	}
	// The shared map must be untouched — sendDirect mutates only its clone.
	if _, ok := shared["_clientHue"]; ok {
		t.Error("send() wrote _clientHue into the caller's shared map — clone missing")
	}
	if _, ok := shared["_clientLabel"]; ok {
		t.Error("send() wrote _clientLabel into the caller's shared map — clone missing")
	}
}

// --- Relay tests ---

// startTestDaemon creates a test server that serves both browser and relay endpoints.
func startTestDaemon(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleWSConnection)
	mux.HandleFunc("/relay", handleRelayConnection)
	srv := httptest.NewServer(mux)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	return srv, wsURL
}

func TestRelay_SendViaRelay(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	srv, wsURL := startTestDaemon(t)
	defer srv.Close()

	// Connect a mock browser to the daemon
	conn := connectMockBrowser(t, wsURL, func(msg map[string]any) map[string]any {
		action := msg["action"].(string)
		return map[string]any{"result": fmt.Sprintf("browser-saw:%s", action)}
	})
	defer conn.Close()

	// Connect a relay client (simulating an MCP instance)
	relayWS, _, err := websocket.DefaultDialer.Dial(wsURL+"/relay", nil)
	if err != nil {
		t.Fatalf("relay dial: %v", err)
	}
	defer relayWS.Close()

	// Save and restore global relay state
	oldRelay := useRelay
	oldConn := relayConn
	defer func() {
		useRelay = oldRelay
		relayMu.Lock()
		relayConn = oldConn
		relayMu.Unlock()
	}()

	useRelay = true
	relayMu.Lock()
	relayConn = relayWS
	relayMu.Unlock()

	// Start reading relay responses in background
	go func() {
		for {
			_, message, err := relayWS.ReadMessage()
			if err != nil {
				return
			}
			var msg struct {
				ID     string          `json:"id"`
				Result json.RawMessage `json:"result,omitempty"`
				Error  string          `json:"error,omitempty"`
			}
			if json.Unmarshal(message, &msg) != nil || msg.ID == "" {
				continue
			}
			pendingMu.Lock()
			p, ok := pending[msg.ID]
			if ok {
				delete(pending, msg.ID)
			}
			pendingMu.Unlock()
			if ok {
				p.timer.Stop()
				if msg.Error != "" {
					p.errCh <- fmt.Errorf("%s", msg.Error)
				} else {
					p.resultCh <- msg.Result
				}
			}
		}
	}()

	// send() should route through relay to the daemon, which routes to browser
	raw, err := send("my_action", map[string]any{"key": "val"}, 5000)
	if err != nil {
		t.Fatalf("send via relay: %v", err)
	}

	var result string
	json.Unmarshal(raw, &result)
	if result != "browser-saw:my_action" {
		t.Errorf("got %q, want browser-saw:my_action", result)
	}
}

func TestRelay_NoBrowsersViaRelay(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	srv, wsURL := startTestDaemon(t)
	defer srv.Close()

	// Connect relay client but NO browsers
	relayWS, _, err := websocket.DefaultDialer.Dial(wsURL+"/relay", nil)
	if err != nil {
		t.Fatalf("relay dial: %v", err)
	}
	defer relayWS.Close()

	oldRelay := useRelay
	oldConn := relayConn
	defer func() {
		useRelay = oldRelay
		relayMu.Lock()
		relayConn = oldConn
		relayMu.Unlock()
	}()

	useRelay = true
	relayMu.Lock()
	relayConn = relayWS
	relayMu.Unlock()

	go func() {
		for {
			_, message, err := relayWS.ReadMessage()
			if err != nil {
				return
			}
			var msg struct {
				ID     string          `json:"id"`
				Result json.RawMessage `json:"result,omitempty"`
				Error  string          `json:"error,omitempty"`
			}
			if json.Unmarshal(message, &msg) != nil || msg.ID == "" {
				continue
			}
			pendingMu.Lock()
			p, ok := pending[msg.ID]
			if ok {
				delete(pending, msg.ID)
			}
			pendingMu.Unlock()
			if ok {
				p.timer.Stop()
				if msg.Error != "" {
					p.errCh <- fmt.Errorf("%s", msg.Error)
				} else {
					p.resultCh <- msg.Result
				}
			}
		}
	}()

	_, err = send("test", nil, 5000)
	if err == nil {
		t.Fatal("expected error with no browsers via relay")
	}
	if !strings.Contains(err.Error(), "No browser extension connected") {
		t.Errorf("error = %q", err.Error())
	}
}

// TestRelay_ConnectionStatusViaRelay guards the bug where connection_status
// reported the browser disconnected in relay mode: handleConnectionStatus
// inspected the relay client's own (empty) browsers map instead of asking the
// daemon. It must now route through send() so the daemon answers.
func TestRelay_ConnectionStatusViaRelay(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	srv, wsURL := startTestDaemon(t)
	defer srv.Close()

	// Browser connects to the daemon (as it does in production).
	conn := connectMockBrowser(t, wsURL, echoHandler)
	defer conn.Close()

	relayWS, _, err := websocket.DefaultDialer.Dial(wsURL+"/relay", nil)
	if err != nil {
		t.Fatalf("relay dial: %v", err)
	}
	defer relayWS.Close()

	oldRelay := useRelay
	oldConn := relayConn
	defer func() {
		useRelay = oldRelay
		relayMu.Lock()
		relayConn = oldConn
		relayMu.Unlock()
	}()

	useRelay = true
	relayMu.Lock()
	relayConn = relayWS
	relayMu.Unlock()

	go func() {
		for {
			_, message, err := relayWS.ReadMessage()
			if err != nil {
				return
			}
			var msg struct {
				ID     string          `json:"id"`
				Result json.RawMessage `json:"result,omitempty"`
				Error  string          `json:"error,omitempty"`
			}
			if json.Unmarshal(message, &msg) != nil || msg.ID == "" {
				continue
			}
			pendingMu.Lock()
			p, ok := pending[msg.ID]
			if ok {
				delete(pending, msg.ID)
			}
			pendingMu.Unlock()
			if ok {
				p.timer.Stop()
				if msg.Error != "" {
					p.errCh <- fmt.Errorf("%s", msg.Error)
				} else {
					p.resultCh <- msg.Result
				}
			}
		}
	}()

	result, err := handleConnectionStatus(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handleConnectionStatus via relay: %v", err)
	}
	text := extractText(t, result)
	var resp struct {
		Connected      bool `json:"connected"`
		Extension      bool `json:"extension"`
		ExtensionCount int  `json:"extensionCount"`
	}
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parse: %v\nraw: %s", err, text)
	}
	if !resp.Connected || !resp.Extension {
		t.Errorf("connection_status via relay = %s, want connected+extension true", text)
	}
	if resp.ExtensionCount != 1 {
		t.Errorf("extensionCount = %d, want 1", resp.ExtensionCount)
	}
}

// TestConnectionStatus_DaemonLinkDown verifies connection_status degrades
// gracefully: when the relay link to the daemon is down, it must return a
// structured connected:false verdict (with an error note), never an MCP error.
func TestConnectionStatus_DaemonLinkDown(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	oldRelay := useRelay
	oldConn := relayConn
	oldBiDi := getBiDi()
	defer func() {
		useRelay = oldRelay
		relayMu.Lock()
		relayConn = oldConn
		relayMu.Unlock()
		setBiDi(oldBiDi)
	}()

	useRelay = true
	relayMu.Lock()
	relayConn = nil // daemon link down
	relayMu.Unlock()
	setBiDi(nil)

	result, err := handleConnectionStatus(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handleConnectionStatus returned a Go error: %v", err)
	}
	if result.IsError {
		t.Error("connection_status must not return an MCP error when the daemon link is down")
	}
	text := extractText(t, result)
	var resp struct {
		Connected bool   `json:"connected"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parse: %v\nraw: %s", err, text)
	}
	if resp.Connected {
		t.Error("connected should be false when the daemon link is down")
	}
	if resp.Error == "" {
		t.Error("error field should explain why the status is unavailable")
	}
}

// TestConnectionStatus_BiDiMergeOverDownLink verifies the BiDi merge: a
// relay-process-owned BiDi session reports connected even when the daemon
// link is down and the daemon-sourced status would say not-connected.
func TestConnectionStatus_BiDiMergeOverDownLink(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	oldRelay := useRelay
	oldConn := relayConn
	oldBiDi := getBiDi()
	defer func() {
		useRelay = oldRelay
		relayMu.Lock()
		relayConn = oldConn
		relayMu.Unlock()
		setBiDi(oldBiDi)
	}()

	useRelay = true
	relayMu.Lock()
	relayConn = nil // daemon link down — daemon-sourced status is unavailable
	relayMu.Unlock()
	setBiDi(&BiDiClient{}) // but this relay process owns a BiDi session

	result, err := handleConnectionStatus(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handleConnectionStatus returned a Go error: %v", err)
	}
	text := extractText(t, result)
	var resp struct {
		Connected bool `json:"connected"`
		BiDi      bool `json:"bidi"`
	}
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parse: %v\nraw: %s", err, text)
	}
	if !resp.Connected {
		t.Error("connected should be true — a local BiDi session is active")
	}
	if !resp.BiDi {
		t.Error("bidi should be true — a local BiDi session is active")
	}
}

func TestRelay_ConcurrentRelaySends(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	srv, wsURL := startTestDaemon(t)
	defer srv.Close()

	conn := connectMockBrowser(t, wsURL, func(msg map[string]any) map[string]any {
		action := msg["action"].(string)
		return map[string]any{"result": fmt.Sprintf("echo:%s", action)}
	})
	defer conn.Close()

	relayWS, _, err := websocket.DefaultDialer.Dial(wsURL+"/relay", nil)
	if err != nil {
		t.Fatalf("relay dial: %v", err)
	}
	defer relayWS.Close()

	oldRelay := useRelay
	oldConn := relayConn
	defer func() {
		useRelay = oldRelay
		relayMu.Lock()
		relayConn = oldConn
		relayMu.Unlock()
	}()

	useRelay = true
	relayMu.Lock()
	relayConn = relayWS
	relayMu.Unlock()

	go func() {
		for {
			_, message, err := relayWS.ReadMessage()
			if err != nil {
				return
			}
			var msg struct {
				ID     string          `json:"id"`
				Result json.RawMessage `json:"result,omitempty"`
				Error  string          `json:"error,omitempty"`
			}
			if json.Unmarshal(message, &msg) != nil || msg.ID == "" {
				continue
			}
			pendingMu.Lock()
			p, ok := pending[msg.ID]
			if ok {
				delete(pending, msg.ID)
			}
			pendingMu.Unlock()
			if ok {
				p.timer.Stop()
				if msg.Error != "" {
					p.errCh <- fmt.Errorf("%s", msg.Error)
				} else {
					p.resultCh <- msg.Result
				}
			}
		}
	}()

	// Fire 10 concurrent requests via relay
	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			action := fmt.Sprintf("action_%d", idx)
			raw, err := send(action, nil, 5000)
			if err != nil {
				errs[idx] = err
				return
			}
			var result string
			json.Unmarshal(raw, &result)
			expected := fmt.Sprintf("echo:%s", action)
			if result != expected {
				errs[idx] = fmt.Errorf("got %q, want %q", result, expected)
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("request %d: %v", i, err)
		}
	}
}
