package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerPrompts wires the MCP prompts the daemon offers. They surface in
// hosts as slash commands (Claude Code: /mcp__turboweb-mcp-by-ikari__<name>).
//
// Two flavours:
//
//   - agent-rules: behavioural guidance the user re-injects mid-session when
//     the agent has drifted toward execute_js or other invisible paths. The
//     server-level WithInstructions already biases the agent at session start;
//     this is the louder reminder for "the agent stopped using the cursor".
//
//   - upgrade: version-aware install/upgrade instructions. The daemon knows
//     its own serverVersion (1.1.0 today) so the prompt can tell the agent
//     "you're on X, latest is at the releases page, here's what to swap".
func registerPrompts(s *server.MCPServer) {
	s.AddPrompt(
		mcp.NewPrompt("agent-rules",
			mcp.WithPromptDescription(
				"Re-inject TurboWeb MCP's full behavioural guidance — tool selection, "+
					"selector-vs-coords, BiDi-vs-synthetic, file uploads, multi-tab orchestration. "+
					"Use when the agent has drifted toward execute_js or otherwise stopped narrating.",
			),
		),
		handleAgentRulesPrompt,
	)

	s.AddPrompt(
		mcp.NewPrompt("upgrade",
			mcp.WithPromptDescription(
				"Walk through upgrading the currently-installed TurboWeb MCP "+
					"to the latest GitHub release. Detects existing install, swaps the binary, "+
					"reloads the extension, restarts the daemon.",
			),
		),
		handleUpgradePrompt,
	)
}

func handleAgentRulesPrompt(_ context.Context, _ mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	body := strings.TrimSpace(`
TurboWeb MCP — full agent behavioural guidance.

# Visibility comes first

The whole point of this MCP is that the human watching can see what you're
doing. Every interaction tool fires the on-page robot cursor + the intent
toast. Bypassing them with execute_js leaves the user blind.

- DOM interaction → prefer click / type_text / scroll. For elements that
  guard on event.isTrusted (MUI portals, dropdowns, framework popovers) or
  pages that block synthetic activation (CSP, javascript: URLs), step up to
  cdp_click / cdp_type / cdp_scroll. They dispatch real OS-level input via
  CDP and produce trusted events.
- Data reads → prefer extract_text / find_text / inspect /
  get_interactive_map. They trigger the loupe-sweep or scan-flash
  visualisation; raw JS reads are invisible.
- execute_js is only correct for: reading window globals, computing state,
  batch DOM transforms with no per-step user-visible meaning, or inside a
  custom_tool you've already registered with the user.

# Selector vs coords

- cdp_click / cdp_type / cdp_scroll all accept either a selector OR x,y.
  Selector wins when both are supplied. The page resolves the selector to
  its centre coordinates with getBoundingClientRect, so you don't need to
  pre-compute viewport math.
- Use coords only when targeting a canvas, an OS-level chrome region, or
  any element where a selector legitimately doesn't apply.
- Prefer selectors — they survive scroll/reflow. For iframes they're also
  the only portable target: a frame argument is a chain of CSS selectors
  (e.g. "#outer > #inner"), and coordinates don't translate across a
  cross-origin frame boundary.

# Frames (same- and cross-origin)

- The content script runs in EVERY frame, so DOM reads and synthetic
  interaction (extract_text, find_text, query_elements, click, type_text,
  fill_input, scroll) reach cross-origin embeds — pass the frame argument
  with the framePath. Discover frames and their framePaths with list_frames.
- Address frames by selector, never by coordinates. cdp_* tools also accept
  a frame argument for trusted input into a frame.

# Replacing a field's value

- To REPLACE (not append) text in an input/textarea, use cdp_type with
  clear=true — it selects-all + deletes before typing, with trusted input,
  so React/Vue/MUI controlled fields update correctly. (fill_input also
  replaces, via the framework value-setter, for plain form fields.)
- Need the selection or deletion as discrete steps? cdp_click clickCount=3
  triple-clicks to select all of a field's text; cdp_key with
  modifiers=["Meta"] (macOS) / ["Control"] (else) sends select-all; cdp_key
  key="Backspace" deletes. These are the trusted-input building blocks.
- Do NOT reach for execute_js to set .value — it skips the overlay and often
  doesn't notify the framework.

# File uploads

- set_input_files: when you can target the <input type=file> (or its
  visible label/wrapper) with a selector. Works on hidden / styled inputs.
- intercept_file_chooser: when the input is dispatched-via-button,
  cross-frame, or lazy-mounted and not selectable. Arm with files=[paths],
  click the visible upload control, the next native chooser is
  auto-fulfilled (one-shot — re-arm for the next upload).
- Paths are resolved on the MCP server host: ~ expands, relative paths are
  rejected, symlinks are realpath'd.

# Multi-tab orchestration

- Every tool accepts an optional tabId. list_tabs to discover targets;
  pass tabId to drive a specific window.
- The daemon supports multiple agents driving the same browser, and a
  single agent driving multiple browsers. When you take a tabId-aware
  action, the on-page overlay tints with this agent's hue so the user can
  tell which agent did what.

# When the user is watching

- Pace yourself for visibility. Long sequences of rapid synthetic clicks
  blow past the cursor animation — prefer cdp_click which gates on cursor
  arrival.
- A meaningful intent string is mandatory. "Clicking" is uselessly vague;
  "Clicking Submit to send the contact form" is what the toast was built
  for.
`)
	return &mcp.GetPromptResult{
		Description: "TurboWeb MCP behavioural guidance",
		Messages: []mcp.PromptMessage{
			{
				Role:    mcp.RoleUser,
				Content: mcp.NewTextContent(body),
			},
		},
	}, nil
}

func handleUpgradePrompt(_ context.Context, _ mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	body := fmt.Sprintf(strings.TrimSpace(`
Please upgrade my TurboWeb MCP install. The currently-running daemon is
version %s.

1. Check https://github.com/ikari-software/turboweb-mcp/releases/latest for
   the newest tag. If it matches %s, there is nothing to upgrade — stop and
   confirm with the user.
2. Download the latest server binary for my OS (darwin-arm64,
   linux-amd64, windows-amd64.exe, or windows-arm64.exe) and the matching
   extension zip (chrome or firefox).
3. Verify SHA256SUMS via cosign verify-blob against the repo's release-
   workflow identity before running anything — instructions in SECURITY.md.
   If verification fails, STOP and tell me; do not run the binary.
4. Locate the existing turboweb-mcp-by-ikari binary on $PATH (which
   $(which turboweb-mcp-by-ikari) should resolve). Swap it for the new one,
   preserving the path. The old binary's stale daemon will detect the
   newer mtime on its next /version probe and respawn itself.
5. Unpack the new extension zip over the existing extension directory.
   In chrome://extensions / about:debugging, reload the extension so the
   new bundle loads.
6. Run a connection check — list my browser tabs — to confirm the new
   daemon + extension are talking.
7. Tell me the version delta (%s → latest) and the highlight of what
   changed (read the release notes from the GitHub release).

Do NOT rewrite my MCP client config (~/.claude.json, ~/.cursor/mcp.json,
etc.). The binary path and command name are stable across versions.
`), serverVersion, serverVersion, serverVersion)

	return &mcp.GetPromptResult{
		Description: fmt.Sprintf("Upgrade TurboWeb MCP (running %s)", serverVersion),
		Messages: []mcp.PromptMessage{
			{
				Role:    mcp.RoleUser,
				Content: mcp.NewTextContent(body),
			},
		},
	}, nil
}
