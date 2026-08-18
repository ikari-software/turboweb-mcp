package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerDebugTools registers tools that use BiDi (replacing the old CDP-via-extension tools).
// These work identically in Chrome and Firefox — same protocol, same commands.
func registerDebugTools(s *server.MCPServer) {
	// --- Network monitoring ---
	addTool(s,
		mcp.NewTool("network_enable",
			mcp.WithDescription("Start capturing network requests via BiDi. Requests are buffered and can be retrieved with network_get."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			mcp.WithNumber("maxSize", mcp.Description("Max requests to buffer (default 500)")),
		),
		bidiOrFallback("network_enable", handleNetworkEnable),
	)

	addTool(s,
		mcp.NewTool("network_get",
			mcp.WithDescription("Get captured network requests. Call network_enable first."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			mcp.WithString("filter", mcp.Description("Filter requests by URL/type/method substring")),
			mcp.WithNumber("limit", mcp.Description("Max results to return (default 100)")),
		),
		bidiOrFallback("network_get", handleNetworkGet),
	)

	addTool(s,
		mcp.NewTool("network_disable",
			mcp.WithDescription("Stop capturing network requests."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		bidiOrFallback("network_disable", handleNetworkDisable),
	)

	// --- Console monitoring ---
	addTool(s,
		mcp.NewTool("console_enable",
			mcp.WithDescription("Start capturing console messages via BiDi log events."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			mcp.WithNumber("maxSize", mcp.Description("Max messages to buffer (default 200)")),
		),
		bidiOrFallback("console_enable", handleConsoleEnable),
	)

	addTool(s,
		mcp.NewTool("console_get",
			mcp.WithDescription("Get captured console messages. Call console_enable first."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			mcp.WithString("type", mcp.Description("Filter by type: log, warn, error, info, debug")),
			mcp.WithNumber("limit", mcp.Description("Max results (default 100)")),
		),
		bidiOrFallback("console_get", handleConsoleGet),
	)

	addTool(s,
		mcp.NewTool("console_clear",
			mcp.WithDescription("Clear captured console messages buffer."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		bidiOrFallback("console_clear", handleConsoleClear),
	)

	addTool(s,
		mcp.NewTool("console_disable",
			mcp.WithDescription("Stop capturing console messages."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		bidiOrFallback("console_disable", handleConsoleDisable),
	)

	// --- Cookies ---
	addTool(s,
		mcp.NewTool("get_cookies",
			mcp.WithDescription("Get all cookies for the current page."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		bidiOrFallback("get_cookies", handleGetCookies),
	)

	addTool(s,
		mcp.NewTool("set_cookie",
			mcp.WithDescription("Set a browser cookie."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Cookie name")),
			mcp.WithString("value", mcp.Required(), mcp.Description("Cookie value")),
			mcp.WithString("domain", mcp.Required(), mcp.Description("Cookie domain")),
			mcp.WithString("path", mcp.Description("Cookie path (default '/')")),
			mcp.WithBoolean("secure", mcp.Description("Secure flag")),
			mcp.WithBoolean("httpOnly", mcp.Description("HttpOnly flag")),
			mcp.WithString("sameSite", mcp.Description("SameSite: Strict, Lax, or None")),
			mcp.WithNumber("expires", mcp.Description("Expiry as Unix timestamp")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		bidiOrFallback("set_cookie", handleSetCookie),
	)

	addTool(s,
		mcp.NewTool("delete_cookies",
			mcp.WithDescription("Delete cookies by name and domain."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Cookie name")),
			mcp.WithString("domain", mcp.Required(), mcp.Description("Cookie domain")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		bidiOrFallback("delete_cookies", handleDeleteCookies),
	)

	// --- Page reload ---
	addTool(s,
		mcp.NewTool("page_reload",
			mcp.WithDescription("Reload the page. Use ignoreCache=true for hard reload."),
			mcp.WithBoolean("ignoreCache", mcp.Description("Bypass cache (hard reload)")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		bidiOrFallback("page_reload", handlePageReload),
	)

	// --- Emulation ---
	addTool(s,
		mcp.NewTool("emulate_device",
			mcp.WithDescription("Emulate a viewport, user agent, or geolocation. Use disable=true to reset."),
			mcp.WithNumber("width", mcp.Description("Viewport width (default 375)")),
			mcp.WithNumber("height", mcp.Description("Viewport height (default 812)")),
			mcp.WithString("userAgent", mcp.Description("Custom User-Agent string")),
			mcp.WithNumber("latitude", mcp.Description("Geolocation latitude")),
			mcp.WithNumber("longitude", mcp.Description("Geolocation longitude")),
			mcp.WithBoolean("disable", mcp.Description("Disable emulation and reset")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		bidiOrFallback("emulate_device", handleEmulateDevice),
	)

	// --- PDF generation ---
	addTool(s,
		mcp.NewTool("print_to_pdf",
			mcp.WithDescription("Generate a PDF of the page. Returns base64-encoded PDF data."),
			mcp.WithBoolean("landscape", mcp.Description("Landscape orientation")),
			mcp.WithNumber("scale", mcp.Description("Scale factor (default 1)")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		bidiOrFallback("print_to_pdf", handlePrintToPDF),
	)

	// --- Script preload (bot detection patches, etc.) ---
	addTool(s,
		mcp.NewTool("add_preload_script",
			mcp.WithDescription("Inject JavaScript that runs before any page script on every navigation. Useful for bot-detection patches."),
			mcp.WithString("code", mcp.Required(), mcp.Description("JavaScript code to inject")),
		),
		handleAddPreloadScript,
	)

	// --- Storage ---
	addTool(s,
		mcp.NewTool("get_storage",
			mcp.WithDescription("Get all localStorage or sessionStorage entries."),
			mcp.WithString("type", mcp.Description("Storage type: 'local' or 'session' (default 'local')")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		handleGetStorage,
	)

	// --- Tools that fall back to extension (no BiDi-native equivalent yet) ---
	addTool(s,
		mcp.NewTool("network_get_body",
			mcp.WithDescription("Get the response body of a captured network request by its requestId."),
			mcp.WithString("requestId", mcp.Required(), mcp.Description("Request ID from network_get results")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		passThrough("network_get_body"),
	)

	addTool(s,
		mcp.NewTool("get_performance",
			mcp.WithDescription("Get performance metrics: JS heap size, DOM nodes, layout count, etc."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		passThrough("get_performance"),
	)

	addTool(s,
		mcp.NewTool("get_accessibility_tree",
			mcp.WithDescription("Get the accessibility tree (AX tree) for the page."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			mcp.WithNumber("depth", mcp.Description("Max tree depth (default 5)")),
			mcp.WithNumber("maxNodes", mcp.Description("Max nodes to return (default 500)")),
		),
		passThrough("get_accessibility_tree"),
	)

	addTool(s,
		mcp.NewTool("network_throttle",
			mcp.WithDescription("Simulate slow network conditions. Presets: 'slow-3g', 'fast-3g', 'offline'."),
			mcp.WithString("preset", mcp.Description("Preset: slow-3g, fast-3g, offline")),
			mcp.WithNumber("latency", mcp.Description("Added latency in ms")),
			mcp.WithBoolean("offline", mcp.Description("Simulate offline")),
			mcp.WithBoolean("disable", mcp.Description("Disable throttling")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		passThrough("network_throttle"),
	)

	addTool(s,
		mcp.NewTool("dom_snapshot",
			mcp.WithDescription("Capture a DOM snapshot with computed styles and layout rectangles."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			mcp.WithBoolean("includeDOMRects", mcp.Description("Include element bounding rects (default true)")),
		),
		passThrough("dom_snapshot"),
	)

	addTool(s,
		mcp.NewTool("css_coverage_start",
			mcp.WithDescription("Start tracking CSS rule usage."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		passThrough("css_coverage_start"),
	)

	addTool(s,
		mcp.NewTool("css_coverage_stop",
			mcp.WithDescription("Stop CSS coverage tracking and get results."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		passThrough("css_coverage_stop"),
	)

	addTool(s,
		mcp.NewTool("cdp_detach",
			mcp.WithDescription("Detach the debugger from a tab. Clears network/console captures."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		passThrough("cdp_detach"),
	)
}

// bidiOrFallback returns the BiDi handler if BiDi is connected, otherwise falls back
// to the extension passThrough handler.
func bidiOrFallback(action string, bidiHandler server.ToolHandlerFunc) server.ToolHandlerFunc {
	extensionHandler := passThrough(action)
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if getBiDi() != nil {
			return bidiHandler(ctx, req)
		}
		// No BiDi: fall back to the extension. Frame targeting is handled there:
		// on Chrome the cdp_* handlers resolve the element inside the frame via
		// the all_frames content script and route trusted input to the OOPIF
		// (Chrome's cross-process input router); on Firefox (no chrome.debugger)
		// ensureDebugger returns an actionable "connect BiDi" error. Either way
		// the extension is the single source of truth for the frame capability,
		// so we no longer refuse here.
		return extensionHandler(ctx, req)
	}
}

// =============================================================================
// Handler implementations
// =============================================================================

// --- Network monitoring state (BiDi-side) ---
var (
	bidiNetworkMu              sync.Mutex
	bidiConsoleMu              sync.Mutex
	bidiNetworkEntries         = make(map[string][]map[string]any) // contextID → entries
	bidiConsoleEntries         []BiDiLogEntry
	bidiCollectorMu            sync.Mutex
	bidiNetworkCollectorClient *BiDiClient
	bidiConsoleCollectorClient *BiDiClient
)

func ensureBidiNetworkCollector() {
	c := getBiDi()
	if c == nil {
		return
	}
	bidiCollectorMu.Lock()
	defer bidiCollectorMu.Unlock()
	if bidiNetworkCollectorClient == c {
		return
	}
	c.OnEvent("network.beforeRequestSent", func(_ string, params json.RawMessage) {
		var ev struct {
			Context string `json:"context"`
			Request struct {
				URL     string `json:"url"`
				Method  string `json:"method"`
				Headers []struct {
					Name  string `json:"name"`
					Value struct {
						Value string `json:"value"`
					} `json:"value"`
				} `json:"headers"`
			} `json:"request"`
			Timestamp int64 `json:"timestamp"`
		}
		if json.Unmarshal(params, &ev) == nil {
			entry := map[string]any{
				"url":       ev.Request.URL,
				"method":    ev.Request.Method,
				"timestamp": ev.Timestamp,
			}
			bidiNetworkMu.Lock()
			bidiNetworkEntries["_all"] = append(bidiNetworkEntries["_all"], entry)
			if len(bidiNetworkEntries["_all"]) > 500 {
				bidiNetworkEntries["_all"] = bidiNetworkEntries["_all"][1:]
			}
			bidiNetworkMu.Unlock()
		}
	})
	bidiNetworkCollectorClient = c
}

func handleNetworkEnable(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	_, err := bidiNetworkEnable(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	ensureBidiNetworkCollector()
	return mcp.NewToolResultText(`{"enabled": true}`), nil
}

func handleNetworkGet(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	bidiNetworkMu.Lock()
	entries := append([]map[string]any(nil), bidiNetworkEntries["_all"]...)
	bidiNetworkMu.Unlock()
	if entries == nil {
		entries = []map[string]any{}
	}
	args := req.GetArguments()
	// Apply filter
	if filter, ok := args["filter"].(string); ok && filter != "" {
		var filtered []map[string]any
		for _, e := range entries {
			url, _ := e["url"].(string)
			method, _ := e["method"].(string)
			if contains(url, filter) || contains(method, filter) {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}
	// Apply limit
	limit := 100
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	result, _ := json.Marshal(map[string]any{"count": len(entries), "requests": entries})
	return mcp.NewToolResultText(string(result)), nil
}

func handleNetworkDisable(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	bidiNetworkMu.Lock()
	bidiNetworkEntries = make(map[string][]map[string]any)
	bidiNetworkMu.Unlock()
	return mcp.NewToolResultText(`{"disabled": true}`), nil
}

func ensureBidiConsoleCollector() {
	c := getBiDi()
	if c == nil {
		return
	}
	bidiCollectorMu.Lock()
	defer bidiCollectorMu.Unlock()
	if bidiConsoleCollectorClient == c {
		return
	}
	c.OnEvent("log.entryAdded", func(_ string, params json.RawMessage) {
		var entry BiDiLogEntry
		if json.Unmarshal(params, &entry) == nil {
			bidiConsoleMu.Lock()
			bidiConsoleEntries = append(bidiConsoleEntries, entry)
			if len(bidiConsoleEntries) > 200 {
				bidiConsoleEntries = bidiConsoleEntries[1:]
			}
			bidiConsoleMu.Unlock()
		}
	})
	bidiConsoleCollectorClient = c
}

func handleConsoleEnable(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	bidiConsoleMu.Lock()
	bidiConsoleEntries = nil
	bidiConsoleMu.Unlock()
	if err := bidiConsoleSubscribeOnce(ctx); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	ensureBidiConsoleCollector()
	return mcp.NewToolResultText(`{"enabled": true}`), nil
}

func handleConsoleGet(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	bidiConsoleMu.Lock()
	entries := append([]BiDiLogEntry(nil), bidiConsoleEntries...)
	bidiConsoleMu.Unlock()
	if entries == nil {
		entries = []BiDiLogEntry{}
	}
	args := req.GetArguments()
	if t, ok := args["type"].(string); ok && t != "" {
		var filtered []BiDiLogEntry
		for _, e := range entries {
			if e.Level == t {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}
	limit := 100
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	result, _ := json.Marshal(map[string]any{"count": len(entries), "messages": entries})
	return mcp.NewToolResultText(string(result)), nil
}

func handleConsoleClear(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	bidiConsoleMu.Lock()
	bidiConsoleEntries = nil
	bidiConsoleMu.Unlock()
	return mcp.NewToolResultText(`{"cleared": true}`), nil
}

func handleConsoleDisable(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	bidiConsoleMu.Lock()
	bidiConsoleEntries = nil
	bidiConsoleMu.Unlock()
	return mcp.NewToolResultText(`{"disabled": true}`), nil
}

// --- Cookies ---

func handleGetCookies(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	raw, err := bidiGetCookies(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(raw)), nil
}

func handleSetCookie(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	err := bidiSetCookie(ctx,
		toString(args["name"]), toString(args["value"]),
		toString(args["domain"]), toString(args["path"]),
		toBool(args["secure"]), toBool(args["httpOnly"]),
		toString(args["sameSite"]), toFloat(args["expires"]),
	)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(`{"success": true}`), nil
}

func handleDeleteCookies(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	err := bidiDeleteCookies(ctx, toString(args["name"]), toString(args["domain"]))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf(`{"deleted": true, "name": %q}`, toString(args["name"]))), nil
}

// --- Page reload ---

func handlePageReload(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	ctxID, err := resolveContext(args["tabId"])
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	err = bidiReload(ctx, ctxID, toBool(args["ignoreCache"]))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(`{"reloaded": true}`), nil
}

// --- Emulation ---

func handleEmulateDevice(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	ctxID, err := resolveContext(args["tabId"])
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if toBool(args["disable"]) {
		// Reset viewport to default
		_ = bidiSetViewport(ctx, ctxID, 1280, 800)
		return mcp.NewToolResultText(`{"emulation": "disabled"}`), nil
	}

	w := intOr(args["width"], 375)
	h := intOr(args["height"], 812)
	if err := bidiSetViewport(ctx, ctxID, w, h); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if ua := toString(args["userAgent"]); ua != "" {
		bidiSetUserAgent(ctx, ua)
	}

	if lat, ok := args["latitude"].(float64); ok {
		lng, _ := args["longitude"].(float64)
		bidiSetGeolocation(ctx, lat, lng, 100)
	}

	result, _ := json.Marshal(map[string]any{"emulation": "enabled", "width": w, "height": h})
	return mcp.NewToolResultText(string(result)), nil
}

// --- PDF ---

func handlePrintToPDF(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	ctxID, err := resolveContext(args["tabId"])
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	landscape := toBool(args["landscape"])
	scale := 1.0
	if s, ok := args["scale"].(float64); ok && s > 0 {
		scale = s
	}
	data, err := bidiPrint(ctx, ctxID, landscape, scale)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	encoded := base64Encode(data)
	result, _ := json.Marshal(map[string]any{"base64": encoded, "mimeType": "application/pdf"})
	return mcp.NewToolResultText(string(result)), nil
}

// --- Preload script ---

func handleAddPreloadScript(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	c, err := requireBiDi()
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	_ = c // used via bidiAddPreloadScript
	args := req.GetArguments()
	scriptID, err := bidiAddPreloadScript(ctx, toString(args["code"]))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	result, _ := json.Marshal(map[string]any{"scriptId": scriptID, "injected": true})
	return mcp.NewToolResultText(string(result)), nil
}

// --- Storage ---

func handleGetStorage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	ctxID, err := resolveContext(args["tabId"])
	if err != nil {
		// Fall back to extension if no BiDi
		return passThrough("get_storage")(ctx, req)
	}
	storageType := "local"
	if t := toString(args["type"]); t != "" {
		storageType = t
	}
	expr := fmt.Sprintf("JSON.parse(JSON.stringify(Object.fromEntries(Object.entries(%sStorage))))", storageType)
	raw, err := bidiEvaluate(ctx, ctxID, expr)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(raw)), nil
}

// =============================================================================
// Helpers
// =============================================================================

func toString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func toBool(v any) bool {
	if v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

// toStringSlice coerces a JSON array (or a single string) into []string,
// dropping empty entries. MCP array args arrive as []any.
func toStringSlice(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s := toString(e); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	default:
		return nil
	}
}

func intOr(v any, def int) int {
	if v == nil {
		return def
	}
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return def
}

func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
