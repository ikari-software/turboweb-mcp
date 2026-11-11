package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestRegisteredTools_TabIDIsRequired(t *testing.T) {
	s := server.NewMCPServer("test", "1.0.0", server.WithToolCapabilities(false))
	registerAllTools(s)
	withTab := 0
	for name, st := range s.ListTools() {
		if _, ok := st.Tool.InputSchema.Properties["tabId"]; !ok {
			continue
		}
		withTab++
		if !slices.Contains(st.Tool.InputSchema.Required, "tabId") {
			t.Errorf("%s declares tabId but does not require it", name)
		}
	}
	if withTab == 0 {
		t.Fatal("no registered tool declares tabId")
	}
	for _, name := range []string{"open_tab", "close_tabs", "move_tabs", "wake_tab"} {
		if s.GetTool(name) == nil {
			t.Errorf("%s is not registered", name)
		}
	}
}

func TestRequireTabID(t *testing.T) {
	ran := false
	h := requireTabID("click", func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ran = true
		return mcp.NewToolResultText("ok"), nil
	})
	for _, c := range []struct {
		args    map[string]any
		wantRun bool
		wantMsg string
	}{
		{map[string]any{}, false, "tabId is required"},
		{map[string]any{"tabId": nil}, false, "tabId is required"},
		{map[string]any{"tabId": ""}, false, "tabId is required"},
		{map[string]any{"tabId": float64(0)}, false, "Invalid tabId"},
		{map[string]any{"tabId": "zen-3f9a1c2b:x"}, false, "Invalid tabId"},
		{map[string]any{"tabId": "zen-3f9a1c2b:42"}, true, "ok"},
		{map[string]any{"tabId": float64(42)}, true, "ok"},
	} {
		ran = false
		res, err := h(context.Background(), reqWith(c.args))
		if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if ran != c.wantRun || !strings.Contains(extractText(t, res), c.wantMsg) {
			t.Errorf("%v: ran=%v, text=%q; want ran=%v, text containing %q",
				c.args, ran, extractText(t, res), c.wantRun, c.wantMsg)
		}
	}
}

// tabBrowser is a mock extension that answers the tab-management commands and
// records the params each one arrived with.
type tabBrowser struct {
	mu   sync.Mutex
	got  map[string]map[string]any
	tabs []int
}

func (b *tabBrowser) connect(t *testing.T, wsURL, ua, instance string) *websocket.Conn {
	t.Helper()
	b.got = map[string]map[string]any{}
	conn := connectMockBrowser(t, wsURL, func(msg map[string]any) map[string]any {
		action, _ := msg["action"].(string)
		params, _ := msg["params"].(map[string]any)
		b.mu.Lock()
		b.got[action] = params
		b.mu.Unlock()
		switch action {
		case "list_tabs":
			list := make([]map[string]any, len(b.tabs))
			for i, id := range b.tabs {
				list[i] = map[string]any{"id": id, "title": instance}
			}
			return map[string]any{"result": list}
		case "open_tab":
			return map[string]any{"result": map[string]any{"id": 77, "active": true, "index": 3}}
		case "close_tabs":
			return map[string]any{"result": map[string]any{
				"closed":  []map[string]any{{"id": 7}},
				"skipped": []map[string]any{{"id": 8, "reason": "pinned"}},
			}}
		case "wake_tab":
			return map[string]any{"result": map[string]any{"id": params["tabId"], "loaded": true, "interactive": true}}
		case "move_tabs":
			return map[string]any{"result": map[string]any{
				"tabs": []map[string]any{{"id": 8, "index": 0}, {"id": 7, "index": 1}},
			}}
		}
		return map[string]any{"result": nil}
	})
	hello, _ := json.Marshal(map[string]any{"type": "hello", "ua": ua, "instance": instance})
	if err := conn.WriteMessage(websocket.TextMessage, hello); err != nil {
		t.Fatalf("hello: %v", err)
	}
	return conn
}

func (b *tabBrowser) params(action string) map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.got[action]
}

