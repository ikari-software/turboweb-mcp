package main

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func connectBiDiReq(args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "connect_bidi", Arguments: args}}
}

func TestHandleConnectBiDiRejectsBadPort(t *testing.T) {
	for _, p := range []any{nil, 0, -1, 70000} {
		res, err := handleConnectBiDi(context.Background(), connectBiDiReq(map[string]any{"port": p}))
		if err != nil {
			t.Fatalf("port %v: unexpected error: %v", p, err)
		}
		if !res.IsError {
			t.Errorf("port %v: expected an error result for an invalid port", p)
		}
		if msg := extractText(t, res); !strings.Contains(msg, "port") {
			t.Errorf("port %v: error should mention the port: %q", p, msg)
		}
	}
}

func TestHandleConnectBiDiAlreadyConnected(t *testing.T) {
	// Pretend BiDi is already up; the handler must short-circuit (no dial) and
	// report already-connected rather than stacking a second client.
	setBiDi(&BiDiClient{})
	defer setBiDi(nil)

	res, err := handleConnectBiDi(context.Background(), connectBiDiReq(map[string]any{"port": 9222}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected a success result when already connected")
	}
	msg := extractText(t, res)
	if !strings.Contains(msg, "already") {
		t.Errorf("expected already-connected note, got: %q", msg)
	}
}
