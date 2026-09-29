package main

import (
	"context"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// introduceExempt are tools an agent may call before introducing itself:
// `introduce` itself, plus pure diagnostics/maintenance that never touch
// the page (so a broken setup can still be debugged).
var introduceExempt = map[string]bool{
	"introduce":         true,
	"connection_status": true,
	"check_for_updates": true,
	"self_update":       true,
}

// introduceRequired reports whether tools refuse to run until the agent
// has called `introduce`. On by default. TURBOWEB_REQUIRE_INTRODUCE=0
// (or false/off/no) turns it off for hosts or scripts that can't comply.
func introduceRequired() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TURBOWEB_REQUIRE_INTRODUCE"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// hasIntroduced reports whether the agent has called `introduce`.
func hasIntroduced() bool {
	sessionMu.RLock()
	defer sessionMu.RUnlock()
	return sess.agentName != ""
}

// introduceFirstMessage is returned instead of running a tool when the
// agent hasn't introduced itself. Written for the model: it says exactly
// what to call and that a retry will work.
const introduceFirstMessage = "Refused: call `introduce` first. The human watching the browser " +
	"needs to know who is driving it. Call `introduce` once with at least `name` " +
	"(e.g. {\"name\": \"Claude\", \"model\": \"claude-opus-4\", \"task\": \"Filling in the form\", " +
	"\"intent\": \"Introducing myself\"}), then retry this call unchanged."

// requireIntroduction wraps a tool handler so it refuses to run until
// `introduce` has been called, the same way a missing `intent` is
// rejected. Exempt tools pass straight through.
func requireIntroduction(name string, h server.ToolHandlerFunc) server.ToolHandlerFunc {
	if introduceExempt[name] {
		return h
	}
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if introduceRequired() && !hasIntroduced() {
			logger.Printf("Refused %s: agent has not called introduce", name)
			return mcp.NewToolResultError(introduceFirstMessage), nil
		}
		return h(ctx, req)
	}
}

// registerSessionTools registers tools that describe the agent itself
// rather than the browser.
func registerSessionTools(s *server.MCPServer) {
	addTool(s,
		mcp.NewTool("introduce",
			mcp.WithDescription(
				"REQUIRED FIRST CALL. Introduce yourself to the human watching the browser. "+
					"All other tools (except connection_status / check_for_updates / self_update) "+
					"refuse to run until you call this once. The extension popup and on-page "+
					"cursor then show who you are instead of a guessed editor name. Call again "+
					"only if your task changes. Cheap: no browser round-trip."),
			mcp.WithString("name", mcp.Required(), mcp.Description(
				"Short name to show on the cursor and toasts, e.g. \"Claude\", \"Codex\", "+
					"\"GPT-5\", or a persona name if you have one. Max 40 chars.")),
			mcp.WithString("model", mcp.Description(
				"Your model identifier if you know it, e.g. \"claude-opus-4\", \"gpt-5\".")),
			mcp.WithString("host", mcp.Description(
				"The app/harness running you, e.g. \"Claude Code\", \"Cursor\", \"jcode\", "+
					"\"Codex CLI\", \"Claude Desktop\". Leave empty if unsure: it's auto-detected.")),
			mcp.WithString("task", mcp.Description(
				"One short line on what you're doing for the user, e.g. \"Filling in the "+
					"expense report\". Max 120 chars.")),
		),
		handleIntroduce,
	)
}

func handleIntroduce(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := req.GetString("name", "")
	if clampText(name, 40) == "" {
		return mcp.NewToolResultError("name is required"), nil
	}
	applyIntroduction(introduction{
		Name:  name,
		Model: req.GetString("model", ""),
		Host:  req.GetString("host", ""),
		Task:  req.GetString("task", ""),
	})
	logger.Printf("Agent introduced itself: %s", describeSession())
	publishSession()
	return mcp.NewToolResultText("Thanks! The browser now shows you as " + describeSession() + "."), nil
}
