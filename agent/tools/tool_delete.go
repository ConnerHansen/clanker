package tools

import (
	"clankerv2/agent/structs"
	"clankerv2/data"
	"fmt"
	"os"
	"strings"
)

// handleDelete implements system-delete: remove a single file, gated on the
// target being a real file BEFORE prompting and previewing the full removal as
// a diff in the approval viewport.
func handleDelete(ctx *HandlerCtx, name string) structs.ToolCallResult {
	path, ok := ctx.args["path"].(string)
	if !ok || path == "" {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "path is required"}}
	}

	// Gate on the target BEFORE prompting so the user never sees a spurious
	// approval for something that cannot be deleted (missing path or a
	// directory — only files are supported).
	st, err := os.Stat(path)
	if err != nil {
		hint := ""
		if os.IsNotExist(err) {
			hint = " (file does not exist)"
		}
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": fmt.Sprintf("failed to stat %s: %v%s", path, err, hint)}}
	}
	if st.IsDir() {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "path is a directory; only files can be deleted"}}
	}

	// Diff against empty so the approval viewport previews the full removal.
	diff, dErr := GenerateDiff(path, "")
	var vp *data.PermissionViewport
	if dErr == nil {
		vp = &data.PermissionViewport{Kind: "diff", Content: diff}
	}

	permissionResult := ctx.requestPermission(ctx.channel, "system-delete", path, "delete", "Delete file: "+path, vp)
	if strings.Contains(permissionResult, "denied") {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"result": permissionResult}}
	}

	result := DeleteFile(path)
	return structs.ToolCallResult{Success: result.Status == "success", Result: toResultMap(result)}
}

func init() { registerTool("system-delete", handleDelete) }
