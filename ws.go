package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const wsPort = 18321

// jsBrowserFingerprint is evaluated via execute_js on each connected tab to
// identify the browser brand.  Zen Browser is a Firefox fork whose
// navigator.userAgent is a stock Firefox string — "Zen" never appears in it —
// so we probe window.zen (a global Zen exposes in content contexts) and prefix
// "Zen/" onto the UA when found, giving the switch below a token to match on.
const jsBrowserFingerprint = `(function(){` +
	`var ua=navigator.userAgent;` +
	`try{if(typeof window.zen!=='undefined')return 'Zen/'+ua;}catch(e){}` +
	`return ua;` +
	`})()`

// BrowserConnection represents a connected Chrome/Arc/Brave/Edge extension.
type BrowserConnection struct {
	conn   *websocket.Conn
	name   string
	tabIDs map[int]bool // tabID -> isActive (true when that tab is the focused one)
	mu     sync.Mutex  // protects conn writes
}

type pendingRequest struct {
	resultCh chan json.RawMessage
	errCh    chan error
	timer    *time.Timer
}

var (
	browsers   = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu sync.RWMutex

	pending   = make(map[string]*pendingRequest)
	pendingMu sync.Mutex

	nextID atomic.Int64

	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			// Allow connections with no Origin header (e.g. Chrome extensions, CLI tools)
			if origin == "" {
				return true
			}
			// Allow localhost origins only
			for _, allowed := range []string{
				"http://127.0.0.1", "http://localhost",
				"https://127.0.0.1", "https://localhost",
				"chrome-extension://",
				"moz-extension://",
			} {
				if len(origin) >= len(allowed) && origin[:len(allowed)] == allowed {
					return true
				}
			}
			logger.Printf("WebSocket connection rejected: origin %q not allowed", origin)
			return false
		},
	}

	// Relay client state (used by MCP instances connecting to the daemon).
	relayConn *websocket.Conn
	relayMu   sync.Mutex // protects relayConn writes
	useRelay  bool       // set once at startup, not changed after

	// relayClients tracks connected MCP relay clients (daemon only).
	// Each entry carries the client's self-reported label/sessionType so the
	// daemon can attribute commands to a specific agent and surface them in
	// the extension popup.
	relayClients   = make(map[*websocket.Conn]*relayClientInfo)
	relayClientsMu sync.Mutex
)

// relayClientInfo tracks metadata for a connected MCP relay client. We
// only keep what the popup actually renders: label (display), sessionType
// (chip), pid (debug hint), connection time, and the daemon-assigned hue
// used to colour-code the agent on the popup, cursor, and toast when
// multiple agents drive the same browser.
type relayClientInfo struct {
	conn        *websocket.Conn
	label       string
	sessionType string
	pid         int
	connectedAt time.Time
	// hue is a 0–359 degree value assigned by computeClientHues based on
	// the agent's position in the connectedAt-sorted list. First agent
	// gets the brand orange hue (40°); each subsequent agent steps by
	// hueStep° (45°) and wraps modulo 360. Stable as long as the agent
	// doesn't reconnect — see computeClientHues for the assignment rule.
	hue int
}

// brandHue is the canonical orange hue of the project's mark (#d29922
// ≈ HSL(40°, 78%, 48%)). hueStep is the amount we rotate between
// consecutive agents — small enough that the family still feels orange-
// adjacent for the first few agents, large enough that two adjacent
// agents are visually distinguishable.
const (
	brandHue = 40
	hueStep  = 45
)

// computeClientHues assigns each relay client a stable hue based on its
// position in the connectedAt-sorted list. Idempotent; safe to call
// every time the client set changes. Must be called with relayClientsMu
// held by the caller.
func computeClientHues() {
	type entry struct {
		info *relayClientInfo
		at   time.Time
	}
	entries := make([]entry, 0, len(relayClients))
	for _, c := range relayClients {
		entries = append(entries, entry{info: c, at: c.connectedAt})
	}
	// Sort oldest first so the first connected agent keeps the brand
	// hue and newcomers rotate.
	sort.Slice(entries, func(i, j int) bool { return entries[i].at.Before(entries[j].at) })
	for i, e := range entries {
		e.info.hue = ((brandHue + i*hueStep) % 360 + 360) % 360
	}
}

