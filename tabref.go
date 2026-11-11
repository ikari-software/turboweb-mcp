package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Tab IDs are only unique within one browser: Firefox and Zen can both have
// a tab 122. list_tabs therefore hands out tab refs of the form
// "<browser>-<key>:<id>" (e.g. "zen-3f9a1c2b:122"), where key is derived from
// the instance ID the extension mints once per profile. Routing goes by key;
// the browser name is only there for humans, so a late rename can't break it.

// instanceKey is the routing part of this browser's tab refs. It is stable
// across reconnects and daemon restarts as long as the extension reports an
// instance ID; older extensions fall back to a per-connection sequence number.
func (bc *BrowserConnection) instanceKey() string {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	if bc.instance == "" {
		return "c" + strconv.Itoa(bc.seq)
	}
	sum := sha256.Sum256([]byte(bc.instance))
	return hex.EncodeToString(sum[:4])
}

// tabPrefix is the "<browser>-<key>" part of this browser's tab refs.
func (bc *BrowserConnection) tabPrefix() string {
	bc.mu.Lock()
	name := bc.name
	bc.mu.Unlock()
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		b.WriteString("browser")
	}
	return b.String() + "-" + bc.instanceKey()
}

func (bc *BrowserConnection) tabRef(id int) string {
	return bc.tabPrefix() + ":" + strconv.Itoa(id)
}

// parseTabRef splits a tabId argument into the browser key and the browser's
// own tab ID. Plain numbers (and numeric strings) have no key.
func parseTabRef(v any) (key string, id int, ok bool) {
	switch n := v.(type) {
	case float64:
		return "", int(n), true
	case int:
		return "", n, true
	case int64:
		return "", int(n), true
	case json.Number:
		i, err := n.Int64()
		return "", int(i), err == nil
	case string:
		s := strings.TrimSpace(n)
		if i := strings.LastIndexByte(s, ':'); i >= 0 {
			prefix, num := s[:i], s[i+1:]
			id, err := strconv.Atoi(num)
			if err != nil {
				return "", 0, false
			}
			key = prefix[strings.LastIndexByte(prefix, '-')+1:]
			return key, id, key != ""
		}
		id, err := strconv.Atoi(s)
		return "", id, err == nil
	}
	return "", 0, false
}

// tabNum is the browser's own tab ID for a tabId argument, prefix dropped
// (0 when absent or malformed).
func tabNum(v any) int {
	_, id, _ := parseTabRef(v)
	return id
}

// tabIDParam declares the tabId argument every tab-scoped tool takes. It
// accepts the string refs from list_tabs and, for one-browser setups and older
// clients, plain numbers. addTool marks it required and guards the handler.
func tabIDParam() mcp.ToolOption {
	return mcp.WithAny("tabId",
		mcp.Description(`Required. Tab ID from list_tabs or open_tab, e.g. "zen-3f9a1c2b:42"`),
		func(schema map[string]any) { schema["type"] = []string{"string", "number"} },
	)
}

// tabRefsParam declares an argument holding a list of tab IDs.
func tabRefsParam(name, description string) mcp.ToolOption {
	return mcp.WithArray(name,
		mcp.Required(),
		mcp.Description(description),
		mcp.Items(map[string]any{"type": []string{"string", "number"}}),
	)
}

// tabIDRequiredMessage is returned instead of running a tool that was called
// without a tabId. Written for the model: it says where IDs come from and
// that a retry will work.
const tabIDRequiredMessage = "Refused: tabId is required. There is no implicit \"active tab\": " +
	"the user keeps browsing while you work, so the focused tab is not a stable target. " +
	"Call `list_tabs` to pick an existing tab, or `open_tab` to get a fresh one, " +
	"then retry with that id as tabId (e.g. \"zen-3f9a1c2b:42\")."

// requireTabID wraps a tool handler so it refuses to run without a usable
// tabId. mcp-go does not enforce schema Required at the call layer, so the
// schema alone would let a missing tabId fall through to the focused tab.
func requireTabID(name string, h server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		v, present := req.GetArguments()["tabId"]
		if !present || v == nil || v == "" {
			logger.Printf("Refused %s: no tabId", name)
			return mcp.NewToolResultError(tabIDRequiredMessage), nil
		}
		if _, id, ok := parseTabRef(v); !ok || id <= 0 {
			return mcp.NewToolResultError(fmt.Sprintf(
				"Invalid tabId %v: use an id from list_tabs or open_tab, e.g. \"zen-3f9a1c2b:42\"", v)), nil
		}
		return h(ctx, req)
	}
}