// withZenAndFirefox runs fn against two connected mock browsers that both
// have a tab 7, and hands it the tab-ref prefix of each.
func withZenAndFirefox(t *testing.T, fn func(zen, ff *tabBrowser, zenPrefix, ffPrefix string)) {
	t.Helper()
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()
	srv, wsURL := startTestWSServer(t)
	defer srv.Close()

	zen, ff := &tabBrowser{tabs: []int{7, 8}}, &tabBrowser{tabs: []int{7, 9}}
	defer zen.connect(t, wsURL, "Zen/Mozilla/5.0 Firefox/144.0", "zen-profile").Close()
	defer ff.connect(t, wsURL, "Mozilla/5.0 Firefox/144.0", "firefox-profile").Close()
	var zenPrefix, ffPrefix string
	waitFor(t, func() bool {
		zenPrefix, ffPrefix = "", ""
		for _, bc := range getOpenBrowsers() {
			switch p := bc.tabPrefix(); {
			case strings.HasPrefix(p, "zen-"):
				zenPrefix = p
			case strings.HasPrefix(p, "firefox-"):
				ffPrefix = p
			}
		}
		return zenPrefix != "" && ffPrefix != ""
	})
	fn(zen, ff, zenPrefix, ffPrefix)
}

func TestMoveTabs_RoutesToOwningBrowserAndRewritesIDs(t *testing.T) {
	withZenAndFirefox(t, func(zen, ff *tabBrowser, zenPrefix, _ string) {
		res, err := handleMoveTabs(context.Background(), reqWith(map[string]any{
			"tabIds": []any{zenPrefix + ":8", zenPrefix + ":7"},
			"after":  zenPrefix + ":7",
		}))
		if err != nil {
			t.Fatal(err)
		}
		if ff.params("move_tabs") != nil {
			t.Error("move_tabs for Zen tabs reached Firefox")
		}
		got := zen.params("move_tabs")
		if fmt.Sprint(got["tabIds"]) != "[8 7]" || toInt(got["after"]) != 7 {
			t.Errorf("Zen received tabIds=%v after=%v; want the browser's own ids [8,7] and 7", got["tabIds"], got["after"])
		}
		text := extractText(t, res)
		if !strings.Contains(text, `"`+zenPrefix+`:8"`) || !strings.Contains(text, `"`+zenPrefix+`:7"`) {
			t.Errorf("result should carry prefixed tab ids, got %s", text)
		}
	})
}

func TestMoveTabs_RefusesTabsOfTwoBrowsers(t *testing.T) {
	withZenAndFirefox(t, func(zen, ff *tabBrowser, zenPrefix, ffPrefix string) {
		res, _ := handleMoveTabs(context.Background(), reqWith(map[string]any{
			"tabIds": []any{zenPrefix + ":7", ffPrefix + ":7"},
		}))
		if !res.IsError || !strings.Contains(extractText(t, res), "one browser per call") {
			t.Errorf("want a one-browser error, got %q", extractText(t, res))
		}
		if zen.params("move_tabs") != nil || ff.params("move_tabs") != nil {
			t.Error("nothing should be moved when the call is refused")
		}
	})
}

func TestMoveTabs_RefusesAmbiguousBareID(t *testing.T) {
	withZenAndFirefox(t, func(_, _ *tabBrowser, _, _ string) {
		res, _ := handleMoveTabs(context.Background(), reqWith(map[string]any{"tabIds": []any{float64(7)}}))
		if !res.IsError || !strings.Contains(extractText(t, res), "ambiguous") {
			t.Errorf("bare id present in both browsers should be refused, got %q", extractText(t, res))
		}
	})
}

func TestCloseTabs_BareIDAcrossTwoBrowsers(t *testing.T) {
	withZenAndFirefox(t, func(zen, ff *tabBrowser, _, _ string) {
		for id, want := range map[int]string{
			999: "Tab 999 not found in any connected browser", // nobody has it
			7:   "ambiguous",                                  // both have it
		} {
			res, err := handleCloseTabs(context.Background(), reqWith(map[string]any{"tabIds": []any{id}}))
			if err != nil {
				t.Fatal(err)
			}
			if !res.IsError || !strings.Contains(extractText(t, res), want) {
				t.Errorf("close_tabs %d: got %s; want an error containing %q", id, extractText(t, res), want)
			}
		}
		if zen.params("close_tabs") != nil || ff.params("close_tabs") != nil {
			t.Error("no browser should have been asked to close anything")
		}
	})
}