// --- Daemon mode: runs the WS server as a standalone singleton ---

// RunDaemon starts the WebSocket server in daemon mode.
// It serves browser extension connections on / (WS), MCP relay
// connections on /relay (WS), and a /version probe (HTTP JSON) used by
// MCP instances to detect a stale daemon after a rebuild.
func RunDaemon() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/version", handleVersion)
	mux.HandleFunc("/", handleWSConnection)
	mux.HandleFunc("/relay", handleRelayConnection)

	addr := fmt.Sprintf("127.0.0.1:%d", wsPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("port %d already in use (daemon already running?): %w", wsPort, err)
	}

	logger.Printf("Daemon WebSocket server on ws://%s", addr)
	// Warm the release-check cache in the background so connection_status
	// can surface "update available" without a blocking GitHub call.
	startUpdateChecker()
	return http.Serve(ln, mux)
}

// handleRelayConnection handles a MCP instance connecting as a relay client.
func handleRelayConnection(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Printf("Relay upgrade error: %v", err)
		return
	}

	info := &relayClientInfo{conn: conn, connectedAt: time.Now()}
	relayClientsMu.Lock()
	relayClients[conn] = info
	count := len(relayClients)
	relayClientsMu.Unlock()
	logger.Printf("MCP relay client connected (%d client(s))", count)
	broadcastClientsToBrowsers()

	defer func() {
		relayClientsMu.Lock()
		delete(relayClients, conn)
		remaining := len(relayClients)
		relayClientsMu.Unlock()
		conn.Close()
		logger.Printf("MCP relay client disconnected (%d client(s) remaining)", remaining)
		broadcastClientsToBrowsers()
	}()

	// Use a mutex for this specific conn's writes since multiple goroutines respond.
	var writeMu sync.Mutex

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			break
		}

		// Try control message first (register).
		var ctrl struct {
			Type        string `json:"type"`
			Label       string `json:"label"`
			SessionType string `json:"sessionType"`
			PID         int    `json:"pid"`
		}
		if json.Unmarshal(message, &ctrl) == nil && ctrl.Type == "register" {
			relayClientsMu.Lock()
			info.label = ctrl.Label
			info.sessionType = ctrl.SessionType
			info.pid = ctrl.PID
			// Compute hue inline so the very first command after
			// register can be tagged correctly — the debounced
			// broadcastClientsToBrowsers below would otherwise leave
			// a 100ms window where commands have no hue assignment.
			computeClientHues()
			relayClientsMu.Unlock()
			logger.Printf("Relay client registered: label=%s type=%s pid=%d hue=%d", ctrl.Label, ctrl.SessionType, ctrl.PID, info.hue)
			broadcastClientsToBrowsers()
			continue
		}

		var req struct {
			ID      string         `json:"id"`
			Action  string         `json:"action"`
			Params  map[string]any `json:"params,omitempty"`
			Timeout int            `json:"timeout"`
		}
		if json.Unmarshal(message, &req) != nil || req.ID == "" {
			continue
		}

		// Attach client metadata so the extension can attribute the command.
		if req.Params == nil {
			req.Params = map[string]any{}
		}
		relayClientsMu.Lock()
		if info.label != "" {
			req.Params["_clientLabel"] = info.label
		}
		if info.sessionType != "" {
			req.Params["_clientType"] = info.sessionType
		}
		// _clientHue lets the extension tint the cursor + toast +
		// activity-row per agent so a human watching can distinguish
		// who's driving when multiple agents share a tab.
		req.Params["_clientHue"] = info.hue
		relayClientsMu.Unlock()

		go func(reqID, action string, params map[string]any, timeout int) {
			result, err := sendDirect(action, params, timeout)
			var resp []byte
			if err != nil {
				resp, _ = json.Marshal(map[string]any{"id": reqID, "error": err.Error()})
			} else {
				resp, _ = json.Marshal(map[string]any{"id": reqID, "result": result})
			}
			writeMu.Lock()
			conn.WriteMessage(websocket.TextMessage, resp)
			writeMu.Unlock()
		}(req.ID, req.Action, req.Params, req.Timeout)
	}
}

