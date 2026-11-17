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

// approvalSkipped reports whether the host has opted out of the browser's
// agent-approval prompt. Off by default: a person at the browser decides
// whether an agent may drive it.
//
// Set TURBOWEB_SKIP_APPROVAL=1 for an unattended setup where nobody can answer
// a window — headless Chrome, CI, a kiosk. The browser still records a refusal
// the user gave explicitly, so the opt-out means "do not ask", not "ignore the
// answer". This is not a security boundary: anything that can set this process's
// environment can already run commands as its user.
func approvalSkipped() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TURBOWEB_SKIP_APPROVAL"))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
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
					"cursor then show who you are. The first call opens a window in the browser where "+
					"the human decides whether to let you in. It waits about 20 seconds for an answer and "+
					"then returns either way; if they haven't answered yet, carry on and check later. "+
					"Their answer lasts until your session ends or they restart the browser, which can be "+
					"days, so `task` must describe the whole job, not your next step. TurboWeb itself adds "+
					"the app you run in, your working folder and git branch, so don't repeat those. Call "+
					"again only when the job itself changes."),
			mcp.WithString("name", mcp.Required(), mcp.Description(
				"Your name and nothing else, e.g. \"Claude\", \"Codex\", \"Gemini\", or a persona name the "+
					"user gave you. Don't add the model or the app: they have their own fields. Max 40 chars.")),
			mcp.WithString("model", mcp.Description(
				"Your exact model ID as your system prompt or host states it, e.g. \"claude-opus-4-5\", "+
					"\"gpt-5-codex\". Leave it empty if nothing tells you: a guess from your name or "+
					"model family misleads the person deciding whether to trust you.")),
			mcp.WithString("host", mcp.Description(
				"Only if TurboWeb cannot tell: the app running you. It is normally read from the MCP "+
					"handshake, and then this field is ignored.")),
			mcp.WithString("task", mcp.Description(
				"The overall job the user gave you in this session, in a way they will recognise later, "+
					"e.g. \"Fixing turboweb-mcp screenshot bugs and checking them in the browser\". Not "+
					"your next step: that goes in each call's `intent`. Max 120 chars.")),
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
	// The human decides whether this agent may use their browser. This
	// blocks on a window in the browser, so it can take a while.
	note, err := requestApproval()
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText("Thanks! The browser now shows you as " + describeSession() + ". " + note), nil
}
