package main

import (
	"context"
	"regexp"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// hintMarker introduces the recovery advice in an error. The extension uses
// the same marker for errors it can explain from the tab's state, so an error
// that already carries advice is left alone.
const hintMarker = "What to do:"

// errorHints maps a failure to the next step that recovers from it. An agent
// handed a bare "timeout" or "not found" tends to conclude the browser is
// unusable and reach for a different one; naming the tool that fixes it keeps
// the work in the user's own browser. First match wins.
var errorHints = []struct {
	match *regexp.Regexp
	hint  string
}{
	{regexp.MustCompile(`(?i)bidi not connected|bidi connection closed|bidi dial failed`),
		"this tool needs WebDriver BiDi, which is not attached. The content-script tools (click, type_text, fill_input, " +
			"scroll, extract_text, get_html, capture_page) work without it on the same tab, so use those. " +
			"connect_bidi attaches to the user's own browser if they started it with --remote-debugging-port."},
	{regexp.MustCompile(`(?i)^timeout after \d+ms`),
		"the browser did not answer in time, which usually means the tab is unloaded, still loading, or busy. " +
			"If the call targets a tab, call wake_tab with the same tabId and retry; connection_status shows whether the browser is still connected."},
	{regexp.MustCompile(`(?i)not connected to daemon|relay write error|websocket write error|connection (was )?closed|browser disconnected`),
		"the link to the browser dropped and reconnects by itself within a few seconds. " +
			"Call connection_status, then retry the same call. The user's browser and its tabs are unaffected."},
	{regexp.MustCompile(`(?i)tab \d+ not found in any connected browser|no bidi context for tab|no tab with id|invalid tab id`),
		"that tab id is stale (the tab was closed, or the browser restarted and renumbered its tabs). " +
			"Call list_tabs for current ids, or open_tab to get a fresh tab, then retry."},
	{regexp.MustCompile(`(?i)no element matches selector|matched no <iframe>|selector .* (not found|matched nothing)|element not found`),
		"the selector does not match the page as it is now. Look before retrying: get_interactive_map lists what can be " +
			"clicked with working selectors, find_text locates visible text, and list_frames shows whether the target sits inside a frame."},
	{regexp.MustCompile(`(?i)content security policy|unsafe-eval|\bCSP\b`),
		"this page's Content-Security-Policy blocks injected scripts. Use the tools that do not evaluate code in the page: " +
			"click, type_text, fill_input, extract_text, get_html, query_elements. page_capabilities reports what the page allows."},
	{regexp.MustCompile(`(?i)browser not found|failed to launch browser|no connection established within`),
		"a separate browser could not be started, and one is rarely needed: the user's own browser is driven through the " +
			"extension. Call connection_status and list_tabs to work in it."},
	{regexp.MustCompile(`(?i)captureVisibleTab|cannot capture|not in the foreground`),
		"a screenshot needs the tab to be the visible one in its window. For a tab in the background, read it with " +
			"extract_text, get_html or capture_page instead, which need no focus (call wake_tab first if it is unloaded)."},
}

// hintFor returns recovery advice for an error message, or "" when the
// message already carries some or nothing specific applies.
func hintFor(msg string) string {
	if strings.Contains(msg, hintMarker) {
		return ""
	}
	for _, h := range errorHints {
		if h.match.MatchString(msg) {
			return h.hint
		}
	}
	return ""
}

// withErrorHints appends recovery advice to a tool's error result.
func withErrorHints(h server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := h(ctx, req)
		if err != nil || res == nil || !res.IsError {
			return res, err
		}
		for i, c := range res.Content {
			text, ok := c.(mcp.TextContent)
			if !ok {
				continue
			}
			if hint := hintFor(text.Text); hint != "" {
				text.Text = strings.TrimRight(text.Text, ". ") + ". " + hintMarker + " " + hint
				res.Content[i] = text
			}
			break
		}
		return res, nil
	}
}
