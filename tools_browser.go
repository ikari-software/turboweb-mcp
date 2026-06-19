package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerBrowserTools(s *server.MCPServer) {
	// --- launch_browser ---
	addTool(s,
		mcp.NewTool("launch_browser",
			mcp.WithDescription("Launch a new Chrome instance with stealth flags (--silent-debugger-extension-api to hide the debugging infobar). Auto-loads the extension. Uses a dedicated profile so it won't affect your main browser."),
			mcp.WithBoolean("headless", mcp.Description("Launch in headless mode (default false)")),
		),
		handleLaunchBrowser,
	)

	// --- connection_status ---
	addTool(s,
		mcp.NewTool("connection_status",
			mcp.WithDescription("Check if the browser extension and/or BiDi are connected to the MCP server"),
		),
		handleConnectionStatus,
	)

	// --- list_tabs ---
	addTool(s,
		mcp.NewTool("list_tabs",
			mcp.WithDescription("List all open Chrome tabs with their IDs, titles, and URLs"),
		),
		handleListTabs,
	)

	// --- navigate ---
	addTool(s,
		mcp.NewTool("navigate",
			mcp.WithDescription("Navigate a tab to a URL. Omit tabId to use the active tab. "+
				"Pass `frame` to navigate ONLY that frame instead of the whole tab — this "+
				"preserves the parent frameset, so frameset-dependent JS (geocoders, save "+
				"handlers) keeps working. Navigating to a frame's content URL at the top level "+
				"strips the shell and breaks that JS."),
			mcp.WithString("url", mcp.Required(), mcp.Description("URL to navigate to")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			mcp.WithString("frame", mcp.Description(
				"Optional. Navigate only this frame, leaving the parent frameset intact. "+
					"A framePath (\">\"-separated CSS selectors, e.g. \"#top_frame\" or "+
					"\"#top_frame > #csframe\"); discover frames with list_frames. With BiDi, any "+
					"frame including cross-origin; without BiDi, the parent chain must be same-origin.")),
		),
		handleNavigate,
	)

	// --- screenshot ---
	addTool(s,
		mcp.NewTool("screenshot",
			mcp.WithDescription("Take a screenshot of a tab. Returns a JPEG image scaled to maxWidth (default 1280px, NOT retina 3000px). Fast and compact."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			mcp.WithNumber("maxWidth", mcp.Description("Max width in pixels (default 1280)")),
			mcp.WithNumber("quality", mcp.Description("JPEG quality 1-100 (default 70)")),
		),
		handleScreenshot,
	)

	// --- turbo_snapshot ---
	addTool(s,
		mcp.NewTool("turbo_snapshot",
			mcp.WithDescription("TURBO: Screenshot + interactive element map in ONE call. Returns a scaled JPEG image AND a JSON spatial map of all interactive elements. The fastest way to understand a page."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			mcp.WithNumber("maxWidth", mcp.Description("Screenshot max width (default 1280)")),
			mcp.WithNumber("quality", mcp.Description("JPEG quality 1-100 (default 70)")),
		),
		handleTurboSnapshot,
	)

	// --- screenshot_diff ---
	addTool(s,
		mcp.NewTool("screenshot_diff",
			mcp.WithDescription("Prove an action changed the page WITHOUT shipping screenshots to the model. Call once with no baseline to MINT a baselineId (mark the before-state); call again with that baselineId after your action to get a cheap JSON verdict: changed yes/no, similarity score, changed bounding-box regions, and a free MutationObserver DOM summary. The default return is pure JSON (a few hundred bytes) — set includeThumb to also get an annotated image. Capture `after` once the page is idle for reliable results."),
			mcp.WithString("baselineId", mcp.Description("Token from a previous screenshot_diff call. Diff the current page against that cached baseline.")),
			mcp.WithString("before", mcp.Description("Base64 JPEG/PNG explicit before-image. Ignored if baselineId is set.")),
			mcp.WithString("after", mcp.Description("Base64 JPEG/PNG explicit after-image. If omitted the tool captures the current page state itself.")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			mcp.WithNumber("threshold", mcp.Description("Per-pixel perceptual delta 0..1 above which a pixel counts as changed (default 0.10)")),
			mcp.WithNumber("minScore", mcp.Description("Similarity 0..1 below which the verdict is 'changed' (default 0.997)")),
			mcp.WithBoolean("includeThumb", mcp.Description("Append an annotated diff thumbnail image (default false — the point of the tool is to avoid shipping images)")),
			mcp.WithBoolean("domSignal", mcp.Description("Also fold in the MutationObserver DOM verdict (default true; nearly free)")),
			mcp.WithBoolean("fast", mcp.Description("DOM-only fast path: when the DOM reports zero mutations, skip the after capture entirely and return changed:false (default true)")),
		),
		handleScreenshotDiff,
	)
}

// screenshotMeta is the content-script's screenshot_diff_meta reply: the
// resolved ignore-selector mask boxes plus the live MutationObserver
// summary. masks come back in CSS pixels and are scaled to capture pixels
// by the handler before diffing.
type screenshotMeta struct {
	Masks []struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
		W float64 `json:"w"`
		H float64 `json:"h"`
	} `json:"masks"`
	DOM      domSignal `json:"dom"`
	Viewport struct {
		W       float64 `json:"w"`
		H       float64 `json:"h"`
		ScrollY float64 `json:"scrollY"`
	} `json:"viewport"`
	URL string `json:"url"`
}

// domSignal mirrors the MutationObserver counter object content.js keeps.
type domSignal struct {
	Mutations      int    `json:"mutations"`
	Added          int    `json:"added"`
	Removed        int    `json:"removed"`
	Attrs          int    `json:"attrs"`
	Text           int    `json:"text"`
	LargestSubtree string `json:"largest_subtree"`
	Partial        bool   `json:"partial"`
}

// capturedShot is one screenshot the tool took itself, with the metadata
// the diff needs (URL for the navigation short-circuit, dimensions).
type capturedShot struct {
	data   []byte
	url    string
	width  int
	height int
}

// handleScreenshotDiff implements the screenshot_diff tool. With no
// baseline source it MINTS a baseline (capture now, cache, return the id
// only). With a baseline it captures/accepts the after image, computes the
// pixel diff in diff.go, folds in the DOM signal, caches the after image as
// the next baseline, and returns the compact JSON verdict.
func handleScreenshotDiff(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	domSignalOn := getBool(args, "domSignal", true)
	fast := getBool(args, "fast", true)
	includeThumb := getBool(args, "includeThumb", false)

	// --- resolve the baseline source ---------------------------------
	baselineID, _ := args["baselineId"].(string)
	beforeArg, _ := args["before"].(string)

	if baselineID == "" && beforeArg == "" {
		// Mint-baseline path: capture the current state, take a DOM mark,
		// cache both, and return only the new id (§2). No diff.
		shot, err := captureScreenshot(ctx, args)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("baseline capture failed: %v", err)), nil
		}
		mark := ""
		if domSignalOn {
			mark = markDOMMutations(args)
		}
		id := theBaselineCache.put(baselineEntry{
			image: shot.data, domMark: mark, url: shot.url,
		})
		return textResult(map[string]any{
			"baselineId": id,
			"minted":     true,
			// changed is explicitly null on a mint: there is nothing to
			// compare against yet, so an agent reading result.changed
			// gets a deliberate null rather than undefined.
			"changed":  nil,
			"viewport": map[string]int{"w": shot.width, "h": shot.height},
		})
	}

	// --- resolve the before image ------------------------------------
	var beforeBytes []byte
	var beforeURL, domMark string
	switch {
	case baselineID != "":
		entry, ok := theBaselineCache.get(baselineID)
		if !ok {
			return mcp.NewToolResultError(fmt.Sprintf(
				"baselineId %q is unknown or expired (baselines live %s) — call screenshot_diff with no baseline to mint a fresh one",
				baselineID, baselineCacheTTL)), nil
		}
		beforeBytes = entry.image
		beforeURL = entry.url
		domMark = entry.domMark
	default:
		decoded, err := base64.StdEncoding.DecodeString(beforeArg)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("decode `before` base64: %v", err)), nil
		}
		beforeBytes = decoded
	}

	// --- fetch the DOM signal + ignore masks -------------------------
	var meta screenshotMeta
	haveMeta := false
	if domSignalOn {
		if m, ok := fetchScreenshotMeta(args, domMark); ok {
			meta = m
			haveMeta = true
		}
	}

	// DOM-only fast path: a pure no-op click produces zero mutations, so
	// skip the after capture entirely (§5). Suppressed when the DOM is
	// only partially observable (cross-origin iframe / closed shadow root)
	// or the page navigated away from the baseline URL.
	if fast && haveMeta && !meta.DOM.Partial && meta.DOM.Mutations == 0 &&
		(beforeURL == "" || meta.URL == "" || meta.URL == beforeURL) {
		return textResult(map[string]any{
			"changed":      false,
			"score":        1.0,
			"pixelSkipped": true,
			"dom":          meta.DOM,
			"baselineId":   baselineID,
			"note":         "DOM reported zero mutations; after-capture skipped (fast path)",
		})
	}

	// --- resolve the after image -------------------------------------
	var afterBytes []byte
	var afterURL string
	if afterArg, ok := args["after"].(string); ok && afterArg != "" {
		decoded, err := base64.StdEncoding.DecodeString(afterArg)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("decode `after` base64: %v", err)), nil
		}
		afterBytes = decoded
	} else {
		shot, err := captureScreenshot(ctx, args)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("after capture failed: %v", err)), nil
		}
		afterBytes = shot.data
		afterURL = shot.url
	}

	// nextBaseline returns the baseline_id to echo back. When the caller
	// passed an explicit baselineID, that baseline is reused verbatim — we
	// do NOT mint+cache a fresh entry, because the 16-entry FIFO cache would
	// otherwise evict the caller's own baseline after a few chained diffs.
	// Only mint (cache the after image) when the diff was against an
	// explicit `before` arg, where there is no caller-owned baseline to
	// preserve.
	nextBaseline := func() string {
		if baselineID != "" {
			return baselineID
		}
		return theBaselineCache.put(baselineEntry{image: afterBytes, url: afterURL})
	}

	// Navigation short-circuit: a URL change is itself the verdict (§7).
	if beforeURL != "" && afterURL != "" && beforeURL != afterURL {
		return textResult(map[string]any{
			"changed":    true,
			"score":      0.0,
			"navigated":  true,
			"fromUrl":    beforeURL,
			"toUrl":      afterURL,
			"dom":        meta.DOM,
			"baselineId": nextBaseline(),
		})
	}

	// --- run the pixel diff ------------------------------------------
	opts := DiffOpts{
		Threshold: toFloat(args["threshold"]),
		MinScore:  toFloat(args["minScore"]),
		WantThumb: includeThumb,
		Masks:     metaMasks(meta),
	}
	res, err := diffImages(beforeBytes, afterBytes, opts)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("diff failed: %v", err)), nil
	}

	// Fold in the DOM signal: changed is true if EITHER pixels OR DOM say
	// so (§5). A steady pixel region with zero DOM mutations is more
	// likely a CSS animation than a real change.
	if haveMeta {
		if meta.DOM.Mutations > 0 {
			res.Changed = true
			res.LikelyAnimation = false
		} else if res.LikelyAnimation {
			// pixels moved, DOM quiet — keep likely_animation, soften
			// the hard verdict so the agent treats it as a spinner.
			res.Changed = false
		}
	}

	out := map[string]any{
		"changed":         res.Changed,
		"score":           round3(res.Score),
		"changedFraction": round3(res.ChangedFraction),
		"regions":         res.Regions,
		"regionCount":     res.RegionCount,
		"viewport":        res.Viewport,
		"scrolled":        res.Scrolled,
		"baselineId":      nextBaseline(),
		"thumbOmitted":    !includeThumb,
	}
	if res.SizeChanged {
		out["sizeChanged"] = true
	}
	if res.LikelyAnimation {
		out["likelyAnimation"] = true
	}
	if res.Scrolled {
		out["note"] = "global vertical shift detected — regions skipped; re-baseline before relying on regions"
	}
	if haveMeta {
		out["dom"] = meta.DOM
	}

	if includeThumb && res.Thumb != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(toJSON(out)),
				mcp.NewImageContent(base64Encode(res.Thumb), "image/jpeg"),
			},
		}, nil
	}
	return textResult(out)
}

