package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// withMockBrowser sets up a WebSocket server with a mock browser and runs fn.
func withMockBrowser(t *testing.T, handler func(msg map[string]any) map[string]any, fn func()) {
	t.Helper()
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	srv, wsURL := startTestWSServer(t)
	defer srv.Close()

	conn := connectMockBrowser(t, wsURL, handler)
	defer conn.Close()

	fn()
}

// echoHandler echoes the result field for any action.
func echoHandler(msg map[string]any) map[string]any {
	action, _ := msg["action"].(string)
	params, _ := msg["params"].(map[string]any)
	return map[string]any{"result": map[string]any{"action": action, "params": params}}
}

// --- connection_status ---

func TestHandleConnectionStatus_NoBrowsers(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	result, err := handleConnectionStatus(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	text := extractText(t, result)
	var resp struct {
		Connected      bool     `json:"connected"`
		Browsers       []string `json:"browsers"`
		ExtensionCount int      `json:"extensionCount"`
		WsPort         int      `json:"wsPort"`
	}
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parse: %v\nraw: %s", err, text)
	}
	if resp.Connected {
		t.Error("should not be connected")
	}
	if resp.ExtensionCount != 0 {
		t.Errorf("extensionCount = %d, want 0", resp.ExtensionCount)
	}
	if resp.WsPort != 18321 {
		t.Errorf("wsPort = %d, want 18321", resp.WsPort)
	}
}

func TestHandleConnectionStatus_WithBrowser(t *testing.T) {
	withMockBrowser(t, echoHandler, func() {
		result, err := handleConnectionStatus(context.Background(), mcp.CallToolRequest{})
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		text := extractText(t, result)
		var resp struct {
			Connected      bool `json:"connected"`
			Extension      bool `json:"extension"`
			ExtensionCount int  `json:"extensionCount"`
		}
		json.Unmarshal([]byte(text), &resp)
		if !resp.Connected {
			t.Error("should be connected")
		}
		if !resp.Extension {
			t.Error("extension should be true")
		}
		if resp.ExtensionCount != 1 {
			t.Errorf("extensionCount = %d, want 1", resp.ExtensionCount)
		}
	})
}

// --- list_tabs ---

func TestHandleListTabs(t *testing.T) {
	withMockBrowser(t, func(msg map[string]any) map[string]any {
		action := msg["action"].(string)
		if action == "list_tabs" {
			return map[string]any{"result": []map[string]any{
				{"id": 1, "title": "Test Page", "url": "https://example.com"},
			}}
		}
		if action == "execute_js" {
			return map[string]any{"result": map[string]any{"result": "Mozilla Chrome"}}
		}
		return map[string]any{"result": nil}
	}, func() {
		result, err := handleListTabs(context.Background(), mcp.CallToolRequest{})
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		text := extractText(t, result)
		if !strings.Contains(text, "Test Page") {
			t.Errorf("should contain tab title: %q", text)
		}
	})
}

func TestHandleListTabs_NoBrowsers(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	result, err := handleListTabs(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error with no browsers")
	}
}

// --- navigate ---

func TestHandleNavigate(t *testing.T) {
	withMockBrowser(t, func(msg map[string]any) map[string]any {
		params := msg["params"].(map[string]any)
		return map[string]any{"result": map[string]any{"url": params["url"]}}
	}, func() {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{"url": "https://example.com"},
			},
		}
		result, err := handleNavigate(context.Background(), req)
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		text := extractText(t, result)
		if !strings.Contains(text, "example.com") {
			t.Errorf("should contain URL: %q", text)
		}
	})
}

func TestHandleNavigate_NoBrowser(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{"url": "https://example.com"},
		},
	}
	result, _ := handleNavigate(context.Background(), req)
	if !result.IsError {
		t.Error("expected error with no browsers")
	}
}

// --- screenshot ---

func TestHandleScreenshot(t *testing.T) {
	withMockBrowser(t, func(msg map[string]any) map[string]any {
		return map[string]any{"result": map[string]any{
			"base64": "dGVzdA==", "mimeType": "image/jpeg", "width": 1280, "height": 720,
		}}
	}, func() {
		result, err := handleScreenshot(context.Background(), mcp.CallToolRequest{})
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if len(result.Content) != 2 {
			t.Fatalf("expected 2 content blocks, got %d", len(result.Content))
		}
		b, _ := json.Marshal(result.Content[1])
		var textBlock struct{ Text string }
		json.Unmarshal(b, &textBlock)
		if textBlock.Text != "1280x720 jpeg" {
			t.Errorf("text = %q, want %q", textBlock.Text, "1280x720 jpeg")
		}
	})
}

