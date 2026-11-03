package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestParseTabRef(t *testing.T) {
	cases := []struct {
		in     any
		key    string
		id     int
		wantOK bool
	}{
		{float64(42), "", 42, true},
		{"42", "", 42, true},
		{"zen-3f9a1c2b:42", "3f9a1c2b", 42, true},
		{"browser-1-c2:7", "c2", 7, true},
		{"firefox-81c2d0aa:x", "", 0, false},
		{":5", "", 5, false},
		{"nope", "", 0, false},
		{nil, "", 0, false},
	}
	for _, c := range cases {
		key, id, ok := parseTabRef(c.in)
		if key != c.key || id != c.id || ok != c.wantOK {
			t.Errorf("parseTabRef(%v) = %q, %d, %v; want %q, %d, %v", c.in, key, id, ok, c.key, c.id, c.wantOK)
		}
	}
}

// connectHelloBrowser is a mock extension that introduces itself with an
// instance ID, lists the given tab IDs and records which tabs it was sent.
func connectHelloBrowser(t *testing.T, wsURL, ua, instance string, tabs []int, got *[]int, mu *sync.Mutex) *websocket.Conn {
	t.Helper()
	conn := connectMockBrowser(t, wsURL, func(msg map[string]any) map[string]any {
		params, _ := msg["params"].(map[string]any)
		switch msg["action"] {
		case "list_tabs":
			list := make([]map[string]any, len(tabs))
			for i, id := range tabs {
				list[i] = map[string]any{"id": id, "title": instance, "active": false}
			}
			return map[string]any{"result": list}
		case "click":
			mu.Lock()
			*got = append(*got, toInt(params["tabId"]))
			mu.Unlock()
			return map[string]any{"result": instance}
		}
		return map[string]any{"result": nil}
	})
	hello, _ := json.Marshal(map[string]any{"type": "hello", "ua": ua, "instance": instance})
	if err := conn.WriteMessage(websocket.TextMessage, hello); err != nil {
		t.Fatalf("hello: %v", err)
	}
	return conn
}

func TestSend_SameTabIDInTwoBrowsers(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()

	srv, wsURL := startTestWSServer(t)
	defer srv.Close()

	var mu sync.Mutex
	var zenGot, ffGot []int
	zen := connectHelloBrowser(t, wsURL, "Zen/Mozilla/5.0 Firefox/144.0", "zen-profile", []int{7, 8}, &zenGot, &mu)
	defer zen.Close()
	ff := connectHelloBrowser(t, wsURL, "Mozilla/5.0 Firefox/144.0", "firefox-profile", []int{7, 9}, &ffGot, &mu)
	defer ff.Close()
	waitFor(t, func() bool {
		for _, bc := range getOpenBrowsers() {
			bc.mu.Lock()
			named := bc.instance != ""
			bc.mu.Unlock()
			if !named {
				return false
			}
		}
		return len(getOpenBrowsers()) == 2
	})

	raw, err := send("list_tabs", nil, 5000)
	if err != nil {
		t.Fatalf("list_tabs: %v", err)
	}
	var tabs []struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Browser string `json:"browser"`
	}
	if err := json.Unmarshal(raw, &tabs); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	refs := map[string]string{} // instance -> ref of its tab 7
	seen := map[string]bool{}
	for _, tab := range tabs {
		if seen[tab.ID] {
			t.Errorf("duplicate tab id %q in %s", tab.ID, raw)
		}
		seen[tab.ID] = true
		if strings.HasSuffix(tab.ID, ":7") {
			refs[tab.Title] = tab.ID
		}
	}
	if !strings.HasPrefix(refs["zen-profile"], "zen-") || !strings.HasPrefix(refs["firefox-profile"], "firefox-") {
		t.Fatalf("tab 7 refs = %v", refs)
	}

	// A prefixed ref reaches exactly the browser it came from, unprefixed.
	for inst, ref := range refs {
		out, err := send("click", map[string]any{"tabId": ref}, 5000)
		if err != nil {
			t.Fatalf("click %s: %v", ref, err)
		}
		var from string
		json.Unmarshal(out, &from)
		if from != inst {
			t.Errorf("click %s went to %s", ref, from)
		}
	}
	mu.Lock()
	if len(zenGot) != 1 || zenGot[0] != 7 || len(ffGot) != 1 || ffGot[0] != 7 {
		t.Errorf("zen got %v, firefox got %v; want [7] each", zenGot, ffGot)
	}
	mu.Unlock()

	// A bare shared ID is refused rather than guessed.
	_, err = send("click", map[string]any{"tabId": float64(7)}, 5000)
	if err == nil || !strings.Contains(err.Error(), refs["zen-profile"]) || !strings.Contains(err.Error(), refs["firefox-profile"]) {
		t.Errorf("bare shared id: err = %v", err)
	}

	// A bare ID only one browser has still works.
	out, err := send("click", map[string]any{"tabId": float64(9)}, 5000)
	var from string
	json.Unmarshal(out, &from)
	if err != nil || from != "firefox-profile" {
		t.Errorf("bare unique id: from %q, err %v", from, err)
	}

	// Refs survive a reconnect: the key comes from the instance ID.
	zen.Close()
	waitFor(t, func() bool { return len(getOpenBrowsers()) == 1 })
	var again []int
	zen2 := connectHelloBrowser(t, wsURL, "Zen/Mozilla/5.0 Firefox/144.0", "zen-profile", []int{7}, &again, &mu)
	defer zen2.Close()
	waitFor(t, func() bool { return len(getOpenBrowsers()) == 2 })
	waitFor(t, func() bool {
		_, err := send("click", map[string]any{"tabId": refs["zen-profile"]}, 5000)
		return err == nil
	})
	mu.Lock()
	if len(again) == 0 || again[0] != 7 {
		t.Errorf("after reconnect, zen got %v", again)
	}
	mu.Unlock()

	// A ref for a browser that isn't connected is an error, not a guess.
	if _, err := send("click", map[string]any{"tabId": "zen-00000000:7"}, 5000); err == nil {
		t.Error("unknown browser key: want error")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 3s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
