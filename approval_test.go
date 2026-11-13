package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// relayAgent is a bare relay client: what any local program that knows the
// protocol could be.
type relayAgent struct {
	t    *testing.T
	conn *websocket.Conn
}

func dialRelay(t *testing.T, wsURL string, register map[string]any) *relayAgent {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL+"/relay", nil)
	if err != nil {
		t.Fatalf("relay dial: %v", err)
	}
	a := &relayAgent{t, conn}
	if register != nil {
		register["type"] = "register"
		a.write(register)
	}
	return a
}

func (a *relayAgent) write(v any) {
	a.t.Helper()
	b, _ := json.Marshal(v)
	if err := a.conn.WriteMessage(websocket.TextMessage, b); err != nil {
		a.t.Fatalf("relay write: %v", err)
	}
}

// call sends one command and waits for its reply.
func (a *relayAgent) call(action string, params map[string]any) {
	a.t.Helper()
	a.write(map[string]any{"id": "1", "action": action, "params": params, "timeout": 5000})
	if _, _, err := a.conn.ReadMessage(); err != nil {
		a.t.Fatalf("relay read: %v", err)
	}
}

func TestRelay_StampsEachCommandWithTheClientItCameFrom(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()
	srv, wsURL := startTestDaemon(t)
	defer srv.Close()
	browser := &tabBrowser{}
	defer browser.connect(t, wsURL, "Mozilla/5.0 Firefox/144.0", "p").Close()
	waitFor(t, func() bool { return len(getOpenBrowsers()) == 1 })

	stampOf := func(a *relayAgent) map[string]any {
		// A client must not be able to name itself as someone already approved.
		a.call("click", map[string]any{"_client": map[string]any{"id": "forged", "remote": "nowhere"}})
		stamp, _ := browser.params("click")["_client"].(map[string]any)
		if stamp == nil {
			t.Fatalf("command reached the browser without a _client stamp: %v", browser.params("click"))
		}
		return stamp
	}

	first := dialRelay(t, wsURL, map[string]any{"key": "secret-1", "displayName": "Claude", "task": "Sorting tabs", "pid": 42})
	stamp := stampOf(first)
	if stamp["id"] != clientIDFromKey("secret-1") || stamp["name"] != "Claude" || stamp["task"] != "Sorting tabs" {
		t.Errorf("stamp = %v", stamp)
	}
	if remote, _ := stamp["remote"].(string); !strings.HasPrefix(remote, "127.0.0.1:") {
		t.Errorf("remote should be the peer address the daemon saw, got %q", remote)
	}
	if strings.Contains(toJSON(stamp), "secret-1") {
		t.Error("the client's key must not reach the browser")
	}
	first.conn.Close()

	// The same process reconnecting keeps its identity; another one does not get it.
	again := dialRelay(t, wsURL, map[string]any{"key": "secret-1"})
	defer again.conn.Close()
	if id := stampOf(again)["id"]; id != stamp["id"] {
		t.Errorf("same key, different id: %v vs %v", id, stamp["id"])
	}
	other := dialRelay(t, wsURL, nil) // never registers
	defer other.conn.Close()
	if id, _ := stampOf(other)["id"].(string); id == "" || id == stamp["id"] || id == "forged" {
		t.Errorf("an unregistered client got id %q", id)
	}
}

func TestRelay_CannotMarkItsOwnCommandInternal(t *testing.T) {
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()
	srv, wsURL := startTestDaemon(t)
	defer srv.Close()
	browser := &tabBrowser{}
	defer browser.connect(t, wsURL, "Mozilla/5.0 Firefox/144.0", "p").Close()
	waitFor(t, func() bool { return len(getOpenBrowsers()) == 1 })

	a := dialRelay(t, wsURL, map[string]any{"key": "secret-1"})
	defer a.conn.Close()
	// _internal is the flag the extension reads to skip asking its user. A
	// relay client that sets it would drive the browser with no prompt, so the
	// daemon must strip it before the command reaches the browser.
	a.call("click", map[string]any{"_internal": true})
	got := browser.params("click")
	if got["_internal"] != nil {
		t.Errorf("a relay client marked its own command internal: %v", got)
	}
	if got["_client"] == nil {
		t.Errorf("the command lost its stamp: %v", got)
	}
	// The same goes for the identity the prompt displays.
	a.call("click", map[string]any{"_clientName": "spoofed", "_clientHue": 1})
	if got := browser.params("click"); got["_clientName"] == "spoofed" || got["_clientHue"] == float64(1) {
		t.Errorf("a relay client named itself: %v", got)
	}
}