func TestHandleScreenshot_NoBrowser(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	result, _ := handleScreenshot(context.Background(), mcp.CallToolRequest{})
	if !result.IsError {
		t.Error("expected error")
	}
}

func TestHandleScreenshot_BadResponse(t *testing.T) {
	withMockBrowser(t, func(msg map[string]any) map[string]any {
		return map[string]any{"result": "not a screenshot object"}
	}, func() {
		result, _ := handleScreenshot(context.Background(), mcp.CallToolRequest{})
		if !result.IsError {
			t.Error("expected error for bad response shape")
		}
	})
}

// --- turbo_snapshot ---

func TestHandleTurboSnapshot(t *testing.T) {
	withMockBrowser(t, func(msg map[string]any) map[string]any {
		return map[string]any{"result": map[string]any{
			"screenshot": map[string]any{
				"base64": "dGVzdA==", "mimeType": "image/jpeg", "width": 1280, "height": 720,
			},
			"interactiveMap": []map[string]any{{"tag": "button", "text": "Click me"}},
		}}
	}, func() {
		result, err := handleTurboSnapshot(context.Background(), mcp.CallToolRequest{})
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if len(result.Content) != 2 {
			t.Fatalf("expected 2 content blocks, got %d", len(result.Content))
		}
	})
}

func TestHandleTurboSnapshot_NoBrowser(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	result, _ := handleTurboSnapshot(context.Background(), mcp.CallToolRequest{})
	if !result.IsError {
		t.Error("expected error")
	}
}

func TestHandleTurboSnapshot_BadResponse(t *testing.T) {
	withMockBrowser(t, func(msg map[string]any) map[string]any {
		return map[string]any{"result": "bad"}
	}, func() {
		result, _ := handleTurboSnapshot(context.Background(), mcp.CallToolRequest{})
		if !result.IsError {
			t.Error("expected error for bad response")
		}
	})
}

// --- passThrough ---

func TestPassThrough_Success(t *testing.T) {
	withMockBrowser(t, echoHandler, func() {
		handler := passThrough("click")
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{"selector": "#btn"},
			},
		}
		result, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		text := extractText(t, result)
		if !strings.Contains(text, "click") {
			t.Errorf("should contain action: %q", text)
		}
	})
}

func TestPassThrough_NoBrowser(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	handler := passThrough("click")
	result, _ := handler(context.Background(), mcp.CallToolRequest{})
	if !result.IsError {
		t.Error("expected error")
	}
}

// --- DOM tools ---

func TestHandleFindText(t *testing.T) {
	withMockBrowser(t, func(msg map[string]any) map[string]any {
		return map[string]any{"result": []map[string]any{
			{"text": "Found it", "selector": "#result", "x": 10, "y": 20},
		}}
	}, func() {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{"query": "Find"},
			},
		}
		result, err := handleFindText(context.Background(), req)
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		text := extractText(t, result)
		if !strings.Contains(text, "Found it") {
			t.Errorf("should contain match: %q", text)
		}
	})
}

func TestHandleInspect(t *testing.T) {
	withMockBrowser(t, func(msg map[string]any) map[string]any {
		return map[string]any{"result": map[string]any{
			"tag": "div", "text": "Hello", "rect": map[string]any{"x": 0, "y": 0, "w": 100, "h": 50},
		}}
	}, func() {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{"selector": "#main"},
			},
		}
		result, err := handleInspect(context.Background(), req)
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		text := extractText(t, result)
		if !strings.Contains(text, "Hello") {
			t.Errorf("should contain element text: %q", text)
		}
	})
}

func TestHandleGetHTML(t *testing.T) {
	withMockBrowser(t, func(msg map[string]any) map[string]any {
		return map[string]any{"result": "<div>Hello</div>"}
	}, func() {
		result, err := handleGetHTML(context.Background(), mcp.CallToolRequest{})
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		text := extractText(t, result)
		// JSON encodes <> as \u003c/\u003e, so check for the text content
		if !strings.Contains(text, "Hello") {
			t.Errorf("should contain HTML content: %q", text)
		}
	})
}

