package main

import (
	"encoding/json"

	"github.com/gorilla/websocket"
)

// relayProfileFields are the extra identity fields a relay client may send
// in its register message and the daemon forwards to the popup. Anything
// else in the message is ignored.
var relayProfileFields = []string{
	"hostName", "agentName", "model", "task", "project",
	"clientName", "clientVersion",
}

// pickRelayProfile extracts whitelisted, length-clamped string fields from
// a raw register message.
func pickRelayProfile(raw map[string]any) map[string]string {
	out := map[string]string{}
	for _, k := range relayProfileFields {
		if s, ok := raw[k].(string); ok {
			if s = clampText(s, 120); s != "" {
				out[k] = s
			}
		}
	}
	return out
}

// registerMessage builds the relay "register" control message from the
// current session snapshot.
func registerMessage() []byte {
	s := snapshotSession()
	msg := map[string]any{"type": "register"}
	keys := append([]string{"label", "displayName", "sessionType", "pid"}, relayProfileFields...)
	for _, k := range keys {
		if v, ok := s[k]; ok {
			msg[k] = v
		}
	}
	b, _ := json.Marshal(msg)
	return b
}

// reRegisterWithDaemon re-sends our register message over an existing
// relay connection. The daemon treats register as an upsert, so this is
// how identity learned after connecting (initialize handshake, `introduce`)
// reaches the popup. No-op when not in relay mode or not yet connected;
// connectRelay sends a fresh register on every (re)connect anyway.
func reRegisterWithDaemon() {
	relayMu.Lock()
	defer relayMu.Unlock()
	if relayConn == nil {
		return
	}
	_ = relayConn.WriteMessage(websocket.TextMessage, registerMessage())
}
