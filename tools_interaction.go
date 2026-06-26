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
			mcp.WithDescription(
				"Click an element. Prefer selector over coordinates — selectors are "+
					"stable across scrolls and reflows. Use x,y only when a selector "+
					"isn't available (canvas, dynamic overlay, etc.).\n\n"+
					"CSS selector tips:\n"+
					"  • button text:   button:has(> span:contains(\"Submit\")) or button[aria-label=\"…\"]\n"+
					"  • input by name: input[name=\"fieldname\"] — brackets in quoted values are valid CSS\n"+
					"  • input by placeholder: input[placeholder=\"Email address\"]\n"+
					"  • nth of type:   li:nth-of-type(3)\n\n"+
					"Coordinates (x, y) are viewport-relative — same system as page_yaml positions. "+
					"Elements scrolled off-screen are not clickable at their reported coordinates; "+
					"scroll first, then click.\n\n"+
					"Dispatches full pointer+mouse event sequence and focuses the nearest focusable "+
					"ancestor. Use cdp_click for isTrusted-guarded handlers (MUI dropdowns, etc.).",
			),
			mcp.WithString("selector", mcp.Description("CSS selector of element to click. Preferred over x,y.")),
			mcp.WithNumber("x", mcp.Description("X viewport coordinate (use when no selector applies)")),
			mcp.WithNumber("y", mcp.Description("Y viewport coordinate (pair with x)")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			frameOpt(),
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
			frameOpt(),
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
			frameOpt(),
		),
		passThrough("scroll"),
	)

	// --- cdp_click (real browser input via BiDi/CDP) ---
	addTool(s,
		mcp.NewTool("cdp_click",
			mcp.WithDescription(
				"Click using real browser input (trusted events, bypasses MUI portals, dropdowns, "+
					"and isTrusted-guarded handlers). Prefer selector over x,y — selectors are stable "+
					"across scroll/reflow, and when combined with `frame` they are the ONLY reliable "+
					"way to target inside a cross-origin frame (coordinates don't translate across the "+
					"origin boundary). Provide ONE of:\n"+
					"  • selector — the click lands on the element's centre, resolved via "+
					"getBoundingClientRect on the page side. Preferred.\n"+
					"  • x,y — explicit viewport coordinates. Use only when targeting a canvas, "+
					"an OS-level chrome region, or anywhere a selector doesn't apply.\n"+
					"If both are given, selector wins. Errors if neither is provided or the "+
					"selector matches nothing / has zero-size bbox.\n\n"+
					"clickCount=2 double-clicks (selects a word); clickCount=3 triple-clicks "+
					"(selects all text in an input/line) — the trusted way to select a field's "+
					"contents before replacing them.",
			),
			mcp.WithString("selector", mcp.Description("CSS selector. Click lands on the element's centre.")),
			mcp.WithNumber("x", mcp.Description("X viewport coordinate (required if no selector)")),
			mcp.WithNumber("y", mcp.Description("Y viewport coordinate (required if no selector)")),
			mcp.WithBoolean("shift", mcp.Description("Hold Shift key during click (for multi-select)")),
			mcp.WithNumber("clickCount", mcp.Description("1=single (default), 2=double-click (select word), 3=triple-click (select all field text)")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			frameOpt(),
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
			mcp.WithBoolean("clear", mcp.Description("Select-all + delete before typing — replaces the existing value (default false). Works on every backend.")),
			mcp.WithNumber("wpm", mcp.Description(
				"Typing speed in words/min. Keystrokes are dispatched at a human cadence "+
					"(jitter + longer pauses after spaces/punctuation) — defaults to 110. "+
					"Set 0 for instant machine-speed typing (no inter-key delay).")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			frameOpt(),
		),
		bidiOrFallback("cdp_type", handleBiDiType),
	)

	// --- cdp_key (real browser input via BiDi/CDP) ---
	addTool(s,
		mcp.NewTool("cdp_key",
			mcp.WithDescription(
				"Press a single key via real browser input, optionally holding modifiers "+
					"(chords). Editing keys perform their native action — Backspace/Delete "+
					"delete, Enter submits, arrows/Home/End/PageUp/PageDown move the caret.\n\n"+
					"modifiers lets you send chords like select-all (Meta+a on macOS, "+
					"Control+a elsewhere), Shift+Tab, or Control+ArrowRight. To clear a field "+
					"you can also just use cdp_type clear=true.",
			),
			mcp.WithString("key", mcp.Required(), mcp.Description(
				"Key name (Enter, Escape, Tab, Backspace, Delete, Home, End, PageUp, "+
					"PageDown, ArrowUp/Down/Left/Right, F1-F12) or a single character (e.g. \"a\").")),
			mcp.WithArray("modifiers", mcp.Description("Modifier keys held during the press: any of Meta, Control, Alt, Shift."),
				mcp.Items(map[string]any{"type": "string", "enum": []string{"Meta", "Control", "Alt", "Shift"}})),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			frameOpt(),
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
			frameOpt(),
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

	// --- fill_input ---
	addTool(s,
		mcp.NewTool("fill_input",
			mcp.WithDescription(
				"Fill a form field (input, textarea, or select) with a value. "+
					"React-compatible: uses the native prototype value setter so React's "+
					"change-tracker sees a delta, then dispatches input+change events.\n\n"+
					"Automatically prefers the visible element when a selector matches "+
					"multiple elements (e.g. two inputs sharing the same name= attribute). "+
					"Returns {filled, value, id} and a note when hidden duplicates were skipped.\n\n"+
					"Use this instead of type_text for <input>/<textarea>/<select>. "+
					"For contenteditable rich-text editors (CKEditor, Quill, ProseMirror), "+
					"use type_text or execute_js with the editor's own API.",
			),
			mcp.WithString("selector", mcp.Required(), mcp.Description(
				"CSS selector for the field. Prefer ID selectors (#field-id) when available. "+
					"input[name=\"fieldname\"] works but may match hidden duplicates on pages "+
					"with multiple form variants — the tool picks the visible one automatically.",
			)),
			mcp.WithString("value", mcp.Required(), mcp.Description("Value to set in the field.")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			frameOpt(),
		),
		passThrough("fill_input"),
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
	frameSpec := toString(args["frame"])
	ctxID, offX, offY, err := resolveFrameContext(ctx, args["tabId"], frameSpec)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	activateIfDefault(ctx, args["tabId"])
	// localX/localY are in the target context's own viewport (where BiDi input
	// dispatches); reportX/reportY are translated back to the top viewport.
	var localX, localY float64
	sel := toString(args["selector"])
	if sel != "" {
		cx, cy, err := resolveSelectorCenter(ctx, ctxID, sel)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		localX, localY = cx, cy
	} else if _, hasX := args["x"]; hasX {
		// Caller's x,y are top-viewport coordinates; translate into the frame.
		localX = toFloat(args["x"]) - offX
		localY = toFloat(args["y"]) - offY
	} else {
		return mcp.NewToolResultError("cdp_click: provide either selector or x,y coordinates"), nil
	}
	clickCount := int(toFloat(args["clickCount"]))
	if clickCount < 1 {
		clickCount = 1
	}
	if clickCount > 3 {
		clickCount = 3
	}
	if err := bidiClickN(ctx, ctxID, localX, localY, "left", clickCount); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	out := map[string]any{"clicked": true, "x": localX + offX, "y": localY + offY, "clickCount": clickCount}
	if sel != "" {
		out["selector"] = sel
	}
	if frameSpec != "" {
		out["frame"] = frameSpec
	}
	return textResult(out)
}

// activateIfDefault foregrounds the target tab ONLY when it is already the
// daemon's default (active) context. Firefox needs a tab foregrounded for
// element.focus() and reliable key input — but we must never yank a BACKGROUND
// tab forward, which would hijack what the human is looking at. Acting on the
// default tab (the no-tabId / active-tab case) is what the agent does anyway,
// so activating it there is expected and non-disruptive; a tab explicitly
// targeted in the background is left where it is.
func activateIfDefault(ctx context.Context, tabId any) {
	top, err := resolveContext(tabId)
	if err != nil {
		return
	}
	def, derr := resolveContext(nil)
	if derr != nil || top != def {
		return
	}
	_ = bidiActivate(ctx, top)
}

func handleBiDiType(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	frameSpec := toString(args["frame"])
	ctxID, _, _, err := resolveFrameContext(ctx, args["tabId"], frameSpec)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	activateIfDefault(ctx, args["tabId"])
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
	// Humanized cadence is ON by default (~110 WPM). wpm=0 means instant.
	wpm := DefaultTypeWPM
	if v, ok := args["wpm"]; ok {
		wpm = int(toFloat(v))
	}
	if wpm > 0 {
		if err := bidiTypeHuman(ctx, ctxID, text, wpm); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
	} else if err := bidiType(ctx, ctxID, text); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	out := map[string]any{"typed": len(text), "wpm": wpm}
	if sel != "" {
		out["selector"] = sel
	}
	if frameSpec != "" {
		out["frame"] = frameSpec
	}
	return textResult(out)
}

func handleBiDiKey(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	frameSpec := toString(args["frame"])
	ctxID, _, _, err := resolveFrameContext(ctx, args["tabId"], frameSpec)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	key := toString(args["key"])
	mods := toStringSlice(args["modifiers"])
	if err := bidiKeyChord(ctx, ctxID, key, mods); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	out := map[string]any{"pressed": key}
	if len(mods) > 0 {
		out["modifiers"] = mods
	}
	if frameSpec != "" {
		out["frame"] = frameSpec
	}
	return textResult(out)
}

func handleBiDiScroll(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	frameSpec := toString(args["frame"])
	ctxID, offX, offY, err := resolveFrameContext(ctx, args["tabId"], frameSpec)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	var localX, localY float64
	sel := toString(args["selector"])
	if sel != "" {
		cx, cy, err := resolveSelectorCenter(ctx, ctxID, sel)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		localX, localY = cx, cy
	} else {
		tx := toFloat(args["x"])
		ty := toFloat(args["y"])
		if tx == 0 {
			tx = 600
		}
		if ty == 0 {
			ty = 400
		}
		// Caller's x,y are top-viewport; translate into the frame's viewport.
		localX = tx - offX
		localY = ty - offY
	}
	deltaX := toFloat(args["deltaX"])
	deltaY := toFloat(args["deltaY"])
	if err := bidiScroll(ctx, ctxID, localX, localY, deltaX, deltaY); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	out := map[string]any{"scrolled": true, "x": localX + offX, "y": localY + offY}
	if sel != "" {
		out["selector"] = sel
	}
	if frameSpec != "" {
		out["frame"] = frameSpec
	}
	return textResult(out)
}
