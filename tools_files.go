package main

import (
	"context"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerFileTools(s *server.MCPServer) {
	addTool(s,
		mcp.NewTool("set_input_files",
			mcp.WithDescription(
				"Attach files to an <input type=file> via CDP DOM.setFileInputFiles. "+
					"Works on hidden/styled inputs and triggers the change event as if the user picked. "+
					"Paths must be absolute on the MCP server host; ~ is expanded; symlinks are resolved. "+
					"If the selector matches a wrapper/label, we walk to the nearest descendant input[type=file].",
			),
			mcp.WithString("selector", mcp.Required(), mcp.Description("CSS selector of the file input (or its visible label/wrapper)")),
			mcp.WithArray("files", mcp.Required(),
				mcp.Description("Absolute filesystem paths on the MCP server host. Relative paths are rejected. ~/foo is expanded."),
				mcp.Items(map[string]any{"type": "string"}),
			),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		handleSetInputFiles,
	)

	addTool(s,
		mcp.NewTool("intercept_file_chooser",
			mcp.WithDescription(
				"Arm or disarm interception of the native file chooser. While armed, the next file-chooser dialog "+
					"is auto-fulfilled with the supplied paths — useful when the agent must click an Upload button "+
					"whose <input type=file> is hidden, dispatched-via-button, lazy-mounted, or cross-frame. "+
					"Workflow: arm with enable=true + files, click the upload control, then disarm with enable=false.",
			),
			mcp.WithBoolean("enable", mcp.Required(), mcp.Description("Arm (true) or disarm (false) interception for this tab")),
			mcp.WithArray("files",
				mcp.Description("Files to auto-attach when a chooser opens. Required when enable=true."),
				mcp.Items(map[string]any{"type": "string"}),
			),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		handleInterceptFileChooser,
	)

	addTool(s,
		mcp.NewTool("drag_drop_file",
			mcp.WithDescription(
				"Upload a local file by simulating a drag-and-drop onto a drop zone — a non-CDP fallback "+
					"for set_input_files. Use when the upload widget is a drag-drop zone with no <input type=file>, "+
					"or when CDP/chrome.debugger is unavailable (DevTools open, Firefox, policy-blocked). "+
					"Synthesizes a real File in the page and dispatches dragenter/dragover/drop carrying it. "+
					"If the target resolves to an <input type=file> it assigns input.files directly instead. "+
					"Some frameworks that gate on event.isTrusted will ignore this — prefer set_input_files when a file input exists.",
			),
			mcp.WithString("selector", mcp.Required(),
				mcp.Description("CSS selector of the drop zone (the element with the drop handler, a wrapper, or an <input type=file>)")),
			mcp.WithString("localFilePath", mcp.Required(),
				mcp.Description("Absolute filesystem path on the MCP server host. Relative paths are rejected; ~/foo is expanded; symlinks are resolved.")),
			mcp.WithString("mimeType",
				mcp.Description("Override the File MIME type. Default: sniffed from the file extension, falling back to application/octet-stream.")),
			mcp.WithNumber("tabId", mcp.Description("Tab ID (omit for active tab)")),
		),
		handleDragDropFile,
	)
}

// resolveHostPath expands ~, requires an absolute path, resolves symlinks via
// realpath, and verifies the result is an existing regular file. It returns
// the canonical absolute path and its os.FileInfo. This is the per-entry
// validation shared by resolveHostPaths (set_input_files) and the
// single-path drag_drop_file tool. `label` is used in error messages so the
// caller can identify which argument failed.
func resolveHostPath(s, label string) (string, os.FileInfo, error) {
	if s == "" {
		return "", nil, fmt.Errorf("%s: not a non-empty string", label)
	}
	if s == "~" || strings.HasPrefix(s, "~/") {
		home, _ := os.UserHomeDir()
		if home == "" {
			return "", nil, fmt.Errorf("%s: cannot expand ~ (no $HOME)", label)
		}
		s = filepath.Join(home, strings.TrimPrefix(s, "~"))
	}
	if !filepath.IsAbs(s) {
		return "", nil, fmt.Errorf("%s: must be an absolute path on the MCP server host (got %q)", label, s)
	}
	resolved, err := filepath.EvalSymlinks(s)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %v", label, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %v", label, err)
	}
	if info.IsDir() {
		return "", nil, fmt.Errorf("%s: %s is a directory, not a file", label, resolved)
	}
	return resolved, info, nil
}

// resolveHostPaths expands ~, requires absolute paths, resolves symlinks via
// realpath, and verifies each entry is an existing regular file. The
// returned slice contains canonical absolute paths suitable for CDP
// DOM.setFileInputFiles, which follows symlinks but reports broken ones
// with unhelpful messages.
func resolveHostPaths(raw any) ([]string, error) {
	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("files: expected an array of strings")
	}
	if len(arr) == 0 {
		return nil, fmt.Errorf("files: empty array")
	}
	out := make([]string, 0, len(arr))
	for i, v := range arr {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("files[%d]: not a non-empty string", i)
		}
		resolved, _, err := resolveHostPath(s, fmt.Sprintf("files[%d]", i))
		if err != nil {
			return nil, err
		}
		out = append(out, resolved)
	}
	return out, nil
}

