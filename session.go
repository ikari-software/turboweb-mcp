package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/mark3labs/mcp-go/mcp"
)

// Session metadata for the current MCP-server process. Surfaced to the
// extension popup and on-page overlay so a human can see which agent
// (Claude Code in some folder, Cursor, jcode, ...) is driving the browser.
//
// Identity is layered, most authoritative last-wins:
//
//  1. process ancestry / environment heuristics (initSession, before handshake)
//  2. MCP initialize clientInfo (refineSessionFromInitialize)
//  3. the agent introducing itself via the `introduce` tool (applyIntroduction)
//  4. MCP_CLIENT_LABEL / MCP_CLIENT_TYPE env overrides (always win)
var (
	sessionMu        sync.RWMutex
	sess             sessionInfo
	sessionStartedAt = time.Now()
)

// sessionInfo is everything we know about who is on the other end of stdio.
type sessionInfo struct {
	host          string // canonical host id: "claude-code", "cursor", "jcode", ...
	clientName    string // raw clientInfo.name from initialize
	clientTitle   string // clientInfo.title, when the host sends one
	clientVersion string // clientInfo.version
	agentName     string // self-introduced name ("Claude", "GPT-5 via jcode", ...)
	model         string // self-introduced model id
	task          string // self-introduced one-line task
	project       string // basename of the working directory
	labelOverride string // MCP_CLIENT_LABEL
}

// knownHosts maps canonical host ids to human display names.
var knownHosts = map[string]string{
	"claude-code":    "Claude Code",
	"claude-desktop": "Claude Desktop",
	"cursor":         "Cursor",
	"vscode":         "VS Code",
	"windsurf":       "Windsurf",
	"zed":            "Zed",
	"jcode":          "jcode",
	"codex":          "Codex",
	"gemini-cli":     "Gemini CLI",
	"opencode":       "opencode",
	"cline":          "Cline",
	"roo-code":       "Roo Code",
	"continue":       "Continue",
	"goose":          "Goose",
	"kiro":           "Kiro",
	"amp":            "Amp",
	"warp":           "Warp",
	"lm-studio":      "LM Studio",
	"inspector":      "MCP Inspector",
}

// hostDisplayName returns the human name for a host id, or the id itself.
func hostDisplayName(host string) string {
	if d, ok := knownHosts[host]; ok {
		return d
	}
	return host
}

// initSession derives a best-effort identity without waiting for the
// initialize handshake. Refined later via AddAfterInitialize and `introduce`.
func initSession() {
	sessionMu.Lock()
	defer sessionMu.Unlock()

	cwd, _ := os.Getwd()
	sess.project = projectName(cwd)
	sess.host = detectHostFromAncestry(os.Getppid())
	if sess.host == "" {
		sess.host = detectHostFromEnv(os.Environ())
	}
	applyEnvOverridesLocked()
}

// applyEnvOverridesLocked applies MCP_CLIENT_LABEL / MCP_CLIENT_TYPE. Caller
// holds sessionMu. MCP_CLIENT_LABEL keeps its historical "<type>/<rest>"
// convention: the part before the first slash doubles as the host type
// unless MCP_CLIENT_TYPE says otherwise.
func applyEnvOverridesLocked() {
	if v := strings.TrimSpace(os.Getenv("MCP_CLIENT_LABEL")); v != "" {
		sess.labelOverride = v
		if before, _, found := strings.Cut(v, "/"); found && before != "" {
			sess.host = before
		}
	}
	if v := strings.TrimSpace(os.Getenv("MCP_CLIENT_TYPE")); v != "" {
		sess.host = v
	}
}

// projectName returns a short project name for a working directory.
func projectName(cwd string) string {
	base := filepath.Base(cwd)
	if base == "." || base == "/" || base == "" || base == string(filepath.Separator) {
		return ""
	}
	return base
}