func TestRawArgs_StripsServerOwnedKeys(t *testing.T) {
	// No tool takes an underscore-prefixed argument; every one of these is the
	// daemon's to set (see reservedParam). An agent that can set them can skip
	// the approval gate or write its own consent prompt.
	got := rawArgs(map[string]any{
		"tabId":       4,
		"intent":      "Clicking submit",
		"_internal":   true,
		"_client":     map[string]any{"id": "already-approved"},
		"_clientName": "spoofed",
		"_intent":     "forged narration",
	})
	for _, k := range []string{"_internal", "_client", "_clientName"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s survived rawArgs: %v", k, got)
		}
	}
	if got["tabId"] != 4 {
		t.Errorf("tabId was dropped: %v", got)
	}
	if got["_intent"] != "Clicking submit" {
		t.Errorf("the agent's own intent should be rewritten as _intent, got %v", got["_intent"])
	}

	// The no-intent path used to hand back the caller's own map, so sendDirect
	// would stamp identity by mutating the arguments the agent supplied.
	args := map[string]any{"_internal": true}
	if out := rawArgs(args); len(out) != 0 {
		t.Errorf("reserved keys survived without an intent: %v", out)
	}
	if len(args) != 1 {
		t.Error("rawArgs must not mutate the caller's map")
	}
}

func TestSendDirect_StampsTheLocalAgentAndMarksItsOwnBookkeeping(t *testing.T) {
	withZenAndFirefox(t, func(zen, ff *tabBrowser, zenPrefix, _ string) {
		if _, err := send("click", map[string]any{"tabId": zenPrefix + ":8"}, 5000); err != nil {
			t.Fatal(err)
		}
		stamp, _ := zen.params("click")["_client"].(map[string]any)
		if stamp["id"] != clientIDFromKey(sessionKey) || stamp["remote"] != "in-process" {
			t.Errorf("stamp = %v", stamp)
		}

		// An agent's list_tabs says who is asking...
		if _, err := send("list_tabs", nil, 5000); err != nil {
			t.Fatal(err)
		}
		if zen.params("list_tabs")["_client"] == nil || zen.params("list_tabs")["_internal"] != nil {
			t.Errorf("agent list_tabs params = %v", zen.params("list_tabs"))
		}
		// ...while the daemon refreshing its tab cache does not pose as one.
		if _, err := sendListTabs(getOpenBrowsers(), 5000, internalParams()); err != nil {
			t.Fatal(err)
		}
		if zen.params("list_tabs")["_internal"] != true || ff.params("list_tabs")["_client"] != nil {
			t.Errorf("internal list_tabs params = %v", zen.params("list_tabs"))
		}
	})
}

func TestConnectionStatus_ReportsWhereTheAgentStands(t *testing.T) {
	withZenAndFirefox(t, func(zen, ff *tabBrowser, _, _ string) {
		var status map[string]any
		read := func() []any {
			raw, err := send("connection_status", nil, 3000)
			if err != nil {
				t.Fatalf("connection_status: %v", err)
			}
			status = map[string]any{}
			if err := json.Unmarshal(raw, &status); err != nil {
				t.Fatalf("connection_status is not an object: %v", err)
			}
			list, _ := status["approval"].([]any)
			return list
		}

		// Both browsers answer, and the agent can tell pending from approved.
		zen.approval, ff.approval = "pending", "approved"
		list := read()
		if len(list) != 2 {
			t.Fatalf("expected one entry per browser, got %v (approval=%v)", len(list), status["approval"])
		}
		states := map[string]string{}
		for _, e := range list {
			m, _ := e.(map[string]any)
			ref, _ := m["browser"].(string)
			st, _ := m["state"].(string)
			states[ref] = st
		}
		var pending, approved bool
		for ref, st := range states {
			switch {
			case strings.HasPrefix(ref, "zen-") && st == "pending":
				pending = true
			case strings.HasPrefix(ref, "firefox-") && st == "approved":
				approved = true
			}
		}
		if !pending || !approved {
			t.Errorf("states = %v, want zen pending and firefox approved", states)
		}

		// A browser on an old extension simply contributes nothing; it must not
		// break a diagnostic or make the command look disconnected.
		ff.approval = ""
		if list := read(); len(list) != 1 {
			t.Errorf("an old extension should be left out, got %v", list)
		}
		if status["extension"] != true {
			t.Errorf("a silent approval probe must not make the browser look gone: %v", status)
		}
	})
}

