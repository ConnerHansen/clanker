package tools

import (
	"clankerv2/agent/structs"
)

// handleList implements system-list: directory listing with optional
// recursion, depth cap, and hidden-file inclusion.
func handleList(ctx *HandlerCtx, name string) structs.ToolCallResult {
	path, _ := ctx.args["path"].(string)

	var recursivePtr *bool
	if rb, ok := ctx.args["recursive"].(bool); ok {
		recursivePtr = &rb
	}
	var maxDepthPtr *int
	if md, ok := toInt(ctx.args["maxDepth"]); ok {
		maxDepthPtr = &md
	}
	var showHiddenPtr *bool
	if sh, ok := ctx.args["showHidden"].(bool); ok {
		showHiddenPtr = &sh
	}

	result := ListDirectory(path, recursivePtr, maxDepthPtr, showHiddenPtr)
	return structs.ToolCallResult{Success: result.Status == "success", Result: toResultMap(result)}
}

func init() { registerTool("system-list", handleList) }