func TestHandleGetInteractiveMap(t *testing.T) {
	withMockBrowser(t, func(msg map[string]any) map[string]any {
		return map[string]any{"result": []map[string]any{
			{"tag": "button", "text": "Submit", "selector": "button.submit"},
		}}
	}, func() {
		result, err := handleGetInteractiveMap(context.Background(), mcp.CallToolRequest{})
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		text := extractText(t, result)
		if !strings.Contains(text, "Submit") {
			t.Errorf("should contain button text: %q", text)
		}
	})
}

func TestHandleQueryElements(t *testing.T) {
	withMockBrowser(t, func(msg map[string]any) map[string]any {
		return map[string]any{"result": []map[string]any{
			{"tag": "input", "selector": "input[name=email]"},
		}}
	}, func() {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{"selector": "input"},
			},
		}
		result, err := handleQueryElements(context.Background(), req)
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		text := extractText(t, result)
		if !strings.Contains(text, "email") {
			t.Errorf("should contain element: %q", text)
		}
	})
}

func TestHandleExtractText_NoQuestion(t *testing.T) {
	withMockBrowser(t, func(msg map[string]any) map[string]any {
		return map[string]any{"result": map[string]any{
			"count": 1, "blocks": []map[string]any{{"text": "Hello", "x": 10, "y": 20}},
		}}
	}, func() {
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{}}}
		result, _ := handleExtractText(context.Background(), req)
		text := extractText(t, result)
		if !strings.Contains(text, "Hello") {
			t.Errorf("should contain text: %q", text)
		}
	})
}

func TestHandleExtractText_NoBrowser(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{}}}
	result, _ := handleExtractText(context.Background(), req)
	if !result.IsError {
		t.Error("expected error")
	}
}

func TestHandlePageYaml(t *testing.T) {
	withMockBrowser(t, func(msg map[string]any) map[string]any {
		return map[string]any{"result": map[string]any{
			"yaml": "heading: Welcome\nparagraph: Hello world",
		}}
	}, func() {
		result, _ := handlePageYaml(context.Background(), mcp.CallToolRequest{})
		text := extractText(t, result)
		if !strings.Contains(text, "heading: Welcome") {
			t.Errorf("should extract yaml field: %q", text)
		}
	})
}

func TestHandlePageYaml_NoYAMLField(t *testing.T) {
	withMockBrowser(t, func(msg map[string]any) map[string]any {
		return map[string]any{"result": "raw structure data"}
	}, func() {
		result, _ := handlePageYaml(context.Background(), mcp.CallToolRequest{})
		text := extractText(t, result)
		if !strings.Contains(text, "raw structure data") {
			t.Errorf("should fallback to raw: %q", text)
		}
	})
}

// --- describe (composite tool) ---

func TestHandleDescribe_NoHaiku(t *testing.T) {
	origHaiku := haiku
	haiku = nil
	defer func() { haiku = origHaiku }()

	withMockBrowser(t, func(msg map[string]any) map[string]any {
		action := msg["action"].(string)
		switch action {
		case "screenshot":
			return map[string]any{"result": map[string]any{
				"base64": "dGVzdA==", "mimeType": "image/jpeg", "width": 100, "height": 100,
			}}
		case "get_page_structure":
			return map[string]any{"result": map[string]any{"yaml": "heading: Test"}}
		case "get_interactive_map":
			return map[string]any{"result": []map[string]any{{"tag": "button"}}}
		}
		return map[string]any{"result": nil}
	}, func() {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{"question": "what is this?"},
			},
		}
		result, err := handleDescribe(context.Background(), req)
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		text := extractText(t, result)
		// Without Haiku, should return raw data as JSON
		if !strings.Contains(text, "what is this?") {
			t.Errorf("should contain question in raw output: %q", text)
		}
	})
}