// broadcastExtensionReload sends a reload_extension message to every connected
// browser. The extension's background service worker calls chrome.runtime.reload()
// on receipt, picking up freshly extracted files from the extension directory.
func broadcastExtensionReload(version string) {
	msg, _ := json.Marshal(map[string]string{"type": "reload_extension", "version": version})
	browsersMu.RLock()
	conns := make([]*BrowserConnection, 0, len(browsers))
	for _, bc := range browsers {
		conns = append(conns, bc)
	}
	browsersMu.RUnlock()
	for _, bc := range conns {
		bc.mu.Lock()
		_ = bc.conn.WriteMessage(websocket.TextMessage, msg)
		bc.mu.Unlock()
	}
}

// broadcastDebounce coalesces a burst of broadcastClientsToBrowsers() calls
// into a single push. Fan-out across N relay clients × M browsers can storm
// when editors flap (CI spawning short-lived MCP processes, the user opening
// multiple project tabs at once); without debouncing we'd write the entire
// client list to every browser per event. 100ms is well under human
// perception and well over the duration of typical register/disconnect
// bursts.
var (
	broadcastTimerMu sync.Mutex
	broadcastTimer   *time.Timer
)

const broadcastDebounceWindow = 100 * time.Millisecond

// broadcastClientsToBrowsers schedules a push of the current MCP client set
// to every connected browser extension, debounced so a burst of calls
// collapses into one. The extension surfaces this in the popup so the user
// can see which agents are talking to them. Safe to call from any goroutine.
func broadcastClientsToBrowsers() {
	broadcastTimerMu.Lock()
	defer broadcastTimerMu.Unlock()
	if broadcastTimer != nil {
		broadcastTimer.Stop()
	}
	broadcastTimer = time.AfterFunc(broadcastDebounceWindow, doBroadcastClientsToBrowsers)
}

// doBroadcastClientsToBrowsers actually serialises the client list and
// pushes it to every browser. Called via broadcastTimer; never called
// directly from outside.
func doBroadcastClientsToBrowsers() {
	clients := []map[string]any{}

	relayClientsMu.Lock()
	// Recompute hues on every broadcast so the assignment is always
	// consistent with the current set (no orphaned hues, no gaps).
	computeClientHues()
	for _, c := range relayClients {
		label := c.label
		if label == "" {
			label = fmt.Sprintf("anon#%p", c.conn)
		}
		entry := map[string]any{
			"label":       label,
			"sessionType": c.sessionType,
			"connectedAt": c.connectedAt.UnixMilli(),
			"hue":         c.hue,
		}
		if c.pid != 0 {
			entry["pid"] = c.pid
		}
		clients = append(clients, entry)
	}
	relayClientsMu.Unlock()

	// In-process MCP mode: this process is itself the only client. The
	// MCP-side calls broadcastClientsToBrowsers() too, so include our own
	// session metadata for browsers.
	if len(clients) == 0 && getSessionLabel() != "" {
		clients = append(clients, snapshotSession())
	}

	msg, _ := json.Marshal(map[string]any{"type": "mcp_clients", "clients": clients, "serverVersion": serverVersion})

	browsersMu.RLock()
	conns := make([]*BrowserConnection, 0, len(browsers))
	for _, bc := range browsers {
		conns = append(conns, bc)
	}
	browsersMu.RUnlock()

	for _, bc := range conns {
		bc.mu.Lock()
		_ = bc.conn.WriteMessage(websocket.TextMessage, msg)
		bc.mu.Unlock()
	}
}

// --- MCP instance mode: connect to daemon as relay client ---

// startWebSocket connects to the daemon as a relay client.
// If the daemon isn't running, it spawns one first.
func startWebSocket() {
	if err := ensureDaemon(); err != nil {
		logger.Printf("Failed to ensure daemon: %v", err)
		// Fall back to running WS server in-process (legacy single-instance mode).
		startInProcess()
		return
	}

	useRelay = true
	go connectRelay()
}

// startInProcess runs the WS server in-process (fallback for when daemon can't start).
func startInProcess() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleWSConnection)
	addr := fmt.Sprintf("127.0.0.1:%d", wsPort)
	logger.Printf("WebSocket server on ws://%s (in-process)", addr)
	if err := http.ListenAndServe(addr, mux); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Printf("WebSocket server error: %v", err)
	}
}

