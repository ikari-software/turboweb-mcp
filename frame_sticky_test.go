package main

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestStickyFrameSetGetClear(t *testing.T) {
	setStickyFrame("")
	defer setStickyFrame("")

	if getStickyFrame() != "" {
		t.Fatal("sticky frame should start empty")
	}
	setStickyFrame("  #top > #inner  ") // trims
	if got := getStickyFrame(); got != "#top > #inner" {
		t.Errorf("sticky = %q, want trimmed %q", got, "#top > #inner")
	}
	setStickyFrame("") // frameSetToRoot
	if getStickyFrame() != "" {
		t.Error("empty set should clear the sticky frame")
	}
}

// injectStickyFrame must fill in `frame` only when the call omits it, and never
// override a per-call frame.
func TestInjectStickyFrame(t *testing.T) {
	setStickyFrame("#sticky")
	defer setStickyFrame("")

	seen := ""
	h := injectStickyFrame(func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		seen = toString(req.GetArguments()["frame"])
		return textResult("ok")
	})

	// (1) No frame → sticky injected.
	seen = ""
	h(context.Background(), reqWith(map[string]any{"x": 1}))
	if seen != "#sticky" {
		t.Errorf("missing frame should inherit sticky, got %q", seen)
	}

	// (2) Empty frame → treated as missing → sticky injected.
	seen = ""
	h(context.Background(), reqWith(map[string]any{"frame": "  "}))
	if seen != "#sticky" {
		t.Errorf("blank frame should inherit sticky, got %q", seen)
	}

	// (3) Explicit per-call frame → wins over sticky.
	seen = ""
	h(context.Background(), reqWith(map[string]any{"frame": "#explicit"}))
	if seen != "#explicit" {
		t.Errorf("per-call frame should win, got %q", seen)
	}

	// (4) No sticky set → nothing injected.
	setStickyFrame("")
	seen = "unset"
	h(context.Background(), reqWith(map[string]any{"x": 1}))
	if seen != "" {
		t.Errorf("no sticky should leave frame empty, got %q", seen)
	}
}

func TestHandleFrameSetAndContext(t *testing.T) {
	setStickyFrame("")
	defer setStickyFrame("")

	// frame_set with a frame → sticky reported by frame_context.
	if _, err := handleFrameSet(context.Background(), reqWith(map[string]any{"frame": "#f > #g"})); err != nil {
		t.Fatalf("frame_set: %v", err)
	}
	res, _ := handleFrameContext(context.Background(), mcp.CallToolRequest{})
	// textResult pretty-prints and HTML-escapes (`>` → `>`), so match on the
	// stable parts rather than the exact framePath string.
	if txt := extractText(t, res); !strings.Contains(txt, "#f") || !strings.Contains(txt, "#g") || !strings.Contains(txt, `"scope": "frame"`) {
		t.Errorf("frame_context should report the sticky frame, got: %q", txt)
	}

	// frame_set with no frame → cleared, frame_context reports top.
	if _, err := handleFrameSet(context.Background(), reqWith(map[string]any{})); err != nil {
		t.Fatalf("frame_set clear: %v", err)
	}
	res, _ = handleFrameContext(context.Background(), mcp.CallToolRequest{})
	if txt := extractText(t, res); !strings.Contains(txt, `"scope": "top"`) {
		t.Errorf("frame_context after clear should report top, got: %q", txt)
	}
}
