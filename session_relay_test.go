package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mark3labs/mcp-go/mcp"
)

// TestRelay_IntroduceReachesBrowser drives the whole identity path the way a
// real relay MCP instance does: connect + register, learn clientInfo from
// initialize, then the agent calls `introduce`. The browser must see each
// update in its mcp_clients push without the relay reconnecting, and
// relayed commands must carry the short agent name.
func TestRelay_IntroduceReachesBrowser(t *testing.T) {
	resetSession(t)
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()
	relayClientsMu.Lock()
	relayClients = make(map[*websocket.Conn]*relayClientInfo)
	relayClientsMu.Unlock()

	srv, wsURL := startTestDaemon(t)
	defer srv.Close()

	// Mock browser that records mcp_clients pushes and answers commands.
	var mu sync.Mutex
	var lastClients []map[string]any
	var lastCmdParams map[string]any
	bconn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bconn.Close()
	go func() {
		for {
			_, m, err := bconn.ReadMessage()
			if err != nil {
				return
			}
			var msg map[string]any
			if json.Unmarshal(m, &msg) != nil {
				continue
			}
			if msg["type"] == "mcp_clients" {
				raw, _ := json.Marshal(msg["clients"])
				var cs []map[string]any
				_ = json.Unmarshal(raw, &cs)
				mu.Lock()
				lastClients = cs
				mu.Unlock()
				continue
			}
			if id, ok := msg["id"]; ok {
				mu.Lock()
				lastCmdParams, _ = msg["params"].(map[string]any)
				mu.Unlock()
				resp, _ := json.Marshal(map[string]any{"id": id, "result": "ok"})
				_ = bconn.WriteMessage(websocket.TextMessage, resp)
			}
		}
	}()

	relayWS, _, err := websocket.DefaultDialer.Dial(wsURL+"/relay", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relayWS.Close()
	oldRelay, oldConn := useRelay, relayConn
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
	go func() { // drain relay responses into pending
		for {
			_, m, err := relayWS.ReadMessage()
			if err != nil {
				return
			}
			var msg struct {
				ID     string          `json:"id"`
				Result json.RawMessage `json:"result"`
			}
			if json.Unmarshal(m, &msg) != nil || msg.ID == "" {
				continue
			}
			pendingMu.Lock()
			p, ok := pending[msg.ID]
			delete(pending, msg.ID)
			pendingMu.Unlock()
			if ok {
				p.timer.Stop()
				p.resultCh <- msg.Result
			}
		}
	}()

	waitClient := func(check func(c map[string]any) bool) map[string]any {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			cs := lastClients
			mu.Unlock()
			if len(cs) == 1 && check(cs[0]) {
				return cs[0]
			}
			time.Sleep(20 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("browser never saw expected client; last push: %v", lastClients)
		return nil
	}

	sessionMu.Lock()
	sess.project = "proj"
	sessionMu.Unlock()

	// 1. initialize handshake: jcode identifies itself.
	refineSessionFromInitialize("jcode", "", "0.89.0")
	publishSession()
	waitClient(func(c map[string]any) bool {
		return c["label"] == "jcode · proj" && c["sessionType"] == "jcode"
	})

	// 2. agent introduces itself via the real tool handler.
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"name": "GPT-5", "model": "gpt-5", "task": "Checking the popup",
	}
	res, err := handleIntroduce(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("introduce: %v %+v", err, res)
	}
	c := waitClient(func(c map[string]any) bool { return c["displayName"] == "GPT-5" })
	if c["label"] != "GPT-5 · proj" || c["hostName"] != "jcode" ||
		c["model"] != "gpt-5" || c["task"] != "Checking the popup" {
		t.Fatalf("client after introduce = %v", c)
	}

	// 3. relayed commands are attributed with the short name.
	if _, err := send("probe", map[string]any{}, 3000); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	p := lastCmdParams
	mu.Unlock()
	if p["_clientName"] != "GPT-5" || p["_clientType"] != "jcode" || p["_clientLabel"] != "GPT-5 · proj" {
		t.Fatalf("command params = %v", p)
	}
}
