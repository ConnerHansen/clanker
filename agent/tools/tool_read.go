package tools

import (
	"clankerv2/agent/structs"
)

// handleRead implements system-read: line-numbered or legacy plain content per
// the readNumbered switch.
func handleRead(ctx *HandlerCtx, name string) structs.ToolCallResult {
	path, ok := ctx.args["path"].(string)
	if !ok || path == "" {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "path is required"}}
	}

	var linePtr *int
	if l, ok2 := toInt(ctx.args["line"]); ok2 {
		linePtr = &l
	}
	var readLinesPtr *int
	if r, ok2 := toInt(ctx.args["readLines"]); ok2 {
		readLinesPtr = &r
	}
	var maxBytesPtr *int
	if mb, ok2 := toInt(ctx.args["maxBytes"]); ok2 {
		maxBytesPtr = &mb
	}

	var result *ReadFileResult
	if readNumbered {
		result = ReadFileNumbered(path, linePtr, readLinesPtr, maxBytesPtr)
	} else {
		result = ReadFile(path, linePtr, readLinesPtr, maxBytesPtr)
	}
	return structs.ToolCallResult{Success: result.Status == "success", Result: toResultMap(result), RawResult: &result.Content}
}

func init() { registerTool("system-read", handleRead) }
