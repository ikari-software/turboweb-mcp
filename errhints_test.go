package main

import (
	"context"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestHintFor(t *testing.T) {
	for _, c := range []struct{ msg, want string }{
		{"timeout after 30000ms: extract_text", "wake_tab"},
		{"timeout after 30000ms: click (via relay)", "wake_tab"},
		{"timeout after 30000ms: wake_tab", "page_reload"},
		{"timeout after 120000ms: move_tabs (via relay)", "before retrying"},
		{"timeout after 30000ms: close_tabs", "before retrying"},
		{"timeout after 30000ms: open_tab", "duplicate tab"},
		{"relay write error: broken pipe", "connection_status"},
		{"No tab with id: 7", "list_tabs"},
		{"frame \"#x\" matched no <iframe>", "list_frames"},
		{"Error: tabs.captureVisibleTab failed", "extract_text"},
		{"Not connected to daemon. Waiting for reconnect.", "connection_status"},
		{"Tab 42 not found in any connected browser", "list_tabs"},
		{"BiDi connection closed", "content-script tools"},
		{"No element matches selector: #go", "get_interactive_map"},
		{"Refused to evaluate: violates Content Security Policy", "page_capabilities"},
		{"browser launched but no connection established within 10s", "user's own browser"},
		{"No browser extension connected to turboweb daemon on port 18321", "Do not switch to another browser"},
		{"chrome.debugger is not available in this browser. On Firefox / Zen, trusted-input tools require a BiDi connection.", "content-script tools"},
		// Already explained by the extension, or nothing specific to add.
		{"An unexpected error occurred. What to do: call wake_tab", ""},
		{"files: empty array", ""},
		{"cspan is not a selector", ""},
	} {
		got := hintFor(c.msg)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("hintFor(%q) = %q; want it to contain %q", c.msg, got, c.want)
		}
	}
}

func TestNoBrowserError_SaysToStayInTheUsersBrowser(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	_, err := sendDirect("list_tabs", nil, 1000)
	if err == nil {
		t.Fatal("want an error with no browser connected")
	}
	// The advice comes from the hint table, so it is written in one place.
	hint := hintFor(err.Error())
	for _, want := range []string{"connection_status", "Do not switch to another browser"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint should contain %q, got: %s", want, hint)
		}
	}
}

func TestWithErrorHints(t *testing.T) {
	fail := func(msg string) *mcp.CallToolResult {
		h := withErrorHints(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultError(msg), nil
		})
		res, err := h(context.Background(), reqWith(nil))
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := fail("timeout after 30000ms: click")
	if text := extractText(t, res); !res.IsError || !strings.HasPrefix(text, "timeout after 30000ms: click. "+hintMarker) {
		t.Errorf("want the original error followed by advice, got %q", text)
	}
	if text := extractText(t, fail("files: empty array")); text != "files: empty array" {
		t.Errorf("an error with no hint should pass through unchanged, got %q", text)
	}

	ok := withErrorHints(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("timeout after 5ms is what the page says"), nil
	})
	res, _ = ok(context.Background(), reqWith(nil))
	if text := extractText(t, res); strings.Contains(text, hintMarker) {
		t.Errorf("a successful result must not be annotated, got %q", text)
	}
}