// captureScreenshot reuses the existing screenshot extension path to grab
// the current tab state. screenshot_diff deliberately does NOT add new
// capture logic — it inherits handleScreenshot's BiDi-or-extension routing
// indirectly via send().
func captureScreenshot(_ context.Context, args map[string]any) (capturedShot, error) {
	params := map[string]any{}
	if tabID, ok := args["tabId"]; ok {
		params["tabId"] = tabID
	}
	raw, err := send("screenshot", params)
	if err != nil {
		return capturedShot{}, err
	}
	var r struct {
		Base64 string `json:"base64"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return capturedShot{}, fmt.Errorf("parse screenshot: %w", err)
	}
	data, err := base64.StdEncoding.DecodeString(r.Base64)
	if err != nil {
		return capturedShot{}, fmt.Errorf("decode screenshot base64: %w", err)
	}
	return capturedShot{data: data, url: r.URL, width: r.Width, height: r.Height}, nil
}

// fetchScreenshotMeta asks the content script (one round trip) for the
// ignore-selector mask boxes and the MutationObserver summary accumulated
// since the baseline's mark token.
func fetchScreenshotMeta(args map[string]any, sinceMark string) (screenshotMeta, bool) {
	params := map[string]any{}
	if tabID, ok := args["tabId"]; ok {
		params["tabId"] = tabID
	}
	if sinceMark != "" {
		params["since"] = sinceMark
	}
	if ig, ok := args["ignore"]; ok {
		params["ignore"] = ig
	}
	raw, err := send("screenshot_diff_meta", params)
	if err != nil {
		return screenshotMeta{}, false
	}
	var meta screenshotMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return screenshotMeta{}, false
	}
	return meta, true
}

// markDOMMutations issues dom_mutations_mark to the content script and
// returns the snapshot token (or "" if the content script is unreachable).
func markDOMMutations(args map[string]any) string {
	params := map[string]any{}
	if tabID, ok := args["tabId"]; ok {
		params["tabId"] = tabID
	}
	raw, err := send("dom_mutations_mark", params)
	if err != nil {
		return ""
	}
	var r struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return ""
	}
	return r.Token
}

// metaMasks converts the content script's CSS-pixel mask boxes into image
// rectangles for diff.go. The capture and the DOM both measure in the same
// (resized) viewport space here — screenshot scales to maxWidth and the
// content script reports viewport pixels — so no extra scaling is applied;
// a future DPR-aware refinement could rescale by viewport.W / capture.W.
func metaMasks(meta screenshotMeta) []image.Rectangle {
	if len(meta.Masks) == 0 {
		return nil
	}
	out := make([]image.Rectangle, 0, len(meta.Masks))
	for _, m := range meta.Masks {
		x, y := int(m.X), int(m.Y)
		out = append(out, image.Rect(x, y, x+int(m.W), y+int(m.H)))
	}
	return out
}

// round3 trims a float to 3 decimal places for a compact JSON payload.
func round3(f float64) float64 {
	return float64(int(f*1000+0.5)) / 1000
}

func handleLaunchBrowser(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	headless := getBool(args, "headless", false)
	pid, err := launchBrowser(headless)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return textResult(map[string]any{
		"launched": true,
		"pid":      pid,
		"headless": headless,
		"stealth":  true,
		"message":  "Chrome launched with --silent-debugger-extension-api (no debugging infobar)",
	})
}

func handleConnectionStatus(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Browser extensions connect to the daemon, not to this relay process, so
	// this process's own browsers map is empty in relay mode. Route through
	// send() — sendDirect/sendViaRelay land on the daemon, which answers from
	// its real browser WebSocket state. Cap the timeout: connection_status is
	// purely diagnostic, so it must not inherit the 30s default and stall an
	// agent waiting on a wedged daemon.
	raw, err := send("connection_status", nil, 3000)
	var status map[string]any
	switch {
	case err != nil:
		// connection_status is a diagnostic tool — it must always return a
		// parseable verdict, never an MCP error. A failed send() means the
		// relay link to the daemon is down or too slow; report not-connected
		// with the reason rather than making the agent handle a tool error.
		status = map[string]any{
			"connected": false, "extension": false, "bidi": false,
			"error": err.Error(),
		}
	case json.Unmarshal(raw, &status) != nil:
		status = map[string]any{
			"connected": false, "extension": false, "bidi": false,
			"error": "malformed connection_status response from daemon",
		}
	}
	// BiDi can be owned by whichever process launched the browser — the
	// daemon, or this relay process if an agent called launch_browser here.
	// Merge the local view so a relay-owned BiDi session still counts, even
	// when the daemon link is down.
	if getBiDi() != nil {
		status["bidi"] = true
		status["connected"] = true
	}
	return textResult(status)
}

func handleListTabs(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	raw, err := send("list_tabs", nil)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if getBiDi() != nil {
		_ = ingestExtensionTabsForBiDi(raw)
	}
	return mcp.NewToolResultText(string(raw)), nil
}

func handleNavigate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()

	// Frame-scoped navigation: navigate only the named frame so the parent
	// frameset (and its geocoder/save JS) survives. Prefer BiDi — it navigates
	// the child browsing context directly and reaches cross-origin frames.
	// Otherwise fall back to the content script setting the iframe's src, which
	// requires a same-origin parent chain.
	if frameSpec := toString(args["frame"]); frameSpec != "" {
		url := getString(args, "url", "")
		if url == "" {
			return mcp.NewToolResultError("navigate: url is required"), nil
		}
		if getBiDi() != nil {
			ctxID, _, _, err := resolveFrameContext(ctx, args["tabId"], frameSpec)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if err := bidiNavigate(ctx, ctxID, url); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return textResult(map[string]any{"navigated": true, "frame": frameSpec, "url": url, "via": "bidi"})
		}
		raw, err := send("navigate_frame", rawArgs(args))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(string(raw)), nil
	}

	raw, err := send("navigate", rawArgs(args))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(raw)), nil
}

func handleScreenshot(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	quality := intOr(args["quality"], 70)

	// Try BiDi first (works in both Chrome and Firefox, no focus needed)
	if getBiDi() != nil {
		ctxID, err := resolveContext(args["tabId"])
		if err == nil {
			data, err := bidiScreenshot(ctx, ctxID, quality)
			if err == nil {
				encoded := base64Encode(data)
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						mcp.NewImageContent(encoded, "image/jpeg"),
						mcp.NewTextContent("screenshot via BiDi"),
					},
				}, nil
			}
			logger.Printf("BiDi screenshot failed, falling back to extension: %v", err)
		}
	}

	// Fallback: extension path
	raw, err := send("screenshot", rawArgs(args))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	var result struct {
		Base64   string `json:"base64"`
		MimeType string `json:"mimeType"`
		Width    int    `json:"width"`
		Height   int    `json:"height"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to parse screenshot: %v", err)), nil
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewImageContent(result.Base64, result.MimeType),
			mcp.NewTextContent(fmt.Sprintf("%dx%d jpeg", result.Width, result.Height)),
		},
	}, nil
}

func handleTurboSnapshot(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	raw, err := send("turbo_snapshot", rawArgs(args))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	var result struct {
		Screenshot struct {
			Base64   string `json:"base64"`
			MimeType string `json:"mimeType"`
			Width    int    `json:"width"`
			Height   int    `json:"height"`
		} `json:"screenshot"`
		InteractiveMap json.RawMessage `json:"interactiveMap"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to parse turbo_snapshot: %v", err)), nil
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewImageContent(result.Screenshot.Base64, result.Screenshot.MimeType),
			mcp.NewTextContent(string(result.InteractiveMap)),
		},
	}, nil
}
