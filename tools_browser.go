package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerBrowserTools(s *server.MCPServer) {
	// --- launch_browser ---
	addTool(s,
		mcp.NewTool("launch_browser",
			mcp.WithDescription("Launch a new Chrome instance with stealth flags (--silent-debugger-extension-api to hide the debugging infobar). Auto-loads the extension. Uses a dedicated profile so it won't affect your main browser."),
			mcp.WithBoolean("headless", mcp.Description("Launch in headless mode (default false)")),
		),
		handleLaunchBrowser,
	)

	// --- connection_status ---
	addTool(s,
		mcp.NewTool("connection_status",
			mcp.WithDescription("Check if the browser extension and/or BiDi are connected to the MCP server"),
		),
		handleConnectionStatus,
	)

	// --- list_tabs ---
	addTool(s,
		mcp.NewTool("list_tabs",
			mcp.WithDescription("List all open Chrome tabs with their IDs, titles, and URLs"),
		),
		handleListTabs,
	)

	// --- navigate ---
	addTool(s,
		mcp.NewTool("navigate",
			mcp.WithDescription("Navigate a tab to a URL. Omit tabId to use the active tab."),
			mcp.WithString("url", mcp.Required(), mcp.Description("URL to navigate to")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		handleNavigate,
	)

	// --- screenshot ---
	addTool(s,
		mcp.NewTool("screenshot",
			mcp.WithDescription("Take a screenshot of a tab. Returns a JPEG image scaled to maxWidth (default 1280px, NOT retina 3000px). Fast and compact."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			mcp.WithNumber("maxWidth", mcp.Description("Max width in pixels (default 1280)")),
			mcp.WithNumber("quality", mcp.Description("JPEG quality 1-100 (default 70)")),
		),
		handleScreenshot,
	)

	// --- turbo_snapshot ---
	addTool(s,
		mcp.NewTool("turbo_snapshot",
			mcp.WithDescription("TURBO: Screenshot + interactive element map in ONE call. Returns a scaled JPEG image AND a JSON spatial map of all interactive elements. The fastest way to understand a page."),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
			mcp.WithNumber("maxWidth", mcp.Description("Screenshot max width (default 1280)")),
			mcp.WithNumber("quality", mcp.Description("JPEG quality 1-100 (default 70)")),
		),
		handleTurboSnapshot,
	)
}

func handleLaunchBrowser(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	headless := getBool(args, "headless", false)
	pid, err := launchBrowser(headless)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return textResult(map[string]any{
		"launched": true,
		"pid":      pid,
		"headless": headless,
		"stealth":  true,
		"message":  "Chrome launched with --silent-debugger-extension-api (no debugging infobar)",
	})
}

func handleConnectionStatus(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Browser extensions connect to the daemon, not to this relay process, so
	// this process's own browsers map is empty in relay mode. Route through
	// send() — sendDirect/sendViaRelay land on the daemon, which answers from
	// its real browser WebSocket state. Cap the timeout: connection_status is
	// purely diagnostic, so it must not inherit the 30s default and stall an
	// agent waiting on a wedged daemon.
	raw, err := send("connection_status", nil, 3000)
	var status map[string]any
	switch {
	case err != nil:
		// connection_status is a diagnostic tool — it must always return a
		// parseable verdict, never an MCP error. A failed send() means the
		// relay link to the daemon is down or too slow; report not-connected
		// with the reason rather than making the agent handle a tool error.
		status = map[string]any{
			"connected": false, "extension": false, "bidi": false,
			"error": err.Error(),
		}
	case json.Unmarshal(raw, &status) != nil:
		status = map[string]any{
			"connected": false, "extension": false, "bidi": false,
			"error": "malformed connection_status response from daemon",
		}
	}
	// BiDi can be owned by whichever process launched the browser — the
	// daemon, or this relay process if an agent called launch_browser here.
	// Merge the local view so a relay-owned BiDi session still counts, even
	// when the daemon link is down.
	if getBiDi() != nil {
		status["bidi"] = true
		status["connected"] = true
	}
	return textResult(status)
}

func handleListTabs(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	raw, err := send("list_tabs", nil)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if getBiDi() != nil {
		_ = ingestExtensionTabsForBiDi(raw)
	}
	return mcp.NewToolResultText(string(raw)), nil
}

func handleNavigate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	raw, err := send("navigate", rawArgs(args))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(raw)), nil
}

func handleScreenshot(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	quality := intOr(args["quality"], 70)

	// Try BiDi first (works in both Chrome and Firefox, no focus needed)
	if getBiDi() != nil {
		ctxID, err := resolveContext(args["tabId"])
		if err == nil {
			data, err := bidiScreenshot(ctx, ctxID, quality)
			if err == nil {
				encoded := base64Encode(data)
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						mcp.NewImageContent(encoded, "image/jpeg"),
						mcp.NewTextContent("screenshot via BiDi"),
					},
				}, nil
			}
			logger.Printf("BiDi screenshot failed, falling back to extension: %v", err)
		}
	}

	// Fallback: extension path
	raw, err := send("screenshot", rawArgs(args))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	var result struct {
		Base64   string `json:"base64"`
		MimeType string `json:"mimeType"`
		Width    int    `json:"width"`
		Height   int    `json:"height"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to parse screenshot: %v", err)), nil
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewImageContent(result.Base64, result.MimeType),
			mcp.NewTextContent(fmt.Sprintf("%dx%d jpeg", result.Width, result.Height)),
		},
	}, nil
}

func handleTurboSnapshot(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	raw, err := send("turbo_snapshot", rawArgs(args))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	var result struct {
		Screenshot struct {
			Base64   string `json:"base64"`
			MimeType string `json:"mimeType"`
			Width    int    `json:"width"`
			Height   int    `json:"height"`
		} `json:"screenshot"`
		InteractiveMap json.RawMessage `json:"interactiveMap"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to parse turbo_snapshot: %v", err)), nil
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewImageContent(result.Screenshot.Base64, result.Screenshot.MimeType),
			mcp.NewTextContent(string(result.InteractiveMap)),
		},
	}, nil
}
