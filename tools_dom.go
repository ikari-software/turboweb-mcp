package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// frameOpt is the shared, optional `frame` parameter that scopes a DOM tool to
// a (same-origin) iframe. It is forwarded verbatim to the extension, which
// resolves it against the target frame's contentDocument and keeps reported
// coordinates viewport-relative so a query inside a frame round-trips to a
// later click. Cross-origin frames are not reachable through the content
// script — the cdp_* tools pierce those natively.
func frameOpt() mcp.ToolOption {
	return mcp.WithString("frame", mcp.Description(
		"Optional. Scope this call to an iframe. A framePath: a \">\"-separated list of CSS "+
			"selectors, each resolving an <iframe>/<frame> within the previous frame's document "+
			"(e.g. \"#top_frame\" or \"#top_frame > #csframe\"). Discover frames with list_frames. "+
			"Works for BOTH same-origin AND cross-origin frames — the content script runs in every "+
			"frame, so DOM reads and synthetic interaction (extract_text, find_text, query_elements, "+
			"click, type_text, fill_input, scroll, etc.) reach cross-origin embeds too. Frames are "+
			"addressed by SELECTOR, not coordinates: a framePath must be a chain of CSS selectors. "+
			"Coordinates stay top-viewport-relative in the result either way.",
	))
}

