package tools

import (
	"clankerv2/agent/structs"
)

// handleFind implements system-find: regex search across the codebase.
func handleFind(ctx *HandlerCtx, name string) structs.ToolCallResult {
	regex, ok := ctx.args["regex"].(string)
	if !ok || regex == "" {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "regex is required"}}
	}

	var rootPathPtr *string
	if rp, ok := ctx.args["path"].(string); ok && rp != "" {
		rootPathPtr = &rp
	}

	result := FindInCodebase(regex, rootPathPtr)
	return structs.ToolCallResult{Success: result.Status == "success", Result: toResultMap(result)}
}

func init() { registerTool("system-find", handleFind) }