func TestHandleDescribe_WithHaiku(t *testing.T) {
	srv := httptest.NewServer(mockAnthropicHandler("This is a test page with a button"))
	defer srv.Close()

	origHaiku := haiku
	haiku = newTestHaikuClient(srv.URL)
	haiku.httpClient.Transport = &redirectTransport{url: srv.URL}
	defer func() { haiku = origHaiku }()

	withMockBrowser(t, func(msg map[string]any) map[string]any {
		action := msg["action"].(string)
		switch action {
		case "screenshot":
			return map[string]any{"result": map[string]any{
				"base64": "dGVzdA==", "mimeType": "image/jpeg", "width": 100, "height": 100,
			}}
		case "get_page_structure":
			return map[string]any{"result": map[string]any{"yaml": "heading: Test"}}
		case "get_interactive_map":
			return map[string]any{"result": []map[string]any{{"tag": "button"}}}
		}
		return map[string]any{"result": nil}
	}, func() {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{"question": "what is this?"},
			},
		}
		result, err := handleDescribe(context.Background(), req)
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		text := extractText(t, result)
		if text != "This is a test page with a button" {
			t.Errorf("text = %q", text)
		}
	})
}

func TestHandleDescribe_NoScreenshot(t *testing.T) {
	origHaiku := haiku
	haiku = nil
	defer func() { haiku = origHaiku }()

	withMockBrowser(t, func(msg map[string]any) map[string]any {
		action := msg["action"].(string)
		switch action {
		case "get_page_structure":
			return map[string]any{"result": map[string]any{"yaml": "heading: Test"}}
		case "get_interactive_map":
			return map[string]any{"result": []map[string]any{}}
		}
		return map[string]any{"result": nil}
	}, func() {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{"question": "what?", "includeScreenshot": false},
			},
		}
		result, err := handleDescribe(context.Background(), req)
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		// Should succeed even without screenshot
		text := extractText(t, result)
		if text == "" {
			t.Error("should return some content")
		}
	})
}

// --- handleRunTool ---

func TestHandleRunTool_Success(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	saveTool(CustomTool{
		Name: "add_one", Description: "adds 1", Code: "return params.x + 1",
		Params: `[{"name":"x","type":"number"}]`, CreatedAt: "2024-01-01T00:00:00Z",
	})

	withMockBrowser(t, func(msg map[string]any) map[string]any {
		return map[string]any{"result": map[string]any{"result": 43}}
	}, func() {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"name": "add_one",
					"args": map[string]any{"x": 42},
				},
			},
		}
		result, err := handleRunTool(context.Background(), req)
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		text := extractText(t, result)
		if !strings.Contains(text, "43") {
			t.Errorf("should contain result: %q", text)
		}
	})
}

func TestHandleRunTool_NotFound(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{"name": "nonexistent"},
		},
	}
	result, _ := handleRunTool(context.Background(), req)
	if !result.IsError {
		t.Error("expected error for nonexistent tool")
	}
	text := extractText(t, result)
	if !strings.Contains(text, "not found") {
		t.Errorf("should say not found: %q", text)
	}
}

func TestHandleRunTool_NoDB(t *testing.T) {
	origDB := db
	db = nil
	defer func() { db = origDB }()

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{"name": "x"},
		},
	}
	result, _ := handleRunTool(context.Background(), req)
	if !result.IsError {
		t.Error("expected error when db is nil")
	}
}

func TestHandleRunTool_WithSystemPrompt(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	// Force haiku-only backend so we exercise the legacy "Haiku
	// unavailable" fallback path; otherwise auto would try local AI
	// via the mock browser and produce a different banner.
	t.Setenv("TURBOWEB_AI_BACKEND", "haiku")
	origHaiku := haiku
	haiku = nil // Haiku nil → returns "[Haiku unavailable]"
	defer func() { haiku = origHaiku }()

	sp := "You are a field analyzer."
	saveTool(CustomTool{
		Name: "prompted", Description: "has prompt", Code: "return 'data'",
		Params: "[]", SystemPrompt: &sp, CreatedAt: "2024-01-01T00:00:00Z",
	})

	withMockBrowser(t, func(msg map[string]any) map[string]any {
		return map[string]any{"result": "raw data"}
	}, func() {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{"name": "prompted"},
			},
		}
		result, _ := handleRunTool(context.Background(), req)
		text := extractText(t, result)
		if !strings.Contains(text, "Haiku unavailable") {
			t.Errorf("should indicate Haiku unavailable: %q", text)
		}
	})
}

// --- JS tools ---

