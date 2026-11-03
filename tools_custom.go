package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	_ "modernc.org/sqlite"
)

var db *sql.DB

const (
	configDirName = "turboweb-mcp-by-ikari"
	dbFileName    = "turboweb.db"
)

// getConfigDir returns the directory holding turboweb's per-user state.
// Honours XDG_CONFIG_HOME (Linux), APPDATA (Windows), or ~/.config (mac
// and Linux fallback).
func getConfigDir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, configDirName)
	}
	if runtime.GOOS == "windows" {
		if dir := os.Getenv("APPDATA"); dir != "" {
			return filepath.Join(dir, configDirName)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", "."+configDirName)
	}
	return filepath.Join(home, ".config", configDirName)
}

func initDB() error {
	configDir := getConfigDir()
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	dbPath := filepath.Join(configDir, dbFileName)

	var err error
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}

	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA busy_timeout=3000")

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS custom_tools (
		name        TEXT PRIMARY KEY,
		description TEXT NOT NULL,
		code        TEXT NOT NULL,
		params      TEXT NOT NULL DEFAULT '[]',
		system_prompt TEXT,
		created_at  TEXT NOT NULL DEFAULT (datetime('now'))
	)`)
	if err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	// Attempt legacy JSON migration
	migrateLegacyJSON()

	logger.Printf("Database: %s", dbPath)
	return nil
}

func migrateLegacyJSON() {
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM custom_tools").Scan(&count); err != nil || count > 0 {
		return
	}

	// Look for legacy JSON next to the binary
	exe, err := os.Executable()
	if err != nil {
		return
	}
	legacyPath := filepath.Join(filepath.Dir(exe), "custom-tools.json")
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		// Also try CWD
		data, err = os.ReadFile("custom-tools.json")
		if err != nil {
			return
		}
	}

	var tools []struct {
		Name         string `json:"name"`
		Description  string `json:"description"`
		Code         string `json:"code"`
		Params       any    `json:"params"`
		SystemPrompt string `json:"systemPrompt"`
		CreatedAt    string `json:"createdAt"`
	}
	if json.Unmarshal(data, &tools) != nil {
		return
	}

	tx, err := db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()

	stmt, _ := tx.Prepare("INSERT OR IGNORE INTO custom_tools (name, description, code, params, system_prompt, created_at) VALUES (?, ?, ?, ?, ?, ?)")
	defer stmt.Close()

	for _, t := range tools {
		params, _ := json.Marshal(t.Params)
		var sp *string
		if t.SystemPrompt != "" {
			sp = &t.SystemPrompt
		}
		stmt.Exec(t.Name, t.Description, t.Code, string(params), sp, t.CreatedAt)
	}
	tx.Commit()
	logger.Printf("Migrated %d custom tool(s) from legacy JSON", len(tools))
}

// CRUD operations

type CustomTool struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Code         string `json:"code"`
	Params       string `json:"params"` // JSON array
	SystemPrompt *string `json:"systemPrompt,omitempty"`
	CreatedAt    string `json:"createdAt"`
}

func saveTool(t CustomTool) error {
	_, err := db.Exec(
		`INSERT INTO custom_tools (name, description, code, params, system_prompt, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET description=excluded.description, code=excluded.code, params=excluded.params, system_prompt=excluded.system_prompt`,
		t.Name, t.Description, t.Code, t.Params, t.SystemPrompt, t.CreatedAt,
	)
	return err
}

func getTool(name string) (*CustomTool, error) {
	row := db.QueryRow("SELECT name, description, code, params, system_prompt, created_at FROM custom_tools WHERE name = ?", name)
	var t CustomTool
	err := row.Scan(&t.Name, &t.Description, &t.Code, &t.Params, &t.SystemPrompt, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func getAllTools() ([]CustomTool, error) {
	rows, err := db.Query("SELECT name, description, code, params, system_prompt, created_at FROM custom_tools ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tools []CustomTool
	for rows.Next() {
		var t CustomTool
		if err := rows.Scan(&t.Name, &t.Description, &t.Code, &t.Params, &t.SystemPrompt, &t.CreatedAt); err != nil {
			continue
		}
		tools = append(tools, t)
	}
	return tools, nil
}

func deleteTool(name string) (bool, error) {
	res, err := db.Exec("DELETE FROM custom_tools WHERE name = ?", name)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func toolCount() int {
	var n int
	db.QueryRow("SELECT COUNT(*) FROM custom_tools").Scan(&n)
	return n
}

// MCP tool registration

func registerCustomTools(s *server.MCPServer) {
	// --- create_tool ---
	addTool(s,
		mcp.NewTool("create_tool",
			mcp.WithDescription("Create a reusable custom tool. The tool is saved to disk and persists across sessions. The code runs in the page context with access to a `params` object. Use `run_tool` to execute it later."),
			mcp.WithString("name", mcp.Required(), mcp.Description(`Tool name (lowercase, no spaces — e.g. "get_field_info")`)),
			mcp.WithString("description", mcp.Required(), mcp.Description("What the tool does")),
			mcp.WithString("code", mcp.Required(), mcp.Description("JavaScript code to execute in page context. Receives `params` object. Use `return` to return a value.")),
			mcp.WithArray("params",
				mcp.Description("Tool parameters (in addition to tabId which is always available)"),
				mcp.Items(map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":        map[string]any{"type": "string", "description": "Parameter name"},
						"type":        map[string]any{"type": "string", "enum": []string{"string", "number", "boolean"}, "description": "Parameter type"},
						"description": map[string]any{"type": "string", "description": "What this parameter does"},
						"required":    map[string]any{"type": "boolean", "description": "Is this required? (default false)"},
					},
				}),
			),
			mcp.WithString("systemPrompt", mcp.Description(`Baked-in context and instructions for Haiku when processing this tool's results.`)),
		),
		handleCreateTool,
	)

	// --- run_tool ---
	addTool(s,
		mcp.NewTool("run_tool",
			mcp.WithDescription("Run a previously created custom tool by name. Pass arguments as a JSON object."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Name of the custom tool to run")),
			mcp.WithObject("args", mcp.Description("Arguments to pass to the tool (as JSON object)")),
			tabIDParam(),
			mcp.WithString("question", mcp.Description("If provided, Haiku processes the result and answers this question")),
		),
		handleRunTool,
	)

	// --- list_custom_tools ---
	addTool(s,
		mcp.NewTool("list_custom_tools",
			mcp.WithDescription("List all saved custom tools with their descriptions and parameters."),
		),
		handleListCustomTools,
	)

	// --- delete_tool ---
	addTool(s,
		mcp.NewTool("delete_tool",
			mcp.WithDescription("Delete a custom tool by name."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Name of the custom tool to delete")),
		),
		handleDeleteTool,
	)
}

func handleCreateTool(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if db == nil {
		return mcp.NewToolResultError("Database not initialized"), nil
	}
	args := req.GetArguments()
	name := getString(args, "name", "")
	desc := getString(args, "description", "")
	code := getString(args, "code", "")

	paramsJSON := "[]"
	if p, ok := args["params"]; ok && p != nil {
		b, _ := json.Marshal(p)
		paramsJSON = string(b)
	}
	var sp *string
	if s := getString(args, "systemPrompt", ""); s != "" {
		sp = &s
	}

	tool := CustomTool{
		Name:         name,
		Description:  desc,
		Code:         code,
		Params:       paramsJSON,
		SystemPrompt: sp,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	if err := saveTool(tool); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to save tool: %v", err)), nil
	}

	logger.Printf("Custom tool created: %s", name)

	// Parse params for the usage hint
	var params []struct {
		Name string `json:"name"`
	}
	json.Unmarshal([]byte(paramsJSON), &params)
	paramNames := make([]string, len(params))
	for i, p := range params {
		paramNames[i] = p.Name + ": ..."
	}

	return textResult(map[string]any{
		"created":     name,
		"description": desc,
		"params":      paramNames,
		"usage":       fmt.Sprintf(`run_tool(name: "%s", args: {%s})`, name, join(paramNames, ", ")),
	})
}

func handleRunTool(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if db == nil {
		return mcp.NewToolResultError("Database not initialized"), nil
	}
	args := req.GetArguments()
	name := getString(args, "name", "")
	question := getString(args, "question", "")

	tool, err := getTool(name)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("DB error: %v", err)), nil
	}
	if tool == nil {
		all, _ := getAllTools()
		names := make([]string, len(all))
		for i, t := range all {
			names[i] = t.Name
		}
		return mcp.NewToolResultError(fmt.Sprintf("Custom tool '%s' not found. Available: %s", name, join(names, ", "))), nil
	}

	// Build JS code
	toolArgs := map[string]any{}
	if a, ok := args["args"]; ok && a != nil {
		if m, ok := a.(map[string]any); ok {
			toolArgs = m
		}
	}
	argsJSON, _ := json.Marshal(toolArgs)
	code := fmt.Sprintf("(async function(params) { %s })(%s)", tool.Code, string(argsJSON))

	sendParams := map[string]any{"code": code}
	if tabID, ok := args["tabId"]; ok && tabID != nil {
		sendParams["tabId"] = tabID
	}

	raw, err := send("execute_js", sendParams)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// If tool has systemPrompt, always pipe through Haiku
	if tool.SystemPrompt != nil && *tool.SystemPrompt != "" {
		q := question
		if q == "" {
			q = "Analyze and summarize the result."
		}
		return maybeAskWithSystem(raw, q, *tool.SystemPrompt, "")
	}
	return maybeAsk(raw, question, "")
}

