package main

import (
	"fmt"
	"log"
	"os"
	"slices"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

var logger = log.New(os.Stderr, "[turboweb] ", 0)

// serverVersion is advertised to MCP clients and to /version on the
// daemon. Bumping it on a release lets running MCP instances detect a
// stale daemon and respawn it instead of routing through the old build.
const serverVersion = "1.3.0"

func main() {
	// --ws-server: run as a standalone WebSocket daemon (no MCP stdio).
	if len(os.Args) > 1 && os.Args[1] == "--ws-server" {
		logger.Println("Starting as WebSocket daemon")
		initDaemonInfo()
		if err := RunDaemon(); err != nil {
			fmt.Fprintf(os.Stderr, "Daemon error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	initSession()
	initHaiku()
	loadBrowserConfig()
	if err := initDB(); err != nil {
		logger.Printf("Warning: custom tools DB failed to init: %v", err)
	}

	go startWebSocket()

	hooks := &server.Hooks{}
	hooks.AddAfterInitialize(initializeHook())

	s := server.NewMCPServer(
		"turboweb",
		serverVersion,
		server.WithToolCapabilities(false),
		server.WithHooks(hooks),
		// Surface a top-level instruction the host can fold into the
		// system prompt. Every tool call should be preceded by an
		// `intent` argument — a one-line natural-language narration of
		// what the agent is about to do. The browser popup and on-page
		// overlay rely on this so the human user can follow along
		// without having to expand each tool-call dump.
		server.WithInstructions(strings.TrimSpace(`
TurboWeb MCP drives a real browser tab on the user's screen — a human is
watching. ⚠️ Every tool call MUST include `+"`intent`"+`: one short sentence
narrating what you're about to do (shown live as a toast on the page and in
the extension popup). Without it the overlay goes silent.

⚠️ Prefer user-visible tools — `+"`click` / `type_text` / `scroll`"+` (or
`+"`cdp_*`"+` when you need trusted events) — over `+"`execute_js`"+`, which
bypasses the overlay entirely. For reads, prefer `+"`extract_text` / `find_text`"+`
/ `+"`inspect` / `get_interactive_map`"+` — they animate; raw JS reads don't.

For deeper guidance, invoke the `+"`agent-rules`"+` prompt.
`)),
	)

	registerAllTools(s)
	registerPrompts(s)

	logger.Printf("turboweb MCP server running (stdio) — session: %s", getSessionLabel())
	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

func registerAllTools(s *server.MCPServer) {
	registerBrowserTools(s)
	registerDomTools(s)
	registerFormTools(s)
	registerInteractionTools(s)
	registerJsTools(s)
	registerDebugTools(s)
	registerFileTools(s)
	registerUpdateTools(s)
	registerCustomTools(s)
}

// textResult converts any value to a JSON text tool result.
func textResult(data any) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultText(toJSON(data)), nil
}

// intentParamDescription is shown to the agent. We keep it a single sentence
// so it fits in the model's tool-list view without dominating each tool's own
// description. The instruction is uniform across every tool.
const intentParamDescription = "Required: a one-sentence narration of what you're about to do (and ideally why), written for the human watching. Shown live in the browser popup and on the page itself, so the user can follow along without reading every tool call. Example: \"Clicking Submit because the form is filled.\""

// addTool wraps server.AddTool, injecting a uniform `intent` parameter into
// every tool's input schema. The agent fills it in to narrate each call; the
// MCP server pulls it out (see rawArgs) and forwards it as `_intent` to the
// extension and on-page overlay rather than to the tool handler itself.
//
// `intent` is marked Required so MCP hosts that validate input (Claude Code,
// Cursor, etc.) reject calls without one — forcing the agent to write a
// one-line narration before the tool executes. This is what makes the
// "calling turboweb 2 times" transcript line in Claude Code expand to
// readable per-call descriptions.
func addTool(s *server.MCPServer, tool mcp.Tool, handler server.ToolHandlerFunc) {
	if tool.InputSchema.Properties == nil {
		tool.InputSchema.Properties = map[string]any{}
	}
	if _, exists := tool.InputSchema.Properties["intent"]; !exists {
		tool.InputSchema.Properties["intent"] = map[string]any{
			"type":        "string",
			"description": intentParamDescription,
		}
	}
	if !slices.Contains(tool.InputSchema.Required, "intent") {
		tool.InputSchema.Required = append(tool.InputSchema.Required, "intent")
	}
	s.AddTool(tool, handler)
}
