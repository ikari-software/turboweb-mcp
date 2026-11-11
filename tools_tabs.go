package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// moveTabsTimeoutMs covers reordering a few hundred tabs: the extension moves
// them one at a time, re-reading positions before each move.
const moveTabsTimeoutMs = 120000

// wake_tab waits for a page load. The extension gets the wait itself; the
// request gets that plus slack, so a load that runs to its limit is reported
// by the extension rather than as a bare transport timeout.
const (
	wakeTabDefaultMs    = 20000
	wakeTabMaxMs        = 120000
	wakeTabReplySlackMs = 10000
)

func registerTabTools(s *server.MCPServer) {
	// --- open_tab ---
	addTool(s,
		mcp.NewTool("open_tab",
			mcp.WithDescription("Open a new tab and return it, including the `id` to pass as tabId to every other tool. "+
				"Use this to get a tab of your own instead of taking over one the user is reading. "+
				"With a url, waits for the page to load (up to 15s)."),
			mcp.WithString("url", mcp.Description("URL to open (omit for a blank new tab)")),
			mcp.WithBoolean("active", mcp.Description("Focus the new tab (default true). Pass false to open it in the background.")),
			mcp.WithAny("nextTo",
				mcp.Description("Open right after this tab, in its window and browser (tab ID from list_tabs)"),
				func(schema map[string]any) { schema["type"] = []string{"string", "number"} },
			),
			mcp.WithString("browser", mcp.Description(
				"Which browser to open the tab in when several are connected and nextTo is not given: "+
					"the prefix of that browser's tab IDs (\"zen-3f9a1c2b\") or its name (\"Zen\").")),
		),
		handleOpenTab,
	)

	// --- close_tabs ---
	addTool(s,
		mcp.NewTool("close_tabs",
			mcp.WithDescription("Close tabs. Pinned tabs are left open and reported under `skipped` unless includePinned is set. "+
				"All tabs in one call must belong to the same browser."),
			tabRefsParam("tabIds", "Tab IDs from list_tabs to close"),
			mcp.WithBoolean("includePinned", mcp.Description("Also close pinned tabs (default false)")),
		),
		handleCloseTabs,
	)

	// --- move_tabs ---
	addTool(s,
		mcp.NewTool("move_tabs",
			mcp.WithDescription("Reorder the tab strip. The listed tabs end up side by side, in the order given: "+
				"the first goes right after `after`, right before `before`, or to `index` (default: the end), "+
				"and each next one right after the previous. To sort a whole window, list every tab in the order "+
				"you want and leave the position out. Returns each tab's resulting index: the browser keeps pinned "+
				"tabs ahead of unpinned ones, so a move across that boundary is clamped. "+
				"All tabs in one call must belong to the same browser."),
			tabRefsParam("tabIds", "Tab IDs from list_tabs, in the order they should end up"),
			mcp.WithAny("after",
				mcp.Description("Place the tabs right after this tab (may be in another window of the same browser)"),
				func(schema map[string]any) { schema["type"] = []string{"string", "number"} },
			),
			mcp.WithAny("before",
				mcp.Description("Place the tabs right before this tab"),
				func(schema map[string]any) { schema["type"] = []string{"string", "number"} },
			),
			mcp.WithNumber("index", mcp.Description("Position for the first tab when neither after nor before is given (0 = first, -1 = end; default -1)")),
			mcp.WithNumber("windowId", mcp.Description("With index: move the tabs to this window (windowId from list_tabs)")),
		),
		handleMoveTabs,
	)

	// --- wake_tab ---
	addTool(s,
		mcp.NewTool("wake_tab",
			mcp.WithDescription("Make a background tab usable WITHOUT bringing it forward. Browsers unload idle tabs "+
				"(list_tabs shows them as discarded) and do not load restored tabs until they are first shown; "+
				"page tools fail on such a tab. This loads it in place, waits for the page, and reports "+
				"`loaded` and `interactive` (false with a `reason` for pages no extension can script, e.g. browser "+
				"settings or the PDF viewer). Focus and window order are untouched. Call it before reading or "+
				"driving a tab the user is not looking at. `screenshot` still needs the tab in front."),
			tabIDParam(),
			mcp.WithNumber("timeoutMs", mcp.Description("How long to wait for the page to load (default 20000)")),
		),
		handleWakeTab,
	)
}

