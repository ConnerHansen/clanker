package tools

import (
	"clankerv2/agent/structs"
	"clankerv2/data"
	"fmt"
	"strings"
)

// handleModify implements system-modify: content-anchored, per-request atomic
// (or nonAtomic per-change) edits. It simulates every change against fresh file
// reads, gates the write on a user approval showing the combined diff, and only
// then applies — returning fresh numbered context around each splice in NEW
// line coordinates so follow-up edits can anchor without a re-read.
func handleModify(ctx *HandlerCtx, name string) structs.ToolCallResult {
	rawChanges, ok := ctx.args["changes"].([]any)
	if !ok || len(rawChanges) == 0 {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "changes is required (non-empty array of {file, type, ...})"}}
	}

	changes := make([]Change, 0, len(rawChanges))
	for i, raw := range rawChanges {
		c, ok := raw.(map[string]any)
		if !ok {
			return structs.ToolCallResult{Success: false, Result: map[string]any{"error": fmt.Sprintf("change %d is not an object", i+1)}}
		}

		file, _ := c["file"].(string)
		if file == "" {
			return structs.ToolCallResult{Success: false, Result: map[string]any{"error": fmt.Sprintf("change %d is missing required field 'file'", i+1)}}
		}

		chType, _ := c["type"].(string)
		if chType != "replace" && chType != "anchor" {
			return structs.ToolCallResult{Success: false, Result: map[string]any{"error": fmt.Sprintf("change %d 'type' must be \"replace\" or \"anchor\", got %q", i+1, chType)}}
		}

		oldStr, ok := strField(c, "old")
		if !ok {
			return structs.ToolCallResult{Success: false, Result: map[string]any{"error": fmt.Sprintf("change %d 'old' must be a string", i+1)}}
		}
		startStr, ok := strField(c, "start")
		if !ok {
			return structs.ToolCallResult{Success: false, Result: map[string]any{"error": fmt.Sprintf("change %d 'start' must be a string (or null for the file start)", i+1)}}
		}
		endStr, ok := strField(c, "end")
		if !ok {
			return structs.ToolCallResult{Success: false, Result: map[string]any{"error": fmt.Sprintf("change %d 'end' must be a string (or null for the file end)", i+1)}}
		}
		newStr, ok := c["new"].(string)
		if !ok {
			return structs.ToolCallResult{Success: false, Result: map[string]any{"error": fmt.Sprintf("change %d 'new' must be a string (the replacement content; empty string deletes the region)", i+1)}}
		}

		changes = append(changes, Change{File: file, Type: chType, Old: oldStr, Start: startStr, End: endStr, New: newStr})
	}

	// Parse optional nonAtomic flag: when true, each change resolves
	// independently and successful changes land even if others fail.
	nonAtomic := false
	if na, ok := ctx.args["nonAtomic"].(bool); ok {
		nonAtomic = na
	}

	var simulated map[string]string
	var failures []ApplyChangesItemResult
	var contexts map[string]string

	if nonAtomic {
		simulated, failures, contexts = SimulateApplyChangesNonAtomic(changes)
	} else {
		// Atomic per request: if ANY block fails to anchor, NOTHING is written.
		simulated, failures, contexts = SimulateApplyChanges(changes)
	}

	// Non-atomic partial success: some changes landed, some failed.
	// Apply the successful subset and report per-change results.
	if nonAtomic && len(failures) > 0 && len(simulated) > 0 {
		var combinedDiff strings.Builder
		for file, newContent := range simulated {
			diff, err := GenerateDiff(file, newContent)
			if err != nil {
				fmt.Fprintf(&combinedDiff, "Error generating diff for %s: %v\n", file, err)
			} else {
				fmt.Fprintf(&combinedDiff, "--- %s\n%s\n", file, diff)
			}
		}

		repPath := changes[0].File
		diffContent := combinedDiff.String()
		modifyPrompt := fmt.Sprintf("Modify %d file(s) (non-atomic)", len(simulated))
		if len(simulated) == 1 {
			modifyPrompt = "Modify file: " + repPath + " (non-atomic)"
		}
		permResult := ctx.requestPermission(ctx.channel, "system-modify", repPath, "update", modifyPrompt, &data.PermissionViewport{Kind: "diff", Content: diffContent})
		if strings.Contains(permResult, "denied") {
			return structs.ToolCallResult{Success: false, Result: map[string]any{"result": permResult}}
		}

		result := ApplyChanges(simulated)
		out := toResultMap(result)
		if result.Status == "success" {
			out["context"] = contexts
		}
		out["results"] = failures // in non-atomic mode, this is ALL per-change results
		failedCount := 0
		for _, f := range failures {
			if !f.Successful {
				failedCount++
			}
		}
		out["status"] = "partial"
		out["applied"] = len(changes) - failedCount
		out["total"] = len(changes)
		var failReasons []string
		for _, f := range failures {
			if !f.Successful {
				failReasons = append(failReasons, fmt.Sprintf("change %d (%s): %s", f.ChangeNum, f.File, f.Error))
			}
		}
		out["failed_changes"] = failReasons
		return structs.ToolCallResult{Success: true, Result: out}
	}

	if len(failures) > 0 {
		var reasons []string
		for _, f := range failures {
			reasons = append(reasons, f.File+": "+f.Error)
		}
		errMsg := "no changes were applied; failures: " + strings.Join(reasons, "; ")
		if !nonAtomic {
			errMsg = "no changes were applied (modify is atomic per request); failures: " + strings.Join(reasons, "; ")
		}
		return structs.ToolCallResult{Success: false, Result: toResultMap(map[string]any{
			"status":  "error",
			"applied": 0,
			"error":   errMsg,
			"results": failures,
		})}
	}

	var combinedDiff strings.Builder
	for file, newContent := range simulated {
		diff, err := GenerateDiff(file, newContent)
		if err != nil {
			fmt.Fprintf(&combinedDiff, "Error generating diff for %s: %v\n", file, err)
		} else {
			fmt.Fprintf(&combinedDiff, "--- %s\n%s\n", file, diff)
		}
	}

	repPath := ""
	if len(changes) > 0 {
		repPath = changes[0].File
	}
	diffContent := combinedDiff.String()
	modifyPrompt := fmt.Sprintf("Modify %d file(s)", len(simulated))
	if len(simulated) == 1 {
		modifyPrompt = "Modify file: " + repPath
	} else if repPath != "" {
		modifyPrompt += ", e.g. " + repPath
	}
	permResult := ctx.requestPermission(ctx.channel, "system-modify", repPath, "update", modifyPrompt, &data.PermissionViewport{Kind: "diff", Content: diffContent})
	if strings.Contains(permResult, "denied") {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"result": permResult}}
	}

	result := ApplyChanges(simulated)
	out := toResultMap(result)
	if result.Status == "success" {
		// Fresh numbered context around each splice, in NEW line coordinates:
		// follow-up edits can anchor off it without a full re-read. (Computed
		// during the pre-write simulate pass above, not re-run after writing.)
		out["context"] = contexts
	}
	return structs.ToolCallResult{Success: result.Status == "success", Result: out}
}

func init() { registerTool("system-modify", handleModify) }
