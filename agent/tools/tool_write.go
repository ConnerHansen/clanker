package tools

import (
	"clankerv2/agent/structs"
	"clankerv2/data"
	"os"
	"strings"
)

// handleWrite implements system-write: create or overwrite a file, gated on a
// user approval whose viewport previews the full diff.
func handleWrite(ctx *HandlerCtx, name string) structs.ToolCallResult {
	path, ok := ctx.args["path"].(string)
	if !ok || path == "" {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "path is required"}}
	}

	mode, _ := ctx.args["mode"].(string)
	if mode == "" {
		mode = "content"
	}

	content, _ := ctx.args["content"].(string)

	action := "create"
	writePrompt := "Create new file: " + path
	if _, err := os.Stat(path); err == nil {
		action = "overwrite"
		writePrompt = "Overwrite existing file: " + path
	}

	diff, err := GenerateDiff(path, content)
	var vp *data.PermissionViewport
	if err == nil {
		vp = &data.PermissionViewport{Kind: "diff", Content: diff}
	}

	permissionResult := ctx.requestPermission(ctx.channel, "system-write", path, action, writePrompt, vp)
	if strings.Contains(permissionResult, "denied") {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"result": permissionResult}}
	}

	result := WriteFile(path, content)
	return structs.ToolCallResult{Success: result.Status == "success", Result: toResultMap(result)}
}

func init() { registerTool("system-write", handleWrite) }