// sniffMimeType resolves the MIME type for a drag_drop_file upload: an
// explicit override wins, otherwise it is guessed from the file extension,
// falling back to application/octet-stream when the extension is unknown.
func sniffMimeType(override, path string) string {
	if override != "" {
		return override
	}
	if t := mime.TypeByExtension(filepath.Ext(path)); t != "" {
		return t
	}
	return "application/octet-stream"
}

func handleSetInputFiles(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	selector := toString(args["selector"])
	if selector == "" {
		return mcp.NewToolResultError("selector is required"), nil
	}
	paths, err := resolveHostPaths(args["files"])
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	fwd := rawArgs(args)
	fwd["selector"] = selector
	fwd["files"] = paths
	raw, err := send("set_input_files", fwd)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(raw)), nil
}

func handleInterceptFileChooser(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	enable := toBool(args["enable"])
	fwd := rawArgs(args)
	fwd["enable"] = enable
	if enable {
		paths, err := resolveHostPaths(args["files"])
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		fwd["files"] = paths
	} else {
		delete(fwd, "files")
	}
	raw, err := send("intercept_file_chooser", fwd)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(raw)), nil
}

// handleDragDropFile is the non-CDP file-upload fallback. It validates the
// host path, mints a single-use token on the loopback file host, and lets
// the content script fetch the bytes and synthesize a drop. The file bytes
// never traverse the WS channel — only the small token/metadata JSON does.
func handleDragDropFile(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	selector := toString(args["selector"])
	if selector == "" {
		return mcp.NewToolResultError("selector is required"), nil
	}
	rawPath := toString(args["localFilePath"])
	absPath, info, err := resolveHostPath(rawPath, "localFilePath")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	mimeType := sniffMimeType(toString(args["mimeType"]), absPath)

	// Start the loopback file host (idempotent) and mint a one-shot grant
	// for exactly this file.
	port, err := ensureFileHost()
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	token, err := fileHost.mintFileGrant(absPath, filepath.Base(absPath), mimeType, info.Size())
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// Forward only metadata + the token; the bytes flow over the separate
	// :18323 HTTP channel. The token is intentionally not echoed into any
	// intent/log surface.
	fwd := rawArgs(args)
	delete(fwd, "localFilePath")
	fwd["selector"] = selector
	// The sniffed/overridden MIME type is forwarded below as fwd["mimeType"];
	// drop the raw input arg of the same name so it cannot shadow it.
	delete(fwd, "mimeType")
	fwd["fileName"] = filepath.Base(absPath)
	fwd["mimeType"] = mimeType
	fwd["size"] = info.Size()
	fwd["fileToken"] = token
	fwd["fileHostPort"] = port

	// A drag-drop fetches the file inside the page, which can outrun a
	// click; pass a generous timeout (the default is already 30s, but be
	// explicit since this tool is I/O-bound).
	raw, err := send("drag_drop_file", fwd, 30000)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(raw)), nil
}