// refineSessionFromInitialize updates session metadata once the MCP client
// has sent clientInfo via initialize. The client's own name is more
// authoritative than process-tree guessing, so it wins, and an unknown
// client name is used verbatim rather than discarded.
func refineSessionFromInitialize(name, title, version string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	sessionMu.Lock()
	defer sessionMu.Unlock()
	sess.clientName = clampText(name, 60)
	sess.clientTitle = clampText(title, 60)
	sess.clientVersion = clampText(version, 30)
	if h := normaliseClientType(name); h != "" {
		sess.host = h
	}
	applyEnvOverridesLocked()
}

// introduction is what an agent tells us about itself via `introduce`.
type introduction struct {
	Name  string
	Model string
	Host  string
	Task  string
}

// applyIntroduction records the agent's self-description. Empty fields
// leave the existing value alone so an agent can update just its task.
func applyIntroduction(in introduction) {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	if v := clampText(in.Name, 40); v != "" {
		sess.agentName = v
	}
	if v := clampText(in.Model, 60); v != "" {
		sess.model = v
	}
	if v := clampText(in.Task, 120); v != "" {
		sess.task = v
	}
	if v := clampText(in.Host, 40); v != "" {
		if h := normaliseClientType(v); h != "" {
			sess.host = h
		}
	}
	applyEnvOverridesLocked()
}

// clampText strips control characters, collapses whitespace, and trims to
// max runes. Everything here ends up in the popup and on the page, and a
// misbehaving (or prompt-injected) agent shouldn't be able to flood either.
func clampText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}

// displayNameLocked is the short "who" shown on the badge, toast, and as
// the popup row title. Caller holds sessionMu (read or write).
func displayNameLocked() string {
	switch {
	case sess.agentName != "":
		return sess.agentName
	case sess.host != "" && knownHosts[sess.host] != "":
		return knownHosts[sess.host]
	case sess.clientTitle != "":
		return sess.clientTitle
	case sess.clientName != "":
		return sess.clientName
	case sess.host != "":
		return sess.host
	}
	return "agent"
}

// labelLocked is the full one-line label: "<who> · <project>". Caller
// holds sessionMu.
func labelLocked() string {
	if sess.labelOverride != "" {
		return sess.labelOverride
	}
	who := displayNameLocked()
	if sess.project == "" {
		return who
	}
	return who + " · " + sess.project
}

// snapshotSession returns a copy of the current session info in the
// shape the daemon and the extension popup consume.
func snapshotSession() map[string]any {
	sessionMu.RLock()
	defer sessionMu.RUnlock()
	return map[string]any{
		"label":         labelLocked(),
		"displayName":   displayNameLocked(),
		"sessionType":   sess.host,
		"hostName":      hostDisplayName(sess.host),
		"agentName":     sess.agentName,
		"model":         sess.model,
		"task":          sess.task,
		"project":       sess.project,
		"clientName":    sess.clientName,
		"clientVersion": sess.clientVersion,
		"connectedAt":   sessionStartedAt.UnixMilli(),
		"pid":           os.Getpid(),
		"ppid":          os.Getppid(),
		"hue":           brandHue, // single-process mode = single agent = brand
	}
}

// getSessionLabel returns the current session label safely.
func getSessionLabel() string {
	sessionMu.RLock()
	defer sessionMu.RUnlock()
	return labelLocked()
}

// getSessionDisplayName returns the short "who" name safely.
func getSessionDisplayName() string {
	sessionMu.RLock()
	defer sessionMu.RUnlock()
	return displayNameLocked()
}

// getSessionType returns the canonical host id safely.
func getSessionType() string {
	sessionMu.RLock()
	defer sessionMu.RUnlock()
	return sess.host
}

// publishSession pushes updated identity everywhere it's displayed: the
// daemon (relay mode re-registers) and any connected browser popups.
func publishSession() {
	reRegisterWithDaemon()
	broadcastClientsToBrowsers()
}