func TestConnectionStatus_MarksTheSkipApprovalOptOut(t *testing.T) {
	withZenAndFirefox(t, func(zen, _ *tabBrowser, _, _ string) {
		zen.approval = "off"
		t.Setenv("TURBOWEB_SKIP_APPROVAL", "1")
		raw, err := send("connection_status", nil, 3000)
		if err != nil {
			t.Fatal(err)
		}
		var status map[string]any
		json.Unmarshal(raw, &status)
		if status["approvalSkipped"] != true {
			t.Errorf("an opted-out host should say so: %v", status)
		}
	})
}

func TestLaunchBrowser_HeadlessExplainsItCannotAskAnyone(t *testing.T) {
	// No TURBOWEB_SKIP_APPROVAL: a headless window has nobody to answer the
	// prompt, so refuse with the two ways out instead of hanging for 2 minutes.
	// The guard is tested on its own — driving handleLaunchBrowser through this
	// path launches a real browser process and leaves it running for the rest of
	// the suite.
	t.Setenv("TURBOWEB_SKIP_APPROVAL", "")
	msg := headlessApprovalRefusal(true)
	for _, want := range []string{"TURBOWEB_SKIP_APPROVAL=1", "headless:false", "What to do:"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal should mention %q, got %q", want, msg)
		}
	}
	// A windowed launch is the normal case and is never refused.
	if msg := headlessApprovalRefusal(false); msg != "" {
		t.Errorf("a windowed launch should not be refused, got %q", msg)
	}
	// With the opt-out a headless launch is allowed through.
	t.Setenv("TURBOWEB_SKIP_APPROVAL", "1")
	if msg := headlessApprovalRefusal(true); msg != "" {
		t.Errorf("the opt-out should let a headless launch through, got %q", msg)
	}
}

func TestSendDirect_CarriesTheSkipApprovalOptOut(t *testing.T) {
	withZenAndFirefox(t, func(zen, _ *tabBrowser, zenPrefix, _ string) {
		// Absent by default: the browser asks the user.
		if _, err := send("click", map[string]any{"tabId": zenPrefix + ":8"}, 5000); err != nil {
			t.Fatal(err)
		}
		if _, ok := zen.params("click")["_skipApproval"]; ok {
			t.Error("the opt-out must not be on unless the host asked for it")
		}

		t.Setenv("TURBOWEB_SKIP_APPROVAL", "1")
		if _, err := send("click", map[string]any{"tabId": zenPrefix + ":8"}, 5000); err != nil {
			t.Fatal(err)
		}
		if zen.params("click")["_skipApproval"] != true {
			t.Errorf("the browser was not told about the opt-out: %v", zen.params("click"))
		}
	})
}

func TestRawArgs_StripsTheSkipApprovalOverride(t *testing.T) {
	// An agent must not be able to hand itself a host-level opt-out.
	if _, ok := rawArgs(map[string]any{"_skipApproval": true})["_skipApproval"]; ok {
		t.Error("_skipApproval survived rawArgs")
	}
	if !reservedParam("_skipApproval") {
		t.Error("_skipApproval must be reserved")
	}
}