// connectRelay connects to the daemon's /relay endpoint with reconnection.
func connectRelay() {
	url := fmt.Sprintf("ws://127.0.0.1:%d/relay", wsPort)
	backoff := 200 * time.Millisecond
	maxBackoff := 5 * time.Second

	for {
		conn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			// Daemon may have died — try restarting it.
			if restartErr := ensureDaemon(); restartErr != nil {
				logger.Printf("Relay connect failed, daemon restart failed: %v", restartErr)
			}
			logger.Printf("Relay connect failed: %v (retry in %v)", err, backoff)
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff = backoff * 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}

		backoff = 200 * time.Millisecond
		relayMu.Lock()
		relayConn = conn
		relayMu.Unlock()
		logger.Printf("Connected to daemon as relay client")

		// Identify ourselves to the daemon so it can attribute our commands.
		sess := snapshotSession()
		reg, _ := json.Marshal(map[string]any{
			"type":        "register",
			"label":       sess["label"],
			"sessionType": sess["sessionType"],
			"pid":         sess["pid"],
		})
		relayMu.Lock()
		_ = conn.WriteMessage(websocket.TextMessage, reg)
		relayMu.Unlock()

		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				logger.Printf("Relay connection lost: %v", err)
				break
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
					p.errCh <- errors.New(msg.Error)
				} else {
					p.resultCh <- msg.Result
				}
			}
		}

		relayMu.Lock()
		relayConn = nil
		relayMu.Unlock()
		conn.Close()

		// Fail every in-flight relay request immediately so callers
		// don't hang for the 30s tool timeout while we're reconnecting.
		// Without this, every Claude tool call mid-daemon-restart looks
		// stuck for 30s and the agent assumes "MCP isn't seeing a
		// connection" — even though the next call would succeed.
		failAllPending("relay connection to daemon lost — reconnecting")

		time.Sleep(backoff)
	}
}

// failAllPending wakes every pending request with an error. Used when
// the relay socket dies so callers see the actual failure mode instead
// of waiting out their per-request timeout.
func failAllPending(reason string) {
	pendingMu.Lock()
	for id, p := range pending {
		p.timer.Stop()
		select {
		case p.errCh <- errors.New(reason):
		default:
		}
		delete(pending, id)
	}
	pendingMu.Unlock()
}

// --- Browser connection handling (daemon only) ---

func handleWSConnection(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Printf("WebSocket upgrade error: %v", err)
		return
	}

	bc := &BrowserConnection{
		conn:   conn,
		name:   fmt.Sprintf("browser-%d", len(browsers)+1),
		tabIDs: make(map[int]bool),
	}
	// Browser name is set by the 'hello' push message the extension sends
	// in ws.onopen — no execute_js round-trip needed. The hello message
	// arrives immediately and requires no active tab.

	browsersMu.Lock()
	browsers[conn] = bc
	count := len(browsers)
	browsersMu.Unlock()
	logger.Printf("Extension connected (%d browser(s) total)", count)
	// Push the current MCP client list right away so the popup is populated
	// without waiting for a registration to happen.
	go broadcastClientsToBrowsers()

	defer func() {
		browsersMu.Lock()
		delete(browsers, conn)
		remaining := len(browsers)
		browsersMu.Unlock()
		conn.Close()
		logger.Printf("Extension disconnected (%d browser(s) remaining)", remaining)
	}()

	// Per-connection write mutex — pong replies race with command writes.
	var writeMu sync.Mutex

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			break
		}

		// Extension identifying itself on connect (sent from ws.onopen).
		// Using a push message avoids the execute_js round-trip that needed
		// an active tab — which could fail with "No active tab found" when
		// the daemon restarts in the background.
		var hello struct {
			Type string `json:"type"`
			UA   string `json:"ua"`
		}
		if json.Unmarshal(message, &hello) == nil && hello.Type == "hello" && hello.UA != "" {
			bc.mu.Lock()
			switch {
			case contains(hello.UA, "Zen"):
				bc.name = "Zen"
			case contains(hello.UA, "Arc"):
				bc.name = "Arc"
			case contains(hello.UA, "Brave"):
				bc.name = "Brave"
			case contains(hello.UA, "Edg"):
				bc.name = "Edge"
			case contains(hello.UA, "Firefox"):
				bc.name = "Firefox"
			case contains(hello.UA, "Chrome"):
				bc.name = "Chrome"
			}
			bc.mu.Unlock()
			continue
		}

		// Active health check from the extension service worker. We
		// reply synchronously so the SW can compare round-trip time
		// and tear down a zombie WS that Chrome killed silently.
		var ping struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if json.Unmarshal(message, &ping) == nil && ping.Type == "ping" {
			pong, _ := json.Marshal(map[string]any{"type": "pong", "id": ping.ID})
			writeMu.Lock()
			_ = conn.WriteMessage(websocket.TextMessage, pong)
			writeMu.Unlock()
			continue
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
				p.errCh <- errors.New(msg.Error)
			} else {
				p.resultCh <- msg.Result
			}
		}
	}
}