// initializeHook returns an AddAfterInitialize hook that refines session info
// from the MCP initialize handshake.
func initializeHook() func(ctx context.Context, id any, req *mcp.InitializeRequest, res *mcp.InitializeResult) {
	return func(_ context.Context, _ any, req *mcp.InitializeRequest, _ *mcp.InitializeResult) {
		if req == nil {
			return
		}
		ci := req.Params.ClientInfo
		refineSessionFromInitialize(ci.Name, ci.Title, ci.Version)
		logger.Printf("MCP client identified: name=%q version=%q -> %s", ci.Name, ci.Version, getSessionLabel())
		publishSession()
	}
}

// ---------------------------------------------------------------------------
// Host detection heuristics
// ---------------------------------------------------------------------------

// normaliseClientType maps a client name (from initialize, or an agent's
// self-reported host) to a canonical host id. Unknown names are slugified
// and returned as-is so the popup shows the client's real name instead of
// a wrong guess.
//
// Order matters: names like "jcode", "opencode", and "roo-code" contain
// "code" and must be matched before anything VS Code-ish. We never match a
// bare "code" substring.
func normaliseClientType(name string) string {
	n := slugify(name)
	if n == "" {
		return ""
	}
	if _, ok := knownHosts[n]; ok {
		return n
	}
	has := func(s string) bool { return strings.Contains(n, s) }
	switch {
	case has("claude-code"):
		return "claude-code"
	case has("claude"): // Claude Desktop reports "claude-ai"
		return "claude-desktop"
	case has("cursor"): // Cursor reports "cursor-vscode"
		return "cursor"
	case has("windsurf"):
		return "windsurf"
	case has("jcode"):
		return "jcode"
	case has("opencode"):
		return "opencode"
	case has("roo-code") || has("roo-cline") || n == "roo":
		return "roo-code"
	case has("cline"):
		return "cline"
	case has("codex"):
		return "codex"
	case has("gemini"):
		return "gemini-cli"
	case has("goose"):
		return "goose"
	case has("kiro"):
		return "kiro"
	case has("inspector"):
		return "inspector"
	case has("lm-studio") || has("lmstudio"):
		return "lm-studio"
	case has("continue"):
		return "continue"
	case n == "zed" || strings.HasPrefix(n, "zed-"):
		return "zed"
	case n == "amp" || strings.HasPrefix(n, "amp-"):
		return "amp"
	case n == "warp" || strings.HasPrefix(n, "warp-"):
		return "warp"
	case has("visual-studio-code") || has("vscode") || has("vs-code") ||
		n == "code" || n == "code-insiders" || has("copilot"):
		return "vscode"
	}
	return n
}

// slugify lowercases and turns runs of non-alphanumerics into single dashes.
func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// wrapperProcs are launchers that sit between the real host and us. We
// walk through them instead of reporting "node" or "disclaimer".
var wrapperProcs = map[string]bool{
	"disclaimer": true, // Claude Desktop's spawn helper
	"node":       true, "npx": true, "npm": true, "pnpm": true, "yarn": true,
	"bun": true, "bunx": true, "deno": true,
	"uv": true, "uvx": true, "python": true, "python3": true,
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true,
	"env": true, "sudo": true, "timeout": true, "nohup": true,
	"script": true, "caffeinate": true, "turboweb-mcp-by-ikari": true,
}

// detectHostFromAncestry walks up the process tree (at most a few levels)
// looking for a recognisable MCP host, skipping wrapper processes.
func detectHostFromAncestry(ppid int) string {
	pid := ppid
	for depth := 0; depth < 6 && pid > 1; depth++ {
		parent, comm := processInfo(pid)
		if comm == "" {
			return ""
		}
		if h := hostFromProcess(comm); h != "" {
			return h
		}
		base := strings.ToLower(strings.TrimSuffix(filepath.Base(comm), ".exe"))
		base = strings.TrimPrefix(base, "-") // login shells show as "-zsh"
		if !wrapperProcs[base] && !strings.HasPrefix(base, "python") {
			// A real, unrecognised program launched us. Stop rather than
			// blame whatever terminal happens to be further up.
			return ""
		}
		if strings.HasPrefix(filepath.Base(comm), "-") {
			// Interactive login shell: a human launched us by hand.
			return ""
		}
		pid = parent
	}
	return ""
}

