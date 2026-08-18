package main

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// reqWith builds a CallToolRequest carrying the given arguments.
func reqWith(args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}}
}

// --- bidiOrFallback frame guard (46a) ---
//
// cross-origin `frame` targeting only works on the BiDi path. Without BiDi the
// extension fallback would silently act on the TOP frame, so the guard must
// refuse cdp_*+frame rather than click/type in the wrong place.

func TestBidiOrFallback_FrameGuardRefusesWithoutBiDi(t *testing.T) {
	setBiDi(nil)
	ranBiDi := false
	h := bidiOrFallback("cdp_click", func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ranBiDi = true
		return textResult("bidi")
	})
	res, err := h(context.Background(), reqWith(map[string]any{
		"frame": "#cross", "x": float64(10), "y": float64(20),
	}))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ranBiDi {
		t.Fatal("BiDi handler must not run when BiDi is disconnected")
	}
	if !res.IsError {
		t.Fatal("expected an error result for cdp_* + frame without BiDi")
	}
	if txt := extractText(t, res); !strings.Contains(txt, "requires WebDriver BiDi") {
		t.Errorf("error should explain the BiDi requirement, got: %q", txt)
	}
}

func TestBidiOrFallback_RoutesToBiDiWhenConnected(t *testing.T) {
	setBiDi(&BiDiClient{})
	defer setBiDi(nil)
	ranBiDi := false
	h := bidiOrFallback("cdp_click", func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ranBiDi = true
		return textResult(map[string]any{"ok": true})
	})
	// A frame arg is allowed on the BiDi path — the guard must not fire.
	if _, err := h(context.Background(), reqWith(map[string]any{"frame": "#cross"})); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !ranBiDi {
		t.Fatal("BiDi handler should run when BiDi is connected")
	}
}

func TestBidiOrFallback_FallsBackToExtensionWhenNoFrame(t *testing.T) {
	setBiDi(nil)
	ranBiDi := false
	withMockBrowser(t, echoHandler, func() {
		h := bidiOrFallback("cdp_click", func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			ranBiDi = true
			return textResult("bidi")
		})
		res, err := h(context.Background(), reqWith(map[string]any{"x": float64(1), "y": float64(2)}))
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if ranBiDi {
			t.Fatal("BiDi handler must not run without BiDi")
		}
		// echoHandler echoes the action it received → proves passthrough to the
		// extension for the non-frame, no-BiDi case.
		if txt := extractText(t, res); !strings.Contains(txt, "cdp_click") {
			t.Errorf("expected passthrough to extension action cdp_click, got: %q", txt)
		}
	})
}

// --- handleNavigate routing (46a): frame+fallback vs no-frame ---
//
// (frame+BiDi is exercised only against a live BiDi session — resolveFrameContext
// needs a real socket — so it is left to integration, matching the rest of the
// suite which does not mock the BiDi transport.)

func TestHandleNavigate_NoFrameRoutesToNavigate(t *testing.T) {
	setBiDi(nil)
	withMockBrowser(t, echoHandler, func() {
		res, err := handleNavigate(context.Background(), reqWith(map[string]any{
			"url": "https://example.com/",
		}))
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if txt := extractText(t, res); !strings.Contains(txt, `"action":"navigate"`) {
			t.Errorf("no-frame navigate should route the `navigate` action, got: %q", txt)
		}
	})
}

func TestHandleNavigate_FrameFallbackRoutesToNavigateFrame(t *testing.T) {
	setBiDi(nil) // no BiDi → extension fallback (content script sets iframe.src)
	withMockBrowser(t, echoHandler, func() {
		res, err := handleNavigate(context.Background(), reqWith(map[string]any{
			"frame": "#top_frame", "url": "https://example.com/inner",
		}))
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		txt := extractText(t, res)
		if !strings.Contains(txt, "navigate_frame") {
			t.Errorf("frame + no-BiDi should route the `navigate_frame` action, got: %q", txt)
		}
		if !strings.Contains(txt, "#top_frame") {
			t.Errorf("the frame selector should be forwarded to the extension, got: %q", txt)
		}
	})
}