func getOpenBrowsers() []*BrowserConnection {
	browsersMu.RLock()
	defer browsersMu.RUnlock()
	var open []*BrowserConnection
	for _, bc := range browsers {
		open = append(open, bc)
	}
	return open
}

// connectionStatusJSON reports the calling process's view of browser
// connectivity. It must run in the process that actually owns the browser
// WebSockets — the daemon — so connection_status routes here through send()
// rather than reading a relay client's (always-empty) browsers map.
func connectionStatusJSON() json.RawMessage {
	open := getOpenBrowsers()
	names := make([]string, len(open))
	for i, b := range open {
		b.mu.Lock()
		names[i] = b.name
		b.mu.Unlock()
	}
	status := map[string]any{
		"connected":      len(open) > 0 || getBiDi() != nil,
		"extension":      len(open) > 0,
		"bidi":           getBiDi() != nil,
		"browsers":       names,
		"extensionCount": len(open),
		"wsPort":         wsPort,
		"version":        serverVersion,
	}
	// Surface "an update is available" passively — read-only, from the
	// cache the background checker warms. Never triggers a GitHub call,
	// so connection_status stays within its 3s diagnostic budget.
	if u := peekUpdateStatus(); u != nil && u.UpdateAvailable {
		status["updateAvailable"] = true
		status["latestVersion"] = u.LatestVersion
	}
	raw, _ := json.Marshal(status)
	return raw
}

// --- Send functions ---

// sendTo sends a command to a specific browser and waits for the response.
func sendTo(bc *BrowserConnection, action string, params map[string]any, timeoutMs int) (json.RawMessage, error) {
	id := strconv.FormatInt(nextID.Add(1), 10)
	resultCh := make(chan json.RawMessage, 1)
	errCh := make(chan error, 1)

	timer := time.AfterFunc(time.Duration(timeoutMs)*time.Millisecond, func() {
		pendingMu.Lock()
		if p, ok := pending[id]; ok {
			delete(pending, id)
			p.errCh <- fmt.Errorf("timeout after %dms: %s", timeoutMs, action)
		}
		pendingMu.Unlock()
	})

	req := &pendingRequest{resultCh: resultCh, errCh: errCh, timer: timer}
	pendingMu.Lock()
	pending[id] = req
	pendingMu.Unlock()

	msg, _ := json.Marshal(map[string]any{"id": id, "action": action, "params": params})
	bc.mu.Lock()
	err := bc.conn.WriteMessage(websocket.TextMessage, msg)
	bc.mu.Unlock()
	if err != nil {
		timer.Stop()
		pendingMu.Lock()
		delete(pending, id)
		pendingMu.Unlock()
		return nil, fmt.Errorf("websocket write error: %w", err)
	}

	select {
	case result := <-resultCh:
		return result, nil
	case err := <-errCh:
		return nil, err
	}
}