func TestHandleExecuteJS(t *testing.T) {
	withMockBrowser(t, func(msg map[string]any) map[string]any {
		return map[string]any{"result": map[string]any{"result": "My Page Title"}}
	}, func() {
		handler := passThrough("execute_js")
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{"code": "return document.title"},
			},
		}
		result, _ := handler(context.Background(), req)
		text := extractText(t, result)
		if !strings.Contains(text, "My Page Title") {
			t.Errorf("should contain page title: %q", text)
		}
	})
}

func TestHandleAdaptScript(t *testing.T) {
	withMockBrowser(t, echoHandler, func() {
		handler := passThrough("adapt_script")
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{"code": "console.log('hi')", "persist": true},
			},
		}
		result, _ := handler(context.Background(), req)
		text := extractText(t, result)
		if !strings.Contains(text, "adapt_script") {
			t.Errorf("should contain action: %q", text)
		}
	})
}

// --- Interaction tools ---

func TestInteractionTools(t *testing.T) {
	actions := []struct {
		action string
		args   map[string]any
	}{
		{"click", map[string]any{"selector": "#btn"}},
		{"click", map[string]any{"x": float64(100), "y": float64(200)}},
		{"type_text", map[string]any{"text": "hello", "selector": "#input"}},
		{"type_text", map[string]any{"text": "test", "clear": true, "pressEnter": true}},
		{"scroll", map[string]any{"direction": "down", "amount": float64(500)}},
		{"cdp_click", map[string]any{"x": float64(100), "y": float64(200)}},
		{"cdp_click", map[string]any{"x": float64(100), "y": float64(200), "shift": true}},
		{"cdp_type", map[string]any{"text": "hello"}},
		{"cdp_type", map[string]any{"text": "hello", "clear": true}},
		{"cdp_key", map[string]any{"key": "Enter"}},
		{"cdp_scroll", map[string]any{"deltaY": float64(300)}},
		{"cdp_scroll", map[string]any{"x": float64(100), "y": float64(200), "deltaX": float64(0), "deltaY": float64(-100)}},
	}

	for _, tt := range actions {
		t.Run(tt.action, func(t *testing.T) {
			withMockBrowser(t, echoHandler, func() {
				handler := passThrough(tt.action)
				req := mcp.CallToolRequest{
					Params: mcp.CallToolParams{Arguments: tt.args},
				}
				result, err := handler(context.Background(), req)
				if err != nil {
					t.Fatalf("error: %v", err)
				}
				text := extractText(t, result)
				if !strings.Contains(text, tt.action) {
					t.Errorf("response should contain action %q: %q", tt.action, text)
				}
			})
		})
	}
}

// --- Register functions ---

func TestRegisterAllTools(t *testing.T) {
	s := server.NewMCPServer("test", "1.0.0", server.WithToolCapabilities(false))
	registerAllTools(s)
	// No panic means success — all 26 tools registered without error
}

func TestRegisterBrowserTools(t *testing.T) {
	s := server.NewMCPServer("test", "1.0.0", server.WithToolCapabilities(false))
	registerBrowserTools(s)
}

func TestRegisterDomTools(t *testing.T) {
	s := server.NewMCPServer("test", "1.0.0", server.WithToolCapabilities(false))
	registerDomTools(s)
}

func TestRegisterInteractionTools(t *testing.T) {
	s := server.NewMCPServer("test", "1.0.0", server.WithToolCapabilities(false))
	registerInteractionTools(s)
}

func TestRegisterJsTools(t *testing.T) {
	s := server.NewMCPServer("test", "1.0.0", server.WithToolCapabilities(false))
	registerJsTools(s)
}

func TestRegisterCustomTools(t *testing.T) {
	s := server.NewMCPServer("test", "1.0.0", server.WithToolCapabilities(false))
	registerCustomTools(s)
}

// --- maybeAskWithSystem with working Haiku ---

func TestMaybeAskWithSystem_WithHaiku(t *testing.T) {
	srv := httptest.NewServer(mockAnthropicHandler("analyzed with custom prompt"))
	defer srv.Close()

	origHaiku := haiku
	haiku = newTestHaikuClient(srv.URL)
	haiku.httpClient.Transport = &redirectTransport{url: srv.URL}
	defer func() { haiku = origHaiku }()

	raw := json.RawMessage(`{"data":"test"}`)
	result, err := maybeAskWithSystem(raw, "analyze this", "custom system prompt", "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	text := extractText(t, result)
	if text != "analyzed with custom prompt" {
		t.Errorf("text = %q", text)
	}
}