func TestCloseTabs_RewritesClosedAndSkippedIDs(t *testing.T) {
	withZenAndFirefox(t, func(_, ff *tabBrowser, _, ffPrefix string) {
		res, err := handleCloseTabs(context.Background(), reqWith(map[string]any{
			"tabIds": []any{ffPrefix + ":7", ffPrefix + ":8"},
		}))
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(ff.params("close_tabs")["tabIds"]) != "[7 8]" {
			t.Errorf("Firefox received tabIds=%v", ff.params("close_tabs")["tabIds"])
		}
		var reply struct {
			Closed, Skipped []struct {
				ID string `json:"id"`
			}
		}
		if err := json.Unmarshal([]byte(extractText(t, res)), &reply); err != nil {
			t.Fatalf("parse %s: %v", extractText(t, res), err)
		}
		if len(reply.Closed) != 1 || reply.Closed[0].ID != ffPrefix+":7" ||
			len(reply.Skipped) != 1 || reply.Skipped[0].ID != ffPrefix+":8" {
			t.Errorf("got %s", extractText(t, res))
		}
	})
}

func TestCloseTabs_EmptyListErrors(t *testing.T) {
	res, _ := handleCloseTabs(context.Background(), reqWith(map[string]any{"tabIds": []any{}}))
	if !res.IsError || !strings.Contains(extractText(t, res), "tabIds is required") {
		t.Errorf("got %q", extractText(t, res))
	}
}

func TestOpenTab_PicksBrowserAndReturnsUsableID(t *testing.T) {
	withZenAndFirefox(t, func(zen, ff *tabBrowser, zenPrefix, ffPrefix string) {
		// Two browsers and no hint: refuse rather than guess.
		res, _ := handleOpenTab(context.Background(), reqWith(map[string]any{"url": "https://example.com/"}))
		if !res.IsError || !strings.Contains(extractText(t, res), zenPrefix) || !strings.Contains(extractText(t, res), ffPrefix) {
			t.Errorf("want an error naming both browsers, got %q", extractText(t, res))
		}

		for _, browser := range []string{"Zen", zenPrefix} {
			res, _ = handleOpenTab(context.Background(), reqWith(map[string]any{"browser": browser}))
			if !strings.Contains(extractText(t, res), `"id":"`+zenPrefix+`:77"`) {
				t.Errorf("browser=%q: want id %s:77, got %s", browser, zenPrefix, extractText(t, res))
			}
		}
		if _, leaked := zen.params("open_tab")["browser"]; leaked {
			t.Error("the browser argument should not be forwarded to the extension")
		}

		// nextTo routes to the anchor's browser and arrives as its own id.
		res, _ = handleOpenTab(context.Background(), reqWith(map[string]any{"nextTo": ffPrefix + ":9"}))
		if !strings.Contains(extractText(t, res), `"id":"`+ffPrefix+`:77"`) || toInt(ff.params("open_tab")["nextTo"]) != 9 {
			t.Errorf("nextTo: got %s, Firefox params %v", extractText(t, res), ff.params("open_tab"))
		}

		// The new tab is addressable by its ref straight away.
		if _, err := send("click", map[string]any{"tabId": ffPrefix + ":77"}, 5000); err != nil {
			t.Errorf("click on the new tab: %v", err)
		}
		if toInt(ff.params("click")["tabId"]) != 77 {
			t.Errorf("click reached Firefox with %v", ff.params("click"))
		}
	})
}

func TestMoveTabs_RefusesAfterTogetherWithBefore(t *testing.T) {
	withZenAndFirefox(t, func(zen, _ *tabBrowser, zenPrefix, _ string) {
		res, _ := handleMoveTabs(context.Background(), reqWith(map[string]any{
			"tabIds": []any{zenPrefix + ":8"}, "after": zenPrefix + ":7", "before": zenPrefix + ":7",
		}))
		if !res.IsError || !strings.Contains(extractText(t, res), "either after or before") {
			t.Errorf("got %q", extractText(t, res))
		}
		if zen.params("move_tabs") != nil {
			t.Error("nothing should be moved when the call is refused")
		}
	})
}

func TestMoveTabs_RefusesAnchorInAnotherBrowser(t *testing.T) {
	withZenAndFirefox(t, func(zen, ff *tabBrowser, zenPrefix, ffPrefix string) {
		res, _ := handleMoveTabs(context.Background(), reqWith(map[string]any{
			"tabIds": []any{zenPrefix + ":8"}, "after": ffPrefix + ":9",
		}))
		if !res.IsError || !strings.Contains(extractText(t, res), "one browser per call") {
			t.Errorf("got %q", extractText(t, res))
		}
		if zen.params("move_tabs") != nil || ff.params("move_tabs") != nil {
			t.Error("nothing should be moved when the call is refused")
		}
	})
}

