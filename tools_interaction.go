package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerInteractionTools(s *server.MCPServer) {
	// --- click ---
	addTool(s,
		mcp.NewTool("click",
			mcp.WithDescription("Click an element by CSS selector OR x,y coordinates. Dispatches mousedown, mouseup, click events."),
			mcp.WithString("selector", mcp.Description("CSS selector of element to click")),
			mcp.WithNumber("x", mcp.Description("X coordinate to click")),
			mcp.WithNumber("y", mcp.Description("Y coordinate to click")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		passThrough("click"),
	)

	// --- type_text ---
	addTool(s,
		mcp.NewTool("type_text",
			mcp.WithDescription("Type text into an element. Uses insertText command so it works with React, Vue, and other frameworks. Can clear existing text first."),
			mcp.WithString("text", mcp.Required(), mcp.Description("Text to type")),
			mcp.WithString("selector", mcp.Description("CSS selector (omit for focused element)")),
			mcp.WithBoolean("clear", mcp.Description("Clear existing text first (default false)")),
			mcp.WithBoolean("pressEnter", mcp.Description("Press Enter after typing (default false)")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		passThrough("type_text"),
	)

	// --- scroll ---
	addTool(s,
		mcp.NewTool("scroll",
			mcp.WithDescription("Scroll the page or a specific element. Use direction (up/down/left/right) or pixel offsets."),
			mcp.WithString("direction", mcp.Description("Scroll direction")),
			mcp.WithNumber("amount", mcp.Description("Pixels to scroll (default ~80% viewport)")),
			mcp.WithString("selector", mcp.Description("Scroll within this element")),
			mcp.WithNumber("x", mcp.Description("Horizontal pixel offset")),
			mcp.WithNumber("y", mcp.Description("Vertical pixel offset")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		passThrough("scroll"),
	)

	// --- cdp_click (real browser input via BiDi/CDP) ---
	addTool(s,
		mcp.NewTool("cdp_click",
			mcp.WithDescription(
				"Click using real browser input (trusted events, bypasses MUI portals, dropdowns, "+
					"and isTrusted-guarded handlers). Provide ONE of:\n"+
					"  • selector — the click lands on the element's centre, resolved via "+
					"getBoundingClientRect on the page side. Preferred when you know the element.\n"+
					"  • x,y — explicit viewport coordinates. Use when targeting a canvas, "+
					"an OS-level chrome region, or anywhere a selector doesn't apply.\n"+
					"If both are given, selector wins. Errors if neither is provided or the "+
					"selector matches nothing / has zero-size bbox.",
			),
			mcp.WithString("selector", mcp.Description("CSS selector. Click lands on the element's centre.")),
			mcp.WithNumber("x", mcp.Description("X viewport coordinate (required if no selector)")),
			mcp.WithNumber("y", mcp.Description("Y viewport coordinate (required if no selector)")),
			mcp.WithBoolean("shift", mcp.Description("Hold Shift key during click (for multi-select)")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		bidiOrFallback("cdp_click", handleBiDiClick),
	)

	// --- cdp_type (real keyboard input via BiDi/CDP) ---
	addTool(s,
		mcp.NewTool("cdp_type",
			mcp.WithDescription(
				"Type text using real keyboard events. Works with React, MUI, and any framework. "+
					"By default types into whatever the page has focused — pass selector to focus "+
					"a specific element first (calls element.focus() on the page and verifies the "+
					"activeElement actually moved). Use clear=true to select-all and delete before typing.",
			),
			mcp.WithString("text", mcp.Required(), mcp.Description("Text to type character by character")),
			mcp.WithString("selector", mcp.Description("Optional CSS selector — focus this element before typing")),
			mcp.WithBoolean("clear", mcp.Description("Select-all + delete before typing (default false)")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		bidiOrFallback("cdp_type", handleBiDiType),
	)

	// --- cdp_key (now via BiDi input.performActions) ---
	addTool(s,
		mcp.NewTool("cdp_key",
			mcp.WithDescription("Press a single key via real browser input. Supports: Enter, Escape, ArrowDown, ArrowUp, Backspace, Tab."),
			mcp.WithString("key", mcp.Required(), mcp.Description("Key name: Enter, Escape, ArrowDown, ArrowUp, Backspace, Tab")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		bidiOrFallback("cdp_key", handleBiDiKey),
	)

	// --- cdp_scroll (real wheel input via BiDi/CDP) ---
	addTool(s,
		mcp.NewTool("cdp_scroll",
			mcp.WithDescription(
				"Scroll using real browser wheel events. Wheel events dispatch AT a point and "+
					"bubble up to the nearest scrollable ancestor, so this is how you scroll inner "+
					"containers (dropdowns, virtualised lists, infinite feeds) that window.scrollBy "+
					"can't reach. Provide ONE of:\n"+
					"  • selector — wheel dispatched at the element's centre.\n"+
					"  • x,y — explicit viewport position (default 600,400).\n"+
					"deltaY (negative=up, positive=down) controls the scroll amount. If both selector "+
					"and x,y are given, selector wins.",
			),
			mcp.WithString("selector", mcp.Description("CSS selector — wheel events fire at the element's centre")),
			mcp.WithNumber("x", mcp.Description("X coordinate for scroll position (default 600)")),
			mcp.WithNumber("y", mcp.Description("Y coordinate for scroll position (default 400)")),
			mcp.WithNumber("deltaX", mcp.Description("Horizontal scroll amount")),
			mcp.WithNumber("deltaY", mcp.Description("Vertical scroll amount (negative=up, positive=down)")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		bidiOrFallback("cdp_scroll", handleBiDiScroll),
	)

	// --- prepare_for_user_click (honest handoff to the watching human) ---
	addTool(s,
		mcp.NewTool("prepare_for_user_click",
			mcp.WithDescription(
				"Hand the final click off to the human watching the tab. Scrolls the "+
					"target into view, draws a persistent highlight + handoff banner, "+
					"takes a screenshot, and returns status:\"awaiting_user\" immediately "+
					"— it does NOT click and does NOT block.\n\n"+
					"Use this ONLY when human action is genuinely required:\n"+
					"  • auth — an OAuth consent, a 2FA prompt, a password re-entry.\n"+
					"  • confirmation — an irreversible button (\"Delete account\", "+
					"\"Transfer funds\", \"Submit order\") a human should own.\n"+
					"  • os_dialog — a native file picker / print dialog / permission "+
					"bubble outside the page DOM.\n"+
					"  • untrusted_input — a control that rejects synthetic input even "+
					"via cdp_* (some payment widgets, captchas, DRM players).\n\n"+
					"This is NOT a fallback for a selector you couldn't resolve or a "+
					"`click` that was merely awkward — use `click` / `cdp_click` for "+
					"those. After calling this tool, end your turn and wait for the "+
					"human to act; then verify the page state before continuing.\n\n"+
					"Provide ONE of selector / x,y. `hint` is required — it is the "+
					"instruction the human reads.",
			),
			mcp.WithString("selector", mcp.Description("CSS selector of the control the human should click. Resolved to a bounding box and scrolled into view. Preferred.")),
			mcp.WithNumber("x", mcp.Description("X viewport coordinate, for canvas / non-DOM targets where a selector doesn't apply (lower fidelity — no scroll-into-view)")),
			mcp.WithNumber("y", mcp.Description("Y viewport coordinate (pair with x)")),
			mcp.WithString("hint", mcp.Required(), mcp.Description("Required. Human-readable instruction shown in the on-page banner: what to click and why the agent is handing off. e.g. \"Click Allow to grant calendar access — I can't approve OAuth scopes for you.\"")),
			mcp.WithString("reason", mcp.Description("Why the handoff is necessary. One of: auth, confirmation, os_dialog, untrusted_input, other. Drives banner copy/iconography; defaults to other."),
				mcp.Enum("auth", "confirmation", "os_dialog", "untrusted_input", "other")),
			mcp.WithString("label", mcp.Description("Short name for the control (\"the Allow button\"), used as the banner's bold first line when the full hint is too long for it.")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		handlePrepareForUserClick,
	)
}

// validHandoffReasons gates the `reason` enum server-side so a bogus value
// can't reach the extension's banner copy switch.
var validHandoffReasons = map[string]bool{
	"auth": true, "confirmation": true, "os_dialog": true,
	"untrusted_input": true, "other": true,
}

// handlePrepareForUserClick runs the instruct-and-stop handoff. It forwards
// the target + hint to the extension (which scrolls into view, paints the
// persistent highlight + banner, and reports the resolved bbox), captures a
// screenshot so the agent sees what the human sees, and assembles a
// multi-content result (image + JSON). It returns immediately — the human is
// expected to act after the agent's turn ends, so this never blocks on them.
func handlePrepareForUserClick(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()

	hint := toString(args["hint"])
	if hint == "" {
		return mcp.NewToolResultError("prepare_for_user_click: hint is required — it is the instruction the human reads"), nil
	}
	sel := toString(args["selector"])
	_, hasX := args["x"]
	if sel == "" && !hasX {
		return mcp.NewToolResultError("prepare_for_user_click: provide either selector or x,y coordinates"), nil
	}

	reason := toString(args["reason"])
	if reason == "" {
		reason = "other"
	} else if !validHandoffReasons[reason] {
		return mcp.NewToolResultError(fmt.Sprintf("prepare_for_user_click: unknown reason %q (want auth, confirmation, os_dialog, untrusted_input, or other)", reason)), nil
	}

	// Forward the handoff to the extension. The content script does the
	// scroll-into-view, paints the banner + persistent highlight, and reports
	// back the resolved bbox plus inViewport/occluded flags.
	params := rawArgs(args)
	params["reason"] = reason
	raw, err := send("prepare_for_user_click", params)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	var prep struct {
		Found        bool            `json:"found"`
		Selector     string          `json:"selector"`
		Label        string          `json:"label"`
		Bbox         json.RawMessage `json:"bbox"`
		InViewport   bool            `json:"inViewport"`
		Occluded     bool            `json:"occluded"`
		Ambiguous    bool            `json:"ambiguous"`
		OverlayShown bool            `json:"overlayShown"`
		ReasonDetail string          `json:"reasonDetail"`
	}
	if err := json.Unmarshal(raw, &prep); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("prepare_for_user_click: failed to parse prepare result: %v", err)), nil
	}

	target := map[string]any{"found": prep.Found}
	if prep.Selector != "" {
		target["selector"] = prep.Selector
	}
	if prep.Label != "" {
		target["label"] = prep.Label
	}
	if len(prep.Bbox) > 0 {
		target["bbox"] = prep.Bbox
	}
	if prep.Ambiguous {
		target["ambiguous"] = true
	}

	// Target not found: the handoff did NOT happen. Return text-only (no
	// image) so the agent knows to re-resolve the target and retry.
	if !prep.Found {
		out := map[string]any{
			"handoff": false,
			"status":  "target_not_found",
			"target":  target,
			"reason":  reason,
		}
		if prep.ReasonDetail != "" {
			out["reasonDetail"] = prep.ReasonDetail
		}
		return textResult(out)
	}

	target["inViewport"] = prep.InViewport
	target["occluded"] = prep.Occluded

	out := map[string]any{
		"handoff":         true,
		"status":          "awaiting_user",
		"target":          target,
		"instruction":     hint,
		"reason":          reason,
		"overlayShown":    prep.OverlayShown,
		"screenshotTaken": false,
	}

	// Capture a screenshot AFTER the banner + highlight have painted so the
	// returned image shows the human exactly what the page now looks like.
	// Reuse the extension `screenshot` action (same path handleScreenshot's
	// fallback uses); on failure we still hand off — the instruction text is
	// the real contract, the image is an aid.
	shotArgs := map[string]any{}
	if tid, ok := args["tabId"]; ok {
		shotArgs["tabId"] = tid
	}
	shotRaw, shotErr := send("screenshot", shotArgs)
	if shotErr == nil {
		var shot struct {
			Base64   string `json:"base64"`
			MimeType string `json:"mimeType"`
		}
		if json.Unmarshal(shotRaw, &shot) == nil && shot.Base64 != "" {
			out["screenshotTaken"] = true
			mime := shot.MimeType
			if mime == "" {
				mime = "image/jpeg"
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.NewImageContent(shot.Base64, mime),
					mcp.NewTextContent(toJSON(out)),
				},
			}, nil
		}
	}

	// No screenshot — still an honest handoff, text-only.
	return textResult(out)
}

// passThrough creates a handler that forwards the action and all args to the extension.
func passThrough(action string) server.ToolHandlerFunc {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		raw, err := send(action, rawArgs(args))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(string(raw)), nil
	}
}

// --- BiDi real-input handlers ---

func handleBiDiClick(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	ctxID, err := resolveContext(args["tabId"])
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	var x, y float64
	sel := toString(args["selector"])
	if sel != "" {
		cx, cy, err := resolveSelectorCenter(ctx, ctxID, sel)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		x, y = cx, cy
	} else if _, hasX := args["x"]; hasX {
		x = toFloat(args["x"])
		y = toFloat(args["y"])
	} else {
		return mcp.NewToolResultError("cdp_click: provide either selector or x,y coordinates"), nil
	}
	if err := bidiClick(ctx, ctxID, x, y, "left"); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	out := map[string]any{"clicked": true, "x": x, "y": y}
	if sel != "" {
		out["selector"] = sel
	}
	return textResult(out)
}

func handleBiDiType(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	ctxID, err := resolveContext(args["tabId"])
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	sel := toString(args["selector"])
	if sel != "" {
		if err := focusSelector(ctx, ctxID, sel); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
	}
	text := toString(args["text"])
	if toBool(args["clear"]) {
		if err := bidiSelectAllClear(ctx, ctxID); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
	}
	if err := bidiType(ctx, ctxID, text); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	out := map[string]any{"typed": len(text)}
	if sel != "" {
		out["selector"] = sel
	}
	return textResult(out)
}

func handleBiDiKey(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	ctxID, err := resolveContext(args["tabId"])
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	key := toString(args["key"])
	if err := bidiKey(ctx, ctxID, key); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return textResult(map[string]any{"pressed": key})
}

func handleBiDiScroll(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	ctxID, err := resolveContext(args["tabId"])
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	var x, y float64
	sel := toString(args["selector"])
	if sel != "" {
		cx, cy, err := resolveSelectorCenter(ctx, ctxID, sel)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		x, y = cx, cy
	} else {
		x = toFloat(args["x"])
		y = toFloat(args["y"])
		if x == 0 {
			x = 600
		}
		if y == 0 {
			y = 400
		}
	}
	deltaX := toFloat(args["deltaX"])
	deltaY := toFloat(args["deltaY"])
	if err := bidiScroll(ctx, ctxID, x, y, deltaX, deltaY); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	out := map[string]any{"scrolled": true, "x": x, "y": y}
	if sel != "" {
		out["selector"] = sel
	}
	return textResult(out)
}