// hostFromProcess recognises a host from a process path or name. Uses
// .app bundle names on macOS (so Claude.app isn't confused with the
// `claude` CLI) and exact executable basenames elsewhere.
func hostFromProcess(comm string) string {
	lc := strings.ToLower(comm)
	bundles := []struct{ app, host string }{
		{"/claude.app/", "claude-desktop"},
		{"/cursor.app/", "cursor"},
		{"/windsurf.app/", "windsurf"},
		{"/visual studio code.app/", "vscode"},
		{"/visual studio code - insiders.app/", "vscode"},
		{"/zed.app/", "zed"},
		{"/zed preview.app/", "zed"},
		{"/kiro.app/", "kiro"},
		{"/lm studio.app/", "lm-studio"},
		{"/warp.app/", "warp"},
		{"/goose.app/", "goose"},
	}
	for _, b := range bundles {
		if strings.Contains(lc, b.app) {
			return b.host
		}
	}
	base := strings.TrimSuffix(filepath.Base(lc), ".exe")
	switch base {
	case "claude":
		return "claude-code" // the Claude Code CLI binary is `claude`
	case "jcode":
		return "jcode"
	case "codex":
		return "codex"
	case "gemini":
		return "gemini-cli"
	case "opencode":
		return "opencode"
	case "cursor", "cursor-agent":
		return "cursor"
	case "windsurf":
		return "windsurf"
	case "zed", "zed-editor":
		return "zed"
	case "goose":
		return "goose"
	case "amp":
		return "amp"
	case "kiro":
		return "kiro"
	case "code", "code-insiders", "code helper", "code helper (plugin)":
		return "vscode"
	}
	return ""
}

// processInfo returns (parentPID, command path) for pid via ps. Returns
// zero values when unavailable (Windows, sandboxed, process gone).
func processInfo(pid int) (int, string) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return 0, ""
	}
	out, err := exec.Command("ps", "-o", "ppid=", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, ""
	}
	return parsePsLine(string(out))
}

// parsePsLine parses "<ppid> <comm...>" (comm may contain spaces).
func parsePsLine(line string) (int, string) {
	line = strings.TrimSpace(line)
	ppidStr, comm, _ := strings.Cut(line, " ")
	ppid, err := strconv.Atoi(strings.TrimSpace(ppidStr))
	if err != nil {
		return 0, ""
	}
	return ppid, strings.TrimSpace(comm)
}

// detectHostFromEnv is the last-resort guess from inherited environment
// variables. Weakest signal: a jcode launched inside Claude Code's terminal
// would inherit CLAUDECODE=1, which is why ancestry is checked first.
func detectHostFromEnv(env []string) string {
	has := func(prefix string) bool {
		for _, kv := range env {
			if strings.HasPrefix(kv, prefix) {
				return true
			}
		}
		return false
	}
	switch {
	case has("JCODE_"):
		return "jcode"
	case has("CLAUDECODE=1") || has("CLAUDE_CODE_ENTRYPOINT="):
		return "claude-code"
	case has("CURSOR_TRACE_ID=") || has("CURSOR_AGENT="):
		return "cursor"
	case has("CODEX_SANDBOX") || has("CODEX_MANAGED_BY"):
		return "codex"
	case has("GEMINI_CLI=1"):
		return "gemini-cli"
	case has("OPENCODE="):
		return "opencode"
	}
	return ""
}

// describeSession is a human summary used in tool results.
func describeSession() string {
	sessionMu.RLock()
	defer sessionMu.RUnlock()
	parts := []string{fmt.Sprintf("%q", labelLocked())}
	if sess.host != "" {
		parts = append(parts, "host="+hostDisplayName(sess.host))
	}
	if sess.model != "" {
		parts = append(parts, "model="+sess.model)
	}
	if sess.task != "" {
		parts = append(parts, fmt.Sprintf("task=%q", sess.task))
	}
	return strings.Join(parts, ", ")
}
