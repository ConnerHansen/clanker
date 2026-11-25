package tools

import (
	"clankerv2/agent/structs"
)

// handleSearchByName implements system-search_by_name: glob filename search.
func handleSearchByName(ctx *HandlerCtx, name string) structs.ToolCallResult {
	pattern, ok := ctx.args["pattern"].(string)
	if !ok || pattern == "" {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "pattern is required"}}
	}

	var rootPathPtr *string
	if rp, ok := ctx.args["path"].(string); ok && rp != "" {
		rootPathPtr = &rp
	}

	result := SearchFilesByName(pattern, rootPathPtr)
	return structs.ToolCallResult{Success: result.Status == "success", Result: toResultMap(result)}
}

func init() { registerTool("system-search_by_name", handleSearchByName) }
