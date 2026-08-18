package main

import (
	"context"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Sticky frame context (frame_set / frame_context).
//
// Mirrors puppeteer's frameSet / frameSetToRoot: set a default frame once and
// every subsequent frame-aware call (DOM reads, click/type_text/scroll, and the
// trusted cdp_* tools) is scoped to it until reset — no repeating the `frame`
// arg across a multi-step flow in one frame. A per-call `frame` always wins; an
// empty/absent `frame` inherits the sticky default. Reset with frame_set (no
// frame) / frame_context report.

var (
	stickyFrameMu sync.RWMutex
	stickyFrame   string // "" == no sticky frame (calls default to the top frame)
)

func setStickyFrame(f string) {
	stickyFrameMu.Lock()
	stickyFrame = strings.TrimSpace(f)
	stickyFrameMu.Unlock()
}

func getStickyFrame() string {
	stickyFrameMu.RLock()
	defer stickyFrameMu.RUnlock()
	return stickyFrame
}

// injectStickyFrame wraps a frame-aware tool handler so a call that doesn't set
// its own `frame` inherits the sticky default. addTool applies this to every
// tool that declares a `frame` property, so both the DOM tools and the cdp_*
// tools honor frame_set with no per-tool wiring. A non-empty per-call `frame`
// is left untouched (per-call override).
func injectStickyFrame(h server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if sticky := getStickyFrame(); sticky != "" {
			args := req.GetArguments()
			if args != nil && strings.TrimSpace(toString(args["frame"])) == "" {
				args["frame"] = sticky
			}
		}
		return h(ctx, req)
	}
}

// registerFrameTools registers the sticky-frame control tools.
func registerFrameTools(s *server.MCPServer) {
	// --- frame_set ---
	addTool(s,
		mcp.NewTool("frame_set",
			mcp.WithDescription("Set a sticky default frame: every subsequent frame-aware call "+
				"(extract_text, find_text, query_elements, click, type_text, fill_input, scroll, "+
				"cdp_*, …) is scoped to this frame until you change or clear it — so you don't repeat "+
				"the `frame` arg across a multi-step flow in one frame. A per-call `frame` still "+
				"overrides. Pass a framePath (\">\"-separated CSS selectors, e.g. \"#top > #inner\", "+
				"discover with list_frames). Omit `frame` (or pass \"\") to clear it and go back to the "+
				"top frame (frameSetToRoot)."),
			mcp.WithString("frame", mcp.Description("framePath to make sticky; omit or \"\" to clear")),
		),
		handleFrameSet,
	)

	// --- frame_context ---
	addTool(s,
		mcp.NewTool("frame_context",
			mcp.WithDescription("Report the current sticky default frame set by frame_set (or none). "+
				"Read-only."),
		),
		handleFrameContext,
	)
}

func handleFrameSet(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	frame := strings.TrimSpace(toString(req.GetArguments()["frame"]))
	setStickyFrame(frame)
	if frame == "" {
		return textResult(map[string]any{"frame": nil, "cleared": true})
	}
	return textResult(map[string]any{"frame": frame, "sticky": true})
}

func handleFrameContext(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	frame := getStickyFrame()
	if frame == "" {
		return textResult(map[string]any{"frame": nil, "scope": "top"})
	}
	return textResult(map[string]any{"frame": frame, "scope": "frame"})
}