func TestTabTools_RefuseABrowserThatIsNotConnected(t *testing.T) {
	withZenAndFirefox(t, func(zen, ff *tabBrowser, zenPrefix, ffPrefix string) {
		res, _ := handleCloseTabs(context.Background(), reqWith(map[string]any{"tabIds": []any{"zen-deadbeef:7"}}))
		if !res.IsError || !strings.Contains(extractText(t, res), "that browser is not connected") {
			t.Errorf("close_tabs: got %q", extractText(t, res))
		}
		if zen.params("close_tabs") != nil || ff.params("close_tabs") != nil {
			t.Error("no browser should have been asked to close anything")
		}

		res, _ = handleOpenTab(context.Background(), reqWith(map[string]any{"browser": "Safari"}))
		text := extractText(t, res)
		if !res.IsError || !strings.Contains(text, `No connected browser matches "safari"`) ||
			!strings.Contains(text, zenPrefix) || !strings.Contains(text, ffPrefix) {
			t.Errorf("open_tab: got %q", text)
		}
	})
}

// A closed tab leaves the owner cache, so its number stops counting towards
// "which browser has tab 7".
func TestCloseTabs_ForgetsTheClosedTab(t *testing.T) {
	withZenAndFirefox(t, func(zen, ff *tabBrowser, _, ffPrefix string) {
		if _, err := send("list_tabs", nil, 5000); err != nil {
			t.Fatal(err)
		}
		if _, err := handleCloseTabs(context.Background(), reqWith(map[string]any{"tabIds": []any{ffPrefix + ":7"}})); err != nil {
			t.Fatal(err)
		}
		res, _ := handleMoveTabs(context.Background(), reqWith(map[string]any{"tabIds": []any{float64(7)}}))
		if res.IsError || zen.params("move_tabs") == nil || ff.params("move_tabs") != nil {
			t.Errorf("bare 7 should now mean Zen's tab, got %q", extractText(t, res))
		}
	})
}

func TestWakeTab_ForwardsTheWaitAndReturnsTheCallersRef(t *testing.T) {
	withZenAndFirefox(t, func(zen, ff *tabBrowser, zenPrefix, _ string) {
		for _, c := range []struct {
			args map[string]any
			want int
		}{
			{map[string]any{}, wakeTabDefaultMs},
			{map[string]any{"timeoutMs": float64(5000)}, 5000},
			{map[string]any{"timeoutMs": float64(9999999)}, wakeTabMaxMs},
			{map[string]any{"timeoutMs": float64(-1)}, wakeTabDefaultMs},
		} {
			c.args["tabId"] = zenPrefix + ":8"
			res, err := handleWakeTab(context.Background(), reqWith(c.args))
			if err != nil {
				t.Fatal(err)
			}
			got := zen.params("wake_tab")
			if toInt(got["tabId"]) != 8 || toInt(got["timeoutMs"]) != c.want {
				t.Errorf("Zen received tabId=%v timeoutMs=%v; want 8 and %d", got["tabId"], got["timeoutMs"], c.want)
			}
			if text := extractText(t, res); res.IsError || !strings.Contains(text, `"`+zenPrefix+`:8"`) {
				t.Errorf("result should carry the ref the caller used, got %s", text)
			}
		}
		if ff.params("wake_tab") != nil {
			t.Error("wake_tab for a Zen tab reached Firefox")
		}
	})
}

func TestBidiTabList_ListsTopLevelContextsWithUsableIDs(t *testing.T) {
	applyContextTree([]BiDiContextInfo{
		{Context: "ctx-a", URL: "https://a.example/", Children: []BiDiContextInfo{{Context: "frame-1", URL: "https://ads.example/"}}},
		{Context: "ctx-b", URL: "https://b.example/"},
	})
	defer applyContextTree(nil)

	tabs := bidiTabList()
	if len(tabs) != 2 {
		t.Fatalf("want the 2 top-level contexts and no frames, got %v", tabs)
	}
	for _, tab := range tabs {
		// The id has to pass the tabId guard and lead back to the context.
		contextMapMu.RLock()
		ctx := tabToContext[toInt(tab["id"])]
		contextMapMu.RUnlock()
		if _, id, ok := parseTabRef(tab["id"]); !ok || id <= 0 || contextURLs[ctx] != tab["url"] {
			t.Errorf("tab %v does not resolve to its own context (got %q)", tab, ctx)
		}
	}
}