func handleListCustomTools(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if db == nil {
		return mcp.NewToolResultError("Database not initialized"), nil
	}
	all, err := getAllTools()
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("DB error: %v", err)), nil
	}

	type toolSummary struct {
		Name               string `json:"name"`
		Description        string `json:"description"`
		Params             any    `json:"params"`
		HasSystemPrompt    bool   `json:"hasSystemPrompt"`
		SystemPromptPreview string `json:"systemPromptPreview,omitempty"`
		CreatedAt          string `json:"createdAt"`
	}

	tools := make([]toolSummary, len(all))
	for i, t := range all {
		var params any
		json.Unmarshal([]byte(t.Params), &params)
		s := toolSummary{
			Name:        t.Name,
			Description: t.Description,
			Params:      params,
			CreatedAt:   t.CreatedAt,
		}
		if t.SystemPrompt != nil && *t.SystemPrompt != "" {
			s.HasSystemPrompt = true
			preview := *t.SystemPrompt
			if len(preview) > 100 {
				preview = preview[:100]
			}
			s.SystemPromptPreview = preview
		}
		tools[i] = s
	}

	return textResult(map[string]any{
		"count": len(tools),
		"tools": tools,
	})
}

func handleDeleteTool(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if db == nil {
		return mcp.NewToolResultError("Database not initialized"), nil
	}
	args := req.GetArguments()
	name := getString(args, "name", "")

	deleted, err := deleteTool(name)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("DB error: %v", err)), nil
	}
	if !deleted {
		return mcp.NewToolResultError(fmt.Sprintf("Custom tool '%s' not found", name)), nil
	}

	logger.Printf("Custom tool deleted: %s", name)
	return textResult(map[string]any{
		"deleted":   name,
		"remaining": toolCount(),
	})
}

// join is a simple string join helper.
func join(parts []string, sep string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += sep
		}
		result += p
	}
	return result
}
