package main

import (
	"encoding/json"
	"strings"
)

// toJSON marshals any value to a JSON string.
func toJSON(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case json.RawMessage:
		return string(val)
	case []byte:
		return string(val)
	default:
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return "{}"
		}
		return string(b)
	}
}

// contains is a simple string contains check.
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// toInt converts various numeric types to int (handles JSON float64).
func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

// toFloat converts various numeric types to float64.
func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	default:
		return 0
	}
}

// getString extracts a string from a map with a default.
func getString(args map[string]any, key, defaultVal string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return defaultVal
}

// getInt extracts an int from a map with a default.
func getInt(args map[string]any, key string, defaultVal int) int {
	if v, ok := args[key]; ok && v != nil {
		return toInt(v)
	}
	return defaultVal
}

// getBool extracts a bool from a map with a default.
func getBool(args map[string]any, key string, defaultVal bool) bool {
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return defaultVal
}

// maxIntentLen caps the agent-supplied narration to a single line of
// reasonable length. The model is asked for one short sentence; clamping
// server-side prevents a misbehaving (or prompt-injected) agent from
// drowning the popup/toast in a 5KB intent.
const maxIntentLen = 200

// reservedParam reports whether an argument key belongs to the server, not to
// the agent. The daemon and extension exchange a handful of underscore-prefixed
// fields that steer behaviour rather than describe a command:
//
//	_internal  skip the browser's agent-approval gate (daemon bookkeeping only)
//	_client*   the identity stamp the browser shows in the approval prompt
//	_intent    the agent's narration, rewritten by rawArgs below
//
// An agent that can set any of these can either bypass the approval gate
// entirely (_internal) or write its own consent prompt (_client). mcp-go does
// not validate arguments against the tool schema, so every key the caller
// sends arrives here verbatim; this predicate is the single chokepoint that
// keeps them out. No tool takes an underscore-prefixed argument.
func reservedParam(k string) bool {
	return strings.HasPrefix(k, "_")
}

// rawArgs returns the arguments map suitable for forwarding to the extension
// via the WS bridge. It drops every server-owned key (see reservedParam), so
// an agent cannot smuggle in a `_internal` bypass or a forged `_client` stamp.
// As a side-effect it pulls out the agent-supplied `intent` field, clamps it to
// maxIntentLen, and rewrites it as `_intent` so the extension popup and on-page
// overlay can surface it to the human user. The original `intent` key is
// removed so handlers never see it.
//
// The result is always a fresh map: callers must not hand back the caller's
// own arguments, because sendDirect mutates what it is given when it stamps
// identity onto it.
func rawArgs(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		if k == "intent" || reservedParam(k) {
			continue
		}
		out[k] = v
	}
	if args == nil {
		return out
	}
	intent, ok := args["intent"].(string)
	if !ok || intent == "" {
		return out
	}
	if len(intent) > maxIntentLen {
		intent = intent[:maxIntentLen] + "…"
	}
	out["_intent"] = intent
	return out
}

// extractIntent reads the agent's `intent` argument without mutating the map.
// Useful from handlers that don't pipe args through rawArgs (e.g. BiDi-only
// handlers that need to log via a side channel).
func extractIntent(args map[string]any) string {
	if args == nil {
		return ""
	}
	if s, ok := args["intent"].(string); ok {
		return s
	}
	return ""
}

// buildParams creates a params map for the WebSocket send, filtering nil values.
func buildParams(pairs ...any) map[string]any {
	m := make(map[string]any)
	for i := 0; i+1 < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			continue
		}
		val := pairs[i+1]
		if val != nil {
			m[key] = val
		}
	}
	return m
}
