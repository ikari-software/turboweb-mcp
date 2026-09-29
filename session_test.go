package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// resetSession clears global session state and env overrides for a test.
func resetSession(t *testing.T) {
	t.Helper()
	t.Setenv("MCP_CLIENT_LABEL", "")
	t.Setenv("MCP_CLIENT_TYPE", "")
	sessionMu.Lock()
	sess = sessionInfo{}
	sessionMu.Unlock()
	t.Cleanup(func() {
		sessionMu.Lock()
		sess = sessionInfo{}
		sessionMu.Unlock()
	})
}

func TestNormaliseClientType(t *testing.T) {
	cases := map[string]string{
		// Real clientInfo.name values reported by hosts.
		"claude-code":           "claude-code",
		"claude-ai":             "claude-desktop",
		"cursor-vscode":         "cursor",
		"Visual Studio Code":    "vscode",
		"Visual-Studio-Code":    "vscode",
		"jcode":                 "jcode",
		"codex-mcp-client":      "codex",
		"gemini-cli-mcp-client": "gemini-cli",
		"opencode":              "opencode",
		"Roo Code":              "roo-code",
		"cline":                 "cline",
		"Windsurf":              "windsurf",
		"Zed":                   "zed",
		"mcp-inspector":         "inspector",
		"GitHub Copilot":        "vscode",
		"Code":                  "vscode",
		// Regression: anything containing "code" used to become vscode.
		"my-custom-code-agent": "my-custom-code-agent",
		"Some Fancy Client":    "some-fancy-client",
		"":                     "",
	}
	for in, want := range cases {
		if got := normaliseClientType(in); got != want {
			t.Errorf("normaliseClientType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHostFromProcess(t *testing.T) {
	cases := map[string]string{
		"/opt/homebrew/bin/jcode":    "jcode",
		"claude":                     "claude-code",
		"/Users/x/.local/bin/claude": "claude-code",
		"/Applications/Claude.app/Contents/MacOS/Claude":                                                                        "claude-desktop",
		"/Applications/Claude.app/Contents/Helpers/disclaimer":                                                                  "claude-desktop",
		"/Applications/Cursor.app/Contents/MacOS/Cursor":                                                                        "cursor",
		"/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper (Plugin).app/Contents/MacOS/Code Helper (Plugin)": "vscode",
		"/usr/local/bin/codex":    "codex",
		"node":                    "",
		"-zsh":                    "",
		"/usr/bin/xcodebuild":     "",
		"/usr/local/bin/opencode": "opencode",
	}
	for in, want := range cases {
		if got := hostFromProcess(in); got != want {
			t.Errorf("hostFromProcess(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParsePsLine(t *testing.T) {
	ppid, comm := parsePsLine("  68518 /Applications/Claude.app/Contents/MacOS/Claude\n")
	if ppid != 68518 || comm != "/Applications/Claude.app/Contents/MacOS/Claude" {
		t.Fatalf("got %d %q", ppid, comm)
	}
	ppid, comm = parsePsLine("1 /Applications/Visual Studio Code.app/x")
	if ppid != 1 || comm != "/Applications/Visual Studio Code.app/x" {
		t.Fatalf("spaces in comm: got %d %q", ppid, comm)
	}
	if ppid, comm = parsePsLine("garbage"); ppid != 0 || comm != "" {
		t.Fatalf("garbage: got %d %q", ppid, comm)
	}
}

func TestDetectHostFromEnv(t *testing.T) {
	cases := []struct {
		env  []string
		want string
	}{
		{[]string{"JCODE_NON_INTERACTIVE=1"}, "jcode"},
		{[]string{"CLAUDECODE=1", "CLAUDE_CODE_ENTRYPOINT=cli"}, "claude-code"},
		{[]string{"CURSOR_TRACE_ID=abc"}, "cursor"},
		{[]string{"HOME=/x"}, ""},
	}
	for _, c := range cases {
		if got := detectHostFromEnv(c.env); got != c.want {
			t.Errorf("detectHostFromEnv(%v) = %q, want %q", c.env, got, c.want)
		}
	}
}

func TestSessionLabelLayers(t *testing.T) {
	resetSession(t)
	sessionMu.Lock()
	sess.project = "turboweb-mcp"
	sess.host = "vscode" // a wrong ancestry guess
	sessionMu.Unlock()

	if got := getSessionLabel(); got != "VS Code · turboweb-mcp" {
		t.Fatalf("heuristic label = %q", got)
	}

	// clientInfo from initialize beats the process guess.
	refineSessionFromInitialize("jcode", "", "0.89.0")
	if got := getSessionLabel(); got != "jcode · turboweb-mcp" {
		t.Fatalf("after initialize = %q", got)
	}
	if got := getSessionType(); got != "jcode" {
		t.Fatalf("type after initialize = %q", got)
	}

	// Unknown client names are shown verbatim, not dropped.
	refineSessionFromInitialize("Fancy Agent", "Fancy Agent Pro", "1")
	if got := getSessionDisplayName(); got != "Fancy Agent Pro" {
		t.Fatalf("unknown client display = %q", got)
	}

	// Self-introduction wins for the display name.
	applyIntroduction(introduction{Name: "Claude", Model: "claude-opus-4", Host: "Claude Code", Task: "Testing labels"})
	if got := getSessionLabel(); got != "Claude · turboweb-mcp" {
		t.Fatalf("after introduce = %q", got)
	}
	snap := snapshotSession()
	if snap["sessionType"] != "claude-code" || snap["hostName"] != "Claude Code" ||
		snap["model"] != "claude-opus-4" || snap["task"] != "Testing labels" {
		t.Fatalf("snapshot = %v", snap)
	}

	// Partial re-introduction keeps earlier fields.
	applyIntroduction(introduction{Task: "Next task"})
	snap = snapshotSession()
	if snap["agentName"] != "Claude" || snap["task"] != "Next task" {
		t.Fatalf("partial update snapshot = %v", snap)
	}
}

func TestEnvOverridesWin(t *testing.T) {
	resetSession(t)
	t.Setenv("MCP_CLIENT_LABEL", "cursor/my-workspace")
	refineSessionFromInitialize("claude-code", "", "")
	applyIntroduction(introduction{Name: "Claude", Host: "Claude Code"})
	if got := getSessionLabel(); got != "cursor/my-workspace" {
		t.Fatalf("label = %q", got)
	}
	if got := getSessionType(); got != "cursor" {
		t.Fatalf("type = %q", got)
	}
	t.Setenv("MCP_CLIENT_TYPE", "codex")
	applyIntroduction(introduction{})
	if got := getSessionType(); got != "codex" {
		t.Fatalf("MCP_CLIENT_TYPE type = %q", got)
	}
}

func TestClampText(t *testing.T) {
	if got := clampText("  a\n\tb  \x07c ", 10); got != "a b c" {
		t.Fatalf("got %q", got)
	}
	if got := clampText("ąęśćżźółń-long", 4); got != "ąęść…" {
		t.Fatalf("rune clamp got %q", got)
	}
}

func TestRegisterMessageAndProfilePick(t *testing.T) {
	resetSession(t)
	sessionMu.Lock()
	sess.project = "proj"
	sessionMu.Unlock()
	refineSessionFromInitialize("claude-code", "", "2.0.1")
	applyIntroduction(introduction{Name: "Claude", Model: "opus", Task: "t"})

	var raw map[string]any
	if err := json.Unmarshal(registerMessage(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw["type"] != "register" || raw["label"] != "Claude · proj" ||
		raw["displayName"] != "Claude" || raw["sessionType"] != "claude-code" {
		t.Fatalf("register = %v", raw)
	}

	raw["evil"] = "<script>"
	raw["model"] = "x\ny"
	prof := pickRelayProfile(raw)
	if _, ok := prof["evil"]; ok {
		t.Fatal("non-whitelisted field leaked into profile")
	}
	if prof["model"] != "x y" || prof["hostName"] != "Claude Code" || prof["clientVersion"] != "2.0.1" {
		t.Fatalf("profile = %v", prof)
	}
}

// callTool dispatches a tools/call through the real MCP server, the same
// path a host uses, and returns the result.
func callTool(t *testing.T, s *server.MCPServer, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	msg, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	resp := s.HandleMessage(context.Background(), msg)
	r, ok := resp.(mcp.JSONRPCResponse)
	if !ok {
		t.Fatalf("%s: unexpected response %#v", name, resp)
	}
	res, ok := r.Result.(*mcp.CallToolResult)
	if !ok {
		t.Fatalf("%s: unexpected result %#v", name, r.Result)
	}
	return res
}

func resultText(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestToolsRefusedUntilIntroduce(t *testing.T) {
	resetSession(t)
	t.Setenv("TURBOWEB_REQUIRE_INTRODUCE", "")
	s := server.NewMCPServer("test", "1.0.0", server.WithToolCapabilities(false))
	registerAllTools(s)

	// Browser tool before introduce: refused with a message telling the model what to do.
	res := callTool(t, s, "list_tabs", map[string]any{"intent": "Listing tabs"})
	if !res.IsError || !strings.Contains(resultText(res), "call `introduce` first") {
		t.Fatalf("expected refusal, got error=%v %q", res.IsError, resultText(res))
	}

	// Exempt diagnostics still work (never refused, whatever their outcome).
	res = callTool(t, s, "connection_status", map[string]any{"intent": "Checking"})
	if strings.Contains(resultText(res), "call `introduce` first") {
		t.Fatal("connection_status must be exempt")
	}

	// introduce itself is allowed and unlocks everything.
	res = callTool(t, s, "introduce", map[string]any{"name": "Claude", "intent": "Introducing myself"})
	if res.IsError {
		t.Fatalf("introduce failed: %q", resultText(res))
	}
	res = callTool(t, s, "list_tabs", map[string]any{"intent": "Listing tabs"})
	if strings.Contains(resultText(res), "call `introduce` first") {
		t.Fatalf("still refused after introduce: %q", resultText(res))
	}
}

func TestIntroduceGateCanBeDisabled(t *testing.T) {
	resetSession(t)
	t.Setenv("TURBOWEB_REQUIRE_INTRODUCE", "0")
	s := server.NewMCPServer("test", "1.0.0", server.WithToolCapabilities(false))
	registerAllTools(s)
	res := callTool(t, s, "list_tabs", map[string]any{"intent": "Listing tabs"})
	if strings.Contains(resultText(res), "call `introduce` first") {
		t.Fatal("gate should be off with TURBOWEB_REQUIRE_INTRODUCE=0")
	}
}

func TestIntroduceRejectsBlankName(t *testing.T) {
	resetSession(t)
	s := server.NewMCPServer("test", "1.0.0", server.WithToolCapabilities(false))
	registerAllTools(s)
	res := callTool(t, s, "introduce", map[string]any{"name": "  \n ", "intent": "x"})
	if !res.IsError || hasIntroduced() {
		t.Fatal("blank name must not count as an introduction")
	}
}