func TestListTabs_ReportsARefusalInsteadOfAnEmptyList(t *testing.T) {
	withZenAndFirefox(t, func(zen, ff *tabBrowser, _, _ string) {
		// Every browser refuses: the agent must learn it was denied. Answering
		// "no tabs open" would leave it with no reason to stop asking.
		refusal := `The user declined to let "Claude" use this browser. What to do: stop calling browser tools.`
		zen.listErr, ff.listErr = refusal, refusal
		_, err := sendListTabs(getOpenBrowsers(), 5000, map[string]any{"_client": map[string]any{"id": "a1"}})
		if err == nil || !strings.Contains(err.Error(), "declined") {
			t.Fatalf("a denial came back as %v", err)
		} else if !strings.Contains(err.Error(), "What to do:") {
			t.Errorf("the refusal should keep the extension's advice, got %v", err)
		}

		// The daemon's own refresh has nobody waiting on it, so it stays quiet.
		if _, err := sendListTabs(getOpenBrowsers(), 5000, internalParams()); err != nil {
			t.Errorf("an internal refresh should not fail: %v", err)
		}

		// One browser answering is still a result.
		zen.listErr, ff.listErr = "", ""
		zen.tabs, ff.tabs = []int{7}, []int{8}
		if _, err := sendListTabs(getOpenBrowsers(), 5000, map[string]any{"_client": map[string]any{"id": "a1"}}); err != nil {
			t.Errorf("got %v", err)
		}
	})
}

func TestIntroduce_DoesNotClaimApprovalWhenNoOneWasAsked(t *testing.T) {
	withZenAndFirefox(t, func(zen, ff *tabBrowser, _, _ string) {
		// An extension from before the approval feature cannot ask its user.
		// Saying "the user approved you" would invent a consent nobody gave.
		old := func() map[string]any { return map[string]any{"error": "Unknown action: approve_agent"} }
		zen.approve, ff.approve = old, old
		res, err := handleIntroduce(context.Background(), reqWith(map[string]any{"name": "Claude", "task": "Sorting tabs"}))
		if err != nil {
			t.Fatal(err)
		}
		text := extractText(t, res)
		if res.IsError {
			t.Fatalf("an old extension should not block introduce, got %q", text)
		}
		if strings.Contains(text, "The user approved you in") {
			t.Errorf("claimed approval nobody gave: %q", text)
		}
		if !strings.Contains(text, "too old to ask") {
			t.Errorf("should say the extension cannot ask, got %q", text)
		}
	})
}

func TestIntroduce_WaitsForTheBrowsersAnswer(t *testing.T) {
	declined := func() map[string]any {
		return map[string]any{"error": `The user declined to let "Claude" use this browser. What to do: stop.`}
	}
	intro := func() (string, bool) {
		res, err := handleIntroduce(context.Background(), reqWith(map[string]any{"name": "Claude", "task": "Sorting tabs"}))
		if err != nil {
			t.Fatal(err)
		}
		return extractText(t, res), res.IsError
	}

	withZenAndFirefox(t, func(zen, ff *tabBrowser, _, ffPrefix string) {
		// One browser saying yes is enough to start working.
		zen.approve = declined
		if text, isErr := intro(); isErr || !strings.Contains(text, "approved you in "+ffPrefix) {
			t.Errorf("want approval by Firefox, got %q", text)
		}
		// Nobody says yes: the agent is told so, in the user's words.
		ff.approve = declined
		if text, isErr := intro(); !isErr || !strings.Contains(text, "The user declined") {
			t.Errorf("want a refusal, got %q", text)
		}
		if zen.params("approve_agent")["_client"] == nil {
			t.Error("the approval request must say who is asking")
		}
		// An extension too old to ask does not block the agent.
		old := func() map[string]any { return map[string]any{"error": "Unknown action: approve_agent"} }
		zen.approve, ff.approve = old, old
		if text, isErr := intro(); isErr {
			t.Errorf("an old extension should not block introduce, got %q", text)
		}
	})

	// No browser to ask: introduce still succeeds and says nobody approved yet.
	browsersMu.Lock()
	browsers = make(map[*websocket.Conn]*BrowserConnection)
	browsersMu.Unlock()
	if text, isErr := intro(); isErr || !strings.Contains(text, "nobody has approved you") {
		t.Errorf("got %q", text)
	}
}
