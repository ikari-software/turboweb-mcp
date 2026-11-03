package main

import (
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerJsTools(s *server.MCPServer) {
	// --- execute_js ---
	addTool(s,
		mcp.NewTool("execute_js",
			mcp.WithDescription("Execute JavaScript in the page's MAIN world (not isolated). Full access to page globals, frameworks (React, Vue, etc.), and APIs. Returns the result. Code is auto-wrapped in a function, so bare `return` statements work."),
			mcp.WithString("code", mcp.Required(), mcp.Description("JavaScript code to execute. `return` works without needing an IIFE wrapper.")),
			tabIDParam(),
		),
		passThrough("execute_js"),
	)

	// --- adapt_script ---
	addTool(s,
		mcp.NewTool("adapt_script",
			mcp.WithDescription("Inject and run a custom script in the page's MAIN world. Use persist=true to keep it running (e.g., MutationObservers, event listeners). Use persist=false to run once and get the result. This is the TURBO power tool — write task-specific scripts that automate work on the page."),
			mcp.WithString("code", mcp.Required(), mcp.Description("JavaScript code to inject. For persist=false, return a value. For persist=true, set up observers/listeners.")),
			mcp.WithBoolean("persist", mcp.Description("Keep script in page (default false)")),
			tabIDParam(),
		),
		passThrough("adapt_script"),
	)
}