func handleWakeTab(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	wait := toInt(args["timeoutMs"])
	if wait <= 0 {
		wait = wakeTabDefaultMs
	}
	wait = min(wait, wakeTabMaxMs)
	args["timeoutMs"] = wait // the extension waits exactly as long as we allow for
	// Leave the extension room to report a load that ran to its own timeout.
	raw, err := send("wake_tab", rawArgs(args), wait+wakeTabReplySlackMs)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	// The extension answers with its own numeric id; hand back the ref the
	// caller used so the result can be passed on as is.
	var reply map[string]any
	if json.Unmarshal(raw, &reply) != nil {
		return mcp.NewToolResultText(string(raw)), nil
	}
	reply["id"] = args["tabId"]
	return textResult(reply)
}

func handleOpenTab(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	raw, err := send("open_tab", rawArgs(req.GetArguments()))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(raw)), nil
}

func handleCloseTabs(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return sendTabList("close_tabs", req, 30000)
}

func handleMoveTabs(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	if args["after"] != nil && args["before"] != nil {
		return mcp.NewToolResultError("Pass either after or before, not both"), nil
	}
	return sendTabList("move_tabs", req, moveTabsTimeoutMs)
}

func sendTabList(action string, req mcp.CallToolRequest, timeoutMs int) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	if ids, _ := args["tabIds"].([]any); len(ids) == 0 {
		return mcp.NewToolResultError("tabIds is required: a non-empty list of tab IDs from list_tabs"), nil
	}
	raw, err := send(action, rawArgs(args), timeoutMs)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(raw)), nil
}

// --- routing (runs where the browser connections live) ---

// browserForRef finds the browser a tab ref belongs to and the tab's ID
// within it. A bare number is only usable when it is unambiguous.
func browserForRef(open []*BrowserConnection, v any, timeout int) (*BrowserConnection, int, error) {
	key, id, ok := parseTabRef(v)
	if !ok {
		return nil, 0, fmt.Errorf("Invalid tab id %v: use an id from list_tabs, e.g. \"zen-3f9a1c2b:42\"", v)
	}
	if key != "" {
		for _, bc := range open {
			if bc.instanceKey() == key {
				return bc, id, nil
			}
		}
		return nil, 0, fmt.Errorf("Tab %v: that browser is not connected; run list_tabs for current ids", v)
	}
	if len(open) == 1 {
		return open[0], id, nil
	}
	found := owners(open, id)
	if len(found) == 0 {
		if _, err := sendListTabs(open, timeout, internalParams()); err == nil {
			found = owners(open, id)
		}
	}
	switch len(found) {
	case 0:
		return nil, 0, fmt.Errorf("Tab %d not found in any connected browser", id)
	case 1:
		return found[0], id, nil
	}
	return nil, 0, fmt.Errorf("Tab %d is ambiguous across %d connected browsers: pass the full id from list_tabs", id, len(open))
}

// browserByName picks the browser for a tab that does not exist yet.
func browserByName(open []*BrowserConnection, want string) (*BrowserConnection, error) {
	want = strings.ToLower(strings.TrimSpace(want))
	var found []*BrowserConnection
	prefixes := make([]string, len(open))
	for i, bc := range open {
		prefixes[i] = strconv.Quote(bc.tabPrefix())
		bc.mu.Lock()
		name := strings.ToLower(bc.name)
		bc.mu.Unlock()
		if want == "" || want == bc.tabPrefix() || want == bc.instanceKey() || want == name {
			found = append(found, bc)
		}
	}
	switch {
	case len(found) == 1:
		return found[0], nil
	case want == "":
		return nil, fmt.Errorf("%d browsers are connected: pass browser (%s) or nextTo", len(open), strings.Join(prefixes, " or "))
	case len(found) == 0:
		return nil, fmt.Errorf("No connected browser matches %q: pass one of %s", want, strings.Join(prefixes, " or "))
	}
	return nil, fmt.Errorf("%q matches %d connected browsers: pass one of %s", want, len(found), strings.Join(prefixes, " or "))
}

