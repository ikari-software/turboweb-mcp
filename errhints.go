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
//
// This table is the home for advice that follows from the message alone, so
// errors elsewhere are raised bare. The one other author is explainError in
// the extension, for advice that depends on the state of the tab (unloaded,
// still loading, a browser-internal page), which only the extension can see.
var errorHints = []struct {
	match *regexp.Regexp
	hint  string
}{
	{regexp.MustCompile(`(?i)no browser extension connected`),
		"this is usually a few seconds of reconnecting, so call connection_status and retry before anything else. " +
			"If it stays disconnected, ask the user to (1) check that their browser is running with the TurboWeb extension enabled " +
			"(chrome://extensions or about:addons) and (2) click the extension's toolbar icon, which wakes a suspended extension and reconnects it. " +
			"Do not switch to another browser or automation tool: the user's tabs and logins are in their own browser, and a fresh one has neither. " +
			"For the user, if that does not help: `curl http://127.0.0.1:<port>/version` shows whether the daemon is alive; " +
			"after rebuilding turboweb a stale daemon may still be running and needs a restart."},
	{regexp.MustCompile(`(?i)bidi not connected|bidi connection closed|bidi dial failed|chrome\.debugger is not available`),
		"this tool needs WebDriver BiDi, which is not attached. The content-script tools (click, type_text, fill_input, " +
			"scroll, extract_text, get_html, capture_page) work without it on the same tab, so use those. " +
			"connect_bidi attaches to the user's own browser if they started it with --remote-debugging-port; " +
			"launch_browser is not a substitute, since it opens an empty profile without the user's tabs or logins."},
	{regexp.MustCompile(`(?i)^timeout after \d+ms: approve_agent`),
		"nobody answered the approval prompt in time. Tell the user a window is waiting for their decision and ask " +
			"them to answer it, then call introduce again, which waits for their answer. Do not retry other browser " +
			"tools meanwhile: without approval they are refused."},
	{regexp.MustCompile(`(?i)declined to let|has not yet approved|approval window`),
		"the browser's user is the one deciding, so do not retry or switch browsers. connection_status reports " +
			"`approval` per browser: denied means stop, pending means ask the user to answer the window, " +
			"needed means call introduce."},
	{regexp.MustCompile(`(?i)^timeout after \d+ms: wake_tab`),
		"the tab did not finish loading in time, and calling wake_tab again is unlikely to change that. " +
			"page_reload the tab, or open_tab with its URL and work in the new tab."},
	{regexp.MustCompile(`(?i)^timeout after \d+ms: (move_tabs|close_tabs|open_tab)`),
		"the reply was lost, but the browser may have done part or all of the work. Call list_tabs to see the " +
			"current state before retrying: a blind retry can open a duplicate tab or repeat a move."},
	{regexp.MustCompile(`(?i)^timeout after \d+ms`),
		"the browser did not answer in time, which usually means the tab is unloaded, still loading, or busy. " +
			"If the call targets a tab, call wake_tab with the same tabId and retry; connection_status shows whether the browser is still connected."},
	{regexp.MustCompile(`(?i)not connected to daemon|relay write error|websocket write error|connection (was )?closed`),
		"the link to the browser dropped and reconnects by itself within a few seconds. " +
			"Call connection_status, then retry the same call. The user's browser and its tabs are unaffected."},
	{regexp.MustCompile(`(?i)tab \d+ not found in any connected browser|no bidi context for tab|no tab with id|invalid tab id`),
		"that tab id is stale (the tab was closed, or the browser restarted and renumbered its tabs). " +
			"Call list_tabs for current ids, or open_tab to get a fresh tab, then retry."},
	{regexp.MustCompile(`(?i)no element matches selector|matched no <iframe>|element not found`),
		"the selector does not match the page as it is now. Look before retrying: get_interactive_map lists what can be " +
			"clicked with working selectors, find_text locates visible text, and list_frames shows whether the target sits inside a frame."},
	{regexp.MustCompile(`(?i)content security policy|unsafe-eval|\bCSP\b`),
		"this page's Content-Security-Policy blocks injected scripts. Use the tools that do not evaluate code in the page: " +
			"click, type_text, fill_input, extract_text, get_html, query_elements. page_capabilities reports what the page allows."},
	{regexp.MustCompile(`(?i)browser not found|failed to launch browser|no connection established within`),
		"a separate browser could not be started, and one is rarely needed: the user's own browser is driven through the " +
			"extension. Call connection_status and list_tabs to work in it."},
	{regexp.MustCompile(`(?i)captureVisibleTab`),
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
