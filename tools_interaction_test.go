package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/mark3labs/mcp-go/mcp"
)

// prepareForUserClickHandler routes the two actions handlePrepareForUserClick
// emits: the `prepare_for_user_click` extension action (target resolution +
// banner) and the follow-up `screenshot`. found controls whether the handoff
// succeeds; capturedParams records the params forwarded to the extension.
func prepareForUserClickHandler(found bool, capturedParams *map[string]any) func(map[string]any) map[string]any {
	return func(msg map[string]any) map[string]any {
		action, _ := msg["action"].(string)
		params, _ := msg["params"].(map[string]any)
		switch action {
		case "prepare_for_user_click":
			if capturedParams != nil {
				*capturedParams = params
			}
			if !found {
				return map[string]any{"result": map[string]any{"found": false, "reasonDetail": "no_target"}}
			}
			return map[string]any{"result": map[string]any{
				"found":        true,
				"selector":     params["selector"],
				"label":        params["label"],
				"bbox":         map[string]any{"x": 10, "y": 20, "width": 96, "height": 40},
				"inViewport":   true,
				"occluded":     false,
				"overlayShown": true,
			}}
		case "screenshot":
			return map[string]any{"result": map[string]any{
				"base64": "dGVzdA==", "mimeType": "image/jpeg", "width": 1280, "height": 720,
			}}
		default:
			return map[string]any{"result": map[string]any{"action": action}}
		}
	}
}

// parsePrepareResult finds the JSON text block in a CallToolResult (it's the
// last block — index 1 when an image precedes it, index 0 when text-only).
func parsePrepareResult(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	if result == nil || len(result.Content) == 0 {
		t.Fatal("result has no content")
	}
	b, _ := json.Marshal(result.Content[len(result.Content)-1])
	var block struct {
		Text string `json:"text"`
	}
	json.Unmarshal(b, &block)
	var out map[string]any
	if err := json.Unmarshal([]byte(block.Text), &out); err != nil {
		t.Fatalf("parse result JSON: %v\nraw: %s", err, block.Text)
	}
	return out
}

func TestHandlePrepareForUserClick_SelectorResolves(t *testing.T) {
	var captured map[string]any
	withMockBrowser(t, prepareForUserClickHandler(true, &captured), func() {
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
			"selector": "button#allow",
			"hint":     "Click Allow to grant access — I can't approve OAuth for you.",
			"label":    "Allow",
			"reason":   "auth",
		}}}
		result, err := handlePrepareForUserClick(context.Background(), req)
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if result.IsError {
			t.Fatalf("unexpected error result: %s", extractText(t, result))
		}
		// Success → multi-content: image + text.
		if len(result.Content) != 2 {
			t.Fatalf("expected 2 content blocks (image + text), got %d", len(result.Content))
		}
		out := parsePrepareResult(t, result)
		if out["handoff"] != true {
			t.Errorf("handoff = %v, want true", out["handoff"])
		}
		if out["status"] != "awaiting_user" {
			t.Errorf("status = %v, want awaiting_user", out["status"])
		}
		if out["reason"] != "auth" {
			t.Errorf("reason = %v, want auth", out["reason"])
		}
		if out["screenshotTaken"] != true {
			t.Errorf("screenshotTaken = %v, want true", out["screenshotTaken"])
		}
		// The hint must be relayed verbatim as the instruction.
		if !strings.Contains(out["instruction"].(string), "Click Allow") {
			t.Errorf("instruction = %q, want it to carry the hint", out["instruction"])
		}
		// hint / label / reason must reach the extension intact.
		if captured["hint"] == nil || captured["label"] != "Allow" || captured["reason"] != "auth" {
			t.Errorf("forwarded params not intact: %#v", captured)
		}
	})
}

func TestHandlePrepareForUserClick_TargetNotFound(t *testing.T) {
	withMockBrowser(t, prepareForUserClickHandler(false, nil), func() {
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
			"selector": "button#missing",
			"hint":     "Click the confirm button.",
		}}}
		result, err := handlePrepareForUserClick(context.Background(), req)
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		// target_not_found → text-only, NO image.
		if len(result.Content) != 1 {
			t.Fatalf("expected 1 content block (text-only), got %d", len(result.Content))
		}
		out := parsePrepareResult(t, result)
		if out["handoff"] != false {
			t.Errorf("handoff = %v, want false", out["handoff"])
		}
		if out["status"] != "target_not_found" {
			t.Errorf("status = %v, want target_not_found", out["status"])
		}
	})
}

func TestHandlePrepareForUserClick_CoordinateMode(t *testing.T) {
	var captured map[string]any
	withMockBrowser(t, prepareForUserClickHandler(true, &captured), func() {
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
			"x":    float64(300),
			"y":    float64(450),
			"hint": "Click the captcha checkbox.",
		}}}
		result, err := handlePrepareForUserClick(context.Background(), req)
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if result.IsError {
			t.Fatalf("unexpected error: %s", extractText(t, result))
		}
		out := parsePrepareResult(t, result)
		if out["status"] != "awaiting_user" {
			t.Errorf("status = %v, want awaiting_user", out["status"])
		}
		// reason defaults to "other" when omitted.
		if out["reason"] != "other" {
			t.Errorf("reason = %v, want other (default)", out["reason"])
		}
		if captured["x"] != float64(300) || captured["y"] != float64(450) {
			t.Errorf("coords not forwarded: %#v", captured)
		}
	})
}

func TestHandlePrepareForUserClick_MissingHint(t *testing.T) {
	withMockBrowser(t, prepareForUserClickHandler(true, nil), func() {
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
			"selector": "button#allow",
		}}}
		result, _ := handlePrepareForUserClick(context.Background(), req)
		if !result.IsError {
			t.Error("expected error result when hint is missing")
		}
		if !strings.Contains(extractText(t, result), "hint is required") {
			t.Errorf("error should mention the missing hint: %q", extractText(t, result))
		}
	})
}

func TestHandlePrepareForUserClick_NoTarget(t *testing.T) {
	withMockBrowser(t, prepareForUserClickHandler(true, nil), func() {
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
			"hint": "Click something.",
		}}}
		result, _ := handlePrepareForUserClick(context.Background(), req)
		if !result.IsError {
			t.Error("expected error result when neither selector nor x,y is given")
		}
	})
}

func TestHandlePrepareForUserClick_BadReason(t *testing.T) {
	withMockBrowser(t, prepareForUserClickHandler(true, nil), func() {
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
			"selector": "button#allow",
			"hint":     "Click Allow.",
			"reason":   "i_give_up",
		}}}
		result, _ := handlePrepareForUserClick(context.Background(), req)
		if !result.IsError {
			t.Error("expected error result for an unknown reason value")
		}
	})
}

func TestHandlePrepareForUserClick_NoBrowser(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"selector": "button#allow",
		"hint":     "Click Allow.",
	}}}
	result, _ := handlePrepareForUserClick(context.Background(), req)
	if !result.IsError {
		t.Error("expected error result with no browser connected")
	}
}