// prefixTabIDs rewrites the browser's own tab IDs in a tab-management reply
// into the refs list_tabs hands out, so they can be passed straight back.
func (bc *BrowserConnection) prefixTabIDs(raw json.RawMessage) json.RawMessage {
	var reply map[string]any
	if json.Unmarshal(raw, &reply) != nil {
		return raw
	}
	fix := func(v any) {
		m, _ := v.(map[string]any)
		if id, ok := m["id"].(float64); ok {
			m["id"] = bc.tabRef(int(id))
		}
	}
	fix(reply)
	for _, k := range []string{"tabs", "closed", "skipped"} {
		list, _ := reply[k].([]any)
		for _, item := range list {
			fix(item)
		}
	}
	out, err := json.Marshal(reply)
	if err != nil {
		return raw
	}
	return out
}

func sendOpenTab(open []*BrowserConnection, params map[string]any, timeout int) (json.RawMessage, error) {
	var bc *BrowserConnection
	var err error
	if anchor := params["nextTo"]; anchor != nil {
		var id int
		if bc, id, err = browserForRef(open, anchor, timeout); err != nil {
			return nil, err
		}
		params["nextTo"] = id
	} else if bc, err = browserByName(open, toString(params["browser"])); err != nil {
		return nil, err
	}
	delete(params, "browser")
	raw, err := sendTo(bc, "open_tab", params, timeout)
	if err != nil {
		return nil, err
	}
	var tab struct {
		ID     int  `json:"id"`
		Active bool `json:"active"`
	}
	if json.Unmarshal(raw, &tab) == nil && tab.ID != 0 {
		bc.mu.Lock()
		bc.tabIDs[tab.ID] = tab.Active
		bc.mu.Unlock()
	}
	return bc.prefixTabIDs(raw), nil
}

// sendTabSet routes move_tabs / close_tabs. Every tab they name, anchors
// included, has to live in one browser: a tab cannot move between browsers.
func sendTabSet(open []*BrowserConnection, action string, params map[string]any, timeout int) (json.RawMessage, error) {
	var target *BrowserConnection
	resolve := func(v any) (int, error) {
		bc, id, err := browserForRef(open, v, timeout)
		if err != nil {
			return 0, err
		}
		if target != nil && bc != target {
			return 0, fmt.Errorf("%s takes tabs from one browser per call, but got tabs of both %s and %s",
				action, target.tabPrefix(), bc.tabPrefix())
		}
		target = bc
		return id, nil
	}

	refs, _ := params["tabIds"].([]any)
	if len(refs) == 0 {
		return nil, fmt.Errorf("tabIds is required: a non-empty list of tab IDs from list_tabs")
	}
	ids := make([]int, len(refs))
	for i, ref := range refs {
		id, err := resolve(ref)
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}
	params["tabIds"] = ids
	for _, k := range []string{"after", "before"} {
		if params[k] == nil {
			delete(params, k)
			continue
		}
		id, err := resolve(params[k])
		if err != nil {
			return nil, err
		}
		params[k] = id
	}

	raw, err := sendTo(target, action, params, timeout)
	if err != nil {
		return nil, err
	}
	if action == "close_tabs" {
		var reply struct {
			Closed []struct {
				ID int `json:"id"`
			} `json:"closed"`
		}
		if json.Unmarshal(raw, &reply) == nil {
			target.mu.Lock()
			for _, t := range reply.Closed {
				delete(target.tabIDs, t.ID)
			}
			target.mu.Unlock()
		}
	}
	return target.prefixTabIDs(raw), nil
}