// sendViaRelay forwards a command through the relay connection to the daemon.
func sendViaRelay(action string, params map[string]any, timeoutMs int) (json.RawMessage, error) {
	relayMu.Lock()
	conn := relayConn
	relayMu.Unlock()
	if conn == nil {
		return nil, errors.New("Not connected to daemon. Waiting for reconnect.")
	}

	id := strconv.FormatInt(nextID.Add(1), 10)
	resultCh := make(chan json.RawMessage, 1)
	errCh := make(chan error, 1)

	timer := time.AfterFunc(time.Duration(timeoutMs)*time.Millisecond, func() {
		pendingMu.Lock()
		if p, ok := pending[id]; ok {
			delete(pending, id)
			p.errCh <- fmt.Errorf("timeout after %dms: %s (via relay)", timeoutMs, action)
		}
		pendingMu.Unlock()
	})

	req := &pendingRequest{resultCh: resultCh, errCh: errCh, timer: timer}
	pendingMu.Lock()
	pending[id] = req
	pendingMu.Unlock()

	msg, _ := json.Marshal(map[string]any{
		"id":      id,
		"action":  action,
		"params":  params,
		"timeout": timeoutMs,
	})
	relayMu.Lock()
	err := conn.WriteMessage(websocket.TextMessage, msg)
	relayMu.Unlock()
	if err != nil {
		timer.Stop()
		pendingMu.Lock()
		delete(pending, id)
		pendingMu.Unlock()
		return nil, fmt.Errorf("relay write error: %w", err)
	}

	select {
	case result := <-resultCh:
		return result, nil
	case err := <-errCh:
		return nil, err
	}
}

// send routes a command to the browser(s), either via relay or directly.
func send(action string, params map[string]any, timeoutMs ...int) (json.RawMessage, error) {
	timeout := 30000
	if len(timeoutMs) > 0 {
		timeout = timeoutMs[0]
	}

	if useRelay {
		return sendViaRelay(action, params, timeout)
	}

	return sendDirect(action, params, timeout)
}

// sendDirect routes a command directly to browsers (daemon mode or in-process fallback).
func sendDirect(action string, params map[string]any, timeout int) (json.RawMessage, error) {
	// connection_status is answered by this process, not forwarded to a
	// browser — the extension has no such command. Handled here (before the
	// "no browser connected" error path) so a relay client's connection_status
	// reaches the daemon, where the browser WebSockets actually live.
	if action == "connection_status" {
		return connectionStatusJSON(), nil
	}

	// Clone params before mutating: a caller may pass one map across several
	// concurrent send() calls (handleDescribe fans out exactly this way), and
	// writing _clientLabel/_clientHue into a shared map is a data race. The
	// clone also covers the nil case — ranging a nil map yields no entries.
	merged := make(map[string]any, len(params)+2)
	for k, v := range params {
		merged[k] = v
	}
	params = merged

	// Ensure every outgoing command carries client-attribution metadata so the
	// extension popup can attribute it. The relay handler may have already
	// attached _clientLabel; only fill it from our session if absent.
	if _, ok := params["_clientLabel"]; !ok {
		if lbl := getSessionLabel(); lbl != "" {
			params["_clientLabel"] = lbl
		}
	}
	// Default hue: brand. In-process mode = single agent = brand orange.
	// (Relay mode sets _clientHue at the relay handler.)
	if _, ok := params["_clientHue"]; !ok {
		params["_clientHue"] = brandHue
	}
	open := getOpenBrowsers()
	if len(open) == 0 {
		// Try auto-launching a browser
		if browserConfig != nil && browserConfig.AutoLaunch {
			if _, err := launchBrowser(false); err != nil {
				logger.Printf("Auto-launch failed: %v", err)
			} else {
				open = getOpenBrowsers()
			}
		}
	}
	if len(open) == 0 {
		return nil, fmt.Errorf(
			"No browser extension connected to turboweb daemon on port %d. "+
				"Troubleshooting: (1) Open Chrome and check chrome://extensions/ — the turboweb extension must be enabled and not crashed. "+
				"(2) The MV3 service worker may be suspended; opening the extension popup or chrome://extensions/ wakes it. "+
				"(3) Verify the daemon is alive: `curl http://127.0.0.1:%d/version`. "+
				"(4) If the daemon was just rebuilt, an in-memory stale daemon may be running — kill it and the next MCP call will spawn a fresh one.",
			wsPort, wsPort,
		)
	}

	// list_tabs: query all browsers, merge results
	if action == "list_tabs" {
		return sendListTabs(open, timeout)
	}

	// If tabId specified, route to owning browser
	if params != nil {
		if tabIDRaw, ok := params["tabId"]; ok && tabIDRaw != nil {
			tabID := toInt(tabIDRaw)
			return sendToTab(open, tabID, action, params, timeout)
		}
	}

	// No tabId — use first browser
	return sendTo(open[0], action, params, timeout)
}

