package main

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerUpdateTools registers the self-update tools: check_for_updates
// (detect) and self_update (install). See update.go for the mechanism.
func registerUpdateTools(s *server.MCPServer) {
	// --- check_for_updates ---
	addTool(s,
		mcp.NewTool("check_for_updates",
			mcp.WithDescription("Check whether a newer turboweb-mcp release is available on GitHub. "+
				"Returns the current and latest version, whether an update is available, and the "+
				"release page URL. Read-only — does not install anything."),
		),
		handleCheckForUpdates,
	)

	// --- self_update ---
	addTool(s,
		mcp.NewTool("self_update",
			mcp.WithDescription("Download and install the latest turboweb-mcp release for this platform. "+
				"Verifies the binary against the release's signed SHA256SUMS before replacing the running "+
				"executable. The browser daemon respawns from the new binary automatically on the next "+
				"call; MCP server instances pick it up on their next launch. No-op if already up to date."),
		),
		handleSelfUpdate,
	)
}

func handleCheckForUpdates(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return textResult(getUpdateStatus(false))
}

func handleSelfUpdate(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	res, err := performSelfUpdate()
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return textResult(res)
}
