package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Before a browser obeys an agent, its user approves that agent in a window
// the extension opens: who it says it is, what it says it is there for, and
// where the connection comes from. The extension is the one that enforces
// this, per agent session, because it is the part that holds the browser's
// privileges. The daemon's job is to tell it truthfully which session each
// command belongs to (the _client stamp) and where that session connected
// from.

// approveWaitMs is how long `introduce` waits for the human's answer. The
// extension waits this long itself; the request gets slack on top so the
// extension's own, more useful error arrives rather than a bare timeout.
const (
	approveWaitMs       = 120000
	approveReplySlackMs = 10000
)

// sessionKey identifies this MCP process to the daemon for as long as it
// runs. It is a secret between the two: the browser only sees its hash.
var sessionKey = randomHex(16)

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // no entropy: nothing sensible to do
	}
	return hex.EncodeToString(b)
}

func clientIDFromKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// stamp is the identity attached to every command this client sends. Caller
// holds relayClientsMu. Everything but id, remote and origin is what the
// agent says about itself.
func (c *relayClientInfo) stamp() map[string]any {
	out := map[string]any{
		"id":     c.id,
		"name":   c.displayName,
		"remote": c.remote,
		"origin": c.origin,
		"pid":    c.pid,
	}
	for _, k := range []string{"agentName", "model", "task", "hostName", "project"} {
		if v := c.profile[k]; v != "" {
			out[k] = v
		}
	}
	return out
}

// localClientStamp is the stamp for in-process mode, where the agent's own
// MCP server talks to the browser without a daemon in between.
func localClientStamp() map[string]any {
	s := snapshotSession()
	out := map[string]any{
		"id":     clientIDFromKey(sessionKey),
		"name":   s["displayName"],
		"remote": "in-process",
		"origin": "",
		"pid":    os.Getpid(),
	}
	for _, k := range []string{"agentName", "model", "task", "hostName", "project"} {
		if v, _ := s[k].(string); v != "" {
			out[k] = v
		}
	}
	return out
}

// internalParams marks a command the daemon sends for its own bookkeeping
// (refreshing which browser owns which tab, naming the browser it is talking
// to). No agent sees the result, so the browser does not ask anyone to approve
// it. Callers that need extra fields pass them as alternating key/value pairs.
//
// Every use of this must be a call site the daemon itself chose — never one an
// agent can steer. rawArgs strips `_internal` from tool arguments and the relay
// handler strips it from raw relay params for exactly that reason.
func internalParams(kv ...any) map[string]any {
	p := map[string]any{"_internal": true}
	for i := 0; i+1 < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			continue
		}
		p[k] = kv[i+1]
	}
	return p
}

// sendApproveAgent asks every connected browser to approve the calling
// agent and returns as soon as one does. The others keep their prompt open;
// each browser enforces its own answer when a command reaches it.
//
// A browser running an extension from before this feature answers "Unknown
// action". That is not an approval: it cannot ask, and it cannot enforce
// either, so there is nothing to wait for — but reporting it as consent would
// tell the agent a human said yes when nobody was ever asked. It is returned
// as `unsupported` instead, and the caller words its message accordingly.
func sendApproveAgent(open []*BrowserConnection, params map[string]any, timeout int) (json.RawMessage, error) {
	type answer struct {
		browser     string
		err         error
		unsupported bool
	}
	answers := make(chan answer, len(open))
	for _, bc := range open {
		// Each browser gets its own copy: sendTo's callers must not share a map.
		p := make(map[string]any, len(params))
		for k, v := range params {
			p[k] = v
		}
		go func(bc *BrowserConnection) {
			_, err := sendTo(bc, "approve_agent", p, timeout)
			unsupported := err != nil && strings.Contains(err.Error(), "Unknown action")
			answers <- answer{bc.tabPrefix(), err, unsupported}
		}(bc)
	}
	var failures, unsupported []string
	for range open {
		a := <-answers
		switch {
		case a.err == nil:
			return json.Marshal(map[string]any{"approved": true, "browser": a.browser})
		case a.unsupported:
			unsupported = append(unsupported, a.browser)
		default:
			failures = append(failures, a.browser+": "+a.err.Error())
		}
	}
	if len(unsupported) > 0 {
		b, _ := json.Marshal(map[string]any{"approved": false, "unsupported": unsupported})
		return b, nil
	}
	return nil, fmt.Errorf("%s", strings.Join(failures, "; "))
}

// requestApproval asks the user's browser to let this agent in and waits for
// the answer. With no browser to ask it returns a note instead of an error:
// the browser will ask when the first command reaches it.
func requestApproval() (note string, err error) {
	raw, err := send("approve_agent", map[string]any{"timeoutMs": approveWaitMs}, approveWaitMs+approveReplySlackMs)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "No browser extension connected") || strings.Contains(msg, "Not connected to daemon") {
			return "No browser is connected yet, so nobody has approved you: the browser will ask its user when your first command reaches it.", nil
		}
		return "", err
	}
	var reply struct {
		Browser     string   `json:"browser"`
		Unsupported []string `json:"unsupported"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return "", fmt.Errorf("could not read the browser's answer: %w", err)
	}
	if len(reply.Unsupported) > 0 {
		return "The extension in " + strings.Join(reply.Unsupported, ", ") +
			" is too old to ask its user, so nobody has approved you — it cannot enforce approval either. " +
			"Your commands will run, but the human gets no say until the extension is updated.", nil
	}
	if reply.Browser == "" {
		return "", fmt.Errorf("the browser answered without saying which one it was")
	}
	return "The user approved you in " + reply.Browser + ".", nil
}