// sendListTabs fans out list_tabs to all browsers and merges results.
func sendListTabs(open []*BrowserConnection, timeout int) (json.RawMessage, error) {
	type result struct {
		tabs []json.RawMessage
	}
	results := make([]result, len(open))
	var wg sync.WaitGroup

	for i, bc := range open {
		wg.Add(1)
		go func(i int, bc *BrowserConnection) {
			defer wg.Done()
			raw, err := sendTo(bc, "list_tabs", nil, timeout)
			if err != nil {
				return
			}
			var tabs []json.RawMessage
			if json.Unmarshal(raw, &tabs) == nil {
				results[i] = result{tabs: tabs}
				// Cache tab IDs with active state for disambiguation
				// when multiple browsers share the same tab ID.
				for _, t := range tabs {
					var tab struct {
						ID     int  `json:"id"`
						Active bool `json:"active"`
					}
					if json.Unmarshal(t, &tab) == nil {
						bc.mu.Lock()
						bc.tabIDs[tab.ID] = tab.Active
						bc.mu.Unlock()
					}
				}
			}
		}(i, bc)
	}
	wg.Wait()

	// Refresh browser names in case the on-connect goroutine hasn't resolved yet.
	for _, bc := range open {
		go func(bc *BrowserConnection) {
			bc.mu.Lock()
			alreadyNamed := bc.name != "" && bc.name != "Chrome" // "Chrome" is the default fallback
			_ = alreadyNamed
			bc.mu.Unlock()
			raw, err := sendTo(bc, "execute_js", map[string]any{"code": jsBrowserFingerprint}, 3000)
			if err != nil {
				return
			}
			var resp struct {
				Result string `json:"result"`
			}
			if json.Unmarshal(raw, &resp) == nil {
				ua := resp.Result
				bc.mu.Lock()
				switch {
				case contains(ua, "Zen"):
					bc.name = "Zen"
				case contains(ua, "Arc"):
					bc.name = "Arc"
				case contains(ua, "Brave"):
					bc.name = "Brave"
				case contains(ua, "Edg"):
					bc.name = "Edge"
				case contains(ua, "Firefox"):
					bc.name = "Firefox"
				case contains(ua, "Chrome"):
					bc.name = "Chrome"
				}
				bc.mu.Unlock()
			}
		}(bc)
	}

	// Merge all tabs
	var all []json.RawMessage
	for _, r := range results {
		all = append(all, r.tabs...)
	}
	merged, _ := json.Marshal(all)
	return merged, nil
}

// sendToTab routes a command to the browser that owns the given tab ID.
// When multiple browsers have the same tab ID cached (common with multi-window
// setups like Zen), the browser whose tab is currently active wins.
func sendToTab(open []*BrowserConnection, tabID int, action string, params map[string]any, timeout int) (json.RawMessage, error) {
	// Check cached ownership — prefer the browser where this tab is active.
	var candidate *BrowserConnection
	for _, bc := range open {
		bc.mu.Lock()
		isActive, owns := bc.tabIDs[tabID]
		bc.mu.Unlock()
		if owns {
			if isActive {
				candidate = bc
				break // active tab wins immediately; no need to check further
			}
			if candidate == nil {
				candidate = bc // keep as fallback if no active one is found
			}
		}
	}
	if candidate != nil {
		return sendTo(candidate, action, params, timeout)
	}
	// Not cached — try each browser; first success wins.
	for _, bc := range open {
		result, err := sendTo(bc, action, params, timeout)
		if err != nil {
			if contains(err.Error(), "No tab") || contains(err.Error(), "Cannot access") {
				continue
			}
			return nil, err
		}
		bc.mu.Lock()
		bc.tabIDs[tabID] = false // cache ownership; active state unknown until next list_tabs
		bc.mu.Unlock()
		return result, nil
	}
	return nil, fmt.Errorf("Tab %d not found in any connected browser", tabID)
}