func registerDomTools(s *server.MCPServer) {
	// --- list_frames ---
	addTool(s,
		mcp.NewTool("list_frames",
			mcp.WithDescription("Enumerate the frame tree of the page. Returns every <iframe>/<frame> "+
				"with {frameId, framePath, id, name, src, url, origin, isSameOrigin, rect}. framePath "+
				"(e.g. \"#top_frame > #csframe\") is what you pass as the `frame` argument to other DOM "+
				"tools to scope them to that frame. Both same-origin and cross-origin frames are "+
				"reachable by DOM/synthetic tools (the content script runs in every frame); pass the "+
				"framePath as the `frame` argument. The cdp_* tools also accept `frame` for trusted input."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		handleListFrames,
	)

	// --- extract_text ---
	addTool(s,
		mcp.NewTool("extract_text",
			mcp.WithDescription("Fast DOM-based OCR: extract ALL visible text on the page with spatial positions (x, y, width, height). Instant — no image processing needed. Supports scoping by CSS selector or viewport region. Add `question` to get a concise Haiku-processed answer instead of raw data."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			mcp.WithString("selector", mcp.Description("CSS selector to scope extraction to a subtree")),
			mcp.WithObject("region",
				mcp.Description("Only return text within this viewport rectangle {rx, ry, rw, rh}"),
				mcp.Properties(map[string]any{
					"rx": map[string]any{"type": "number"},
					"ry": map[string]any{"type": "number"},
					"rw": map[string]any{"type": "number"},
					"rh": map[string]any{"type": "number"},
				}),
			),
			mcp.WithNumber("max", mcp.Description("Max text blocks to return (default 500)")),
			mcp.WithString("question", mcp.Description("If provided, Haiku preprocesses the data and returns a concise answer instead of raw JSON")),
			frameOpt(),
		),
		handleExtractText,
	)

	// --- find_text ---
	addTool(s,
		mcp.NewTool("find_text",
			mcp.WithDescription("Search for visible text on the page (like Cmd+F). Returns matching elements with positions, text content, and CSS selectors. Add `question` for Haiku-processed answer."),
			mcp.WithString("query", mcp.Required(), mcp.Description("Text to search for")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			mcp.WithNumber("max", mcp.Description("Max results (default 20)")),
			mcp.WithBoolean("caseSensitive", mcp.Description("Case sensitive search (default false)")),
			mcp.WithString("question", mcp.Description("If provided, Haiku preprocesses the results and returns a concise answer")),
			frameOpt(),
		),
		handleFindText,
	)

	// --- inspect ---
	addTool(s,
		mcp.NewTool("inspect",
			mcp.WithDescription("Deep one-shot inspection of an element. Find by CSS selector, coordinates, or text search. Returns element details, attributes, styles, parent chain, children, siblings. Add `question` for a concise Haiku-processed answer."),
			mcp.WithString("selector", mcp.Description("CSS selector")),
			mcp.WithNumber("x", mcp.Description("X coordinate (uses elementFromPoint)")),
			mcp.WithNumber("y", mcp.Description("Y coordinate (uses elementFromPoint)")),
			mcp.WithString("text", mcp.Description("Find first element containing this text")),
			mcp.WithNumber("depth", mcp.Description("How deep to summarize children (default 2)")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			mcp.WithString("question", mcp.Description("If provided, Haiku answers this question about the element instead of returning raw data")),
			frameOpt(),
		),
		handleInspect,
	)

	// --- describe ---
	addTool(s,
		mcp.NewTool("describe",
			mcp.WithDescription("The ultimate one-call page understanding tool. Takes a screenshot, gathers spatial/text data, and sends everything to Haiku with your question. Returns a concise answer."),
			mcp.WithString("question", mcp.Required(), mcp.Description("What do you want to know about the page?")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			mcp.WithString("selector", mcp.Description("Scope data gathering to this CSS selector subtree")),
			mcp.WithBoolean("includeScreenshot", mcp.Description("Include a screenshot for visual analysis (default true)")),
		),
		handleDescribe,
	)

	// --- page_yaml ---
	addTool(s,
		mcp.NewTool("page_yaml",
			mcp.WithDescription("Get a YAML semantic structure of the page. Much more useful than raw HTML — strips styling noise, shows logical structure with spatial positions."),
			mcp.WithString("selector", mcp.Description("CSS selector for subtree (omit for full page)")),
			mcp.WithNumber("maxDepth", mcp.Description("Max nesting depth (default 6)")),
			mcp.WithBoolean("visibleOnly", mcp.Description("Only include visible elements (default true)")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			frameOpt(),
		),
		handlePageYaml,
	)

	// --- get_html ---
	addTool(s,
		mcp.NewTool("get_html",
			mcp.WithDescription("Get the HTML of the page or a specific element. Supports depth limiting to avoid massive output."),
			mcp.WithString("selector", mcp.Description("CSS selector (omit for full page)")),
			mcp.WithBoolean("outer", mcp.Description("Return outerHTML instead of innerHTML (default false)")),
			mcp.WithNumber("maxDepth", mcp.Description("Max DOM depth to traverse. Deeper nodes get summarized. 0 = unlimited (default).")),
			mcp.WithNumber("maxLength", mcp.Description("Max output length in chars (default 200000, max 500000)")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			frameOpt(),
		),
		handleGetHTML,
	)

	// --- get_interactive_map ---
	addTool(s,
		mcp.NewTool("get_interactive_map",
			mcp.WithDescription("Get ALL interactive elements (buttons, links, inputs, etc.) with their positions, text, selectors, and attributes. Spatial map for understanding page layout and available actions."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			frameOpt(),
		),
		handleGetInteractiveMap,
	)

	// --- query_elements ---
	addTool(s,
		mcp.NewTool("query_elements",
			mcp.WithDescription("Query elements by CSS selector. Returns matching elements with positions, text, and attributes."),
			mcp.WithString("selector", mcp.Required(), mcp.Description("CSS selector")),
			mcp.WithNumber("limit", mcp.Description("Max elements to return (default 50)")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			frameOpt(),
		),
		handleQueryElements,
	)

	// --- page_capabilities ---
	addTool(s,
		mcp.NewTool("page_capabilities",
			mcp.WithDescription("Probe what the current page supports before automating it. Returns "+
				"cdp_available (whether cdp_* tools work), csp_allows_eval (whether execute_js works "+
				"— strict CSPs block it), framework (react/vue/svelte/angular/unknown), monaco_present, "+
				"iframes_count, and shadow_roots_count. Call this first when a page fights automation."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		handlePageCapabilities,
	)
}

func handleExtractText(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	question := getString(args, "question", "")
	raw, err := send("extract_text", rawArgs(args))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return maybeAsk(raw, question, "")
}

func handleFindText(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	question := getString(args, "question", "")
	raw, err := send("find_text", rawArgs(args))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return maybeAsk(raw, question, "")
}

func handleInspect(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	question := getString(args, "question", "")
	raw, err := send("inspect", rawArgs(args))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return maybeAsk(raw, question, "")
}

func handleDescribe(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	question := getString(args, "question", "")
	tabParams := map[string]any{}
	if v, ok := args["tabId"]; ok && v != nil {
		tabParams["tabId"] = v
	}
	if v, ok := args["selector"]; ok && v != nil {
		tabParams["selector"] = v
	}
	includeScreenshot := getBool(args, "includeScreenshot", true)

	type screenshotResult struct {
		base64   string
		width    int
		height   int
		mimeType string
	}

	var (
		shot      screenshotResult
		structure json.RawMessage
		imap      json.RawMessage
		wg        sync.WaitGroup
		shotErr   error
		structErr error
		imapErr   error
	)

	// Fire 3 parallel requests
	if includeScreenshot {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, err := send("screenshot", tabParams)
			if err != nil {
				shotErr = err
				return
			}
			var r struct {
				Base64   string `json:"base64"`
				MimeType string `json:"mimeType"`
				Width    int    `json:"width"`
				Height   int    `json:"height"`
			}
			if json.Unmarshal(raw, &r) == nil {
				shot = screenshotResult{base64: r.Base64, width: r.Width, height: r.Height, mimeType: r.MimeType}
			}
		}()
	}

	wg.Add(2)
	go func() {
		defer wg.Done()
		structParams := map[string]any{}
		for k, v := range tabParams {
			structParams[k] = v
		}
		raw, err := send("get_page_structure", structParams)
		if err != nil {
			structErr = err
			return
		}
		structure = raw
	}()
	go func() {
		defer wg.Done()
		raw, err := send("get_interactive_map", tabParams)
		if err != nil {
			imapErr = err
			return
		}
		imap = raw
	}()

	wg.Wait()

	// Build context
	contextStr := ""
	if structErr == nil && structure != nil {
		var s struct {
			YAML string `json:"yaml"`
		}
		if json.Unmarshal(structure, &s) == nil && s.YAML != "" {
			contextStr += "Page structure:\n" + s.YAML + "\n\n"
		} else {
			contextStr += "Page structure:\n" + string(structure) + "\n\n"
		}
	}
	if imapErr == nil && imap != nil {
		contextStr += "Interactive elements:\n" + string(imap) + "\n"
	}

	// If no Haiku, return raw data
	if haiku == nil {
		result := map[string]any{
			"question":  question,
			"structure": string(structure),
			"interactiveMap": string(imap),
		}
		if shotErr != nil {
			result["screenshotError"] = shotErr.Error()
		}
		return textResult(result)
	}

	answer, err := haiku.ask(question, contextStr, shot.base64, "")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Haiku error: %v", err)), nil
	}
	return mcp.NewToolResultText(answer), nil
}

func handlePageYaml(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	raw, err := send("get_page_structure", rawArgs(args))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	// Extract the yaml field if present
	var result struct {
		YAML string `json:"yaml"`
	}
	if json.Unmarshal(raw, &result) == nil && result.YAML != "" {
		return mcp.NewToolResultText(result.YAML), nil
	}
	return mcp.NewToolResultText(string(raw)), nil
}

func handleGetHTML(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	raw, err := send("get_html", rawArgs(args))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(raw)), nil
}

func handleGetInteractiveMap(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	raw, err := send("get_interactive_map", rawArgs(args))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(raw)), nil
}

func handleQueryElements(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	raw, err := send("query_elements", rawArgs(args))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(raw)), nil
}

func handlePageCapabilities(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	raw, err := send("page_capabilities", rawArgs(req.GetArguments()))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(raw)), nil
}

func handleListFrames(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	raw, err := send("list_frames", rawArgs(req.GetArguments()))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(raw)), nil
}
