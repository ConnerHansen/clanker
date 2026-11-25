package tools

import (
	"clankerv2/agent/structs"
	"fmt"
	"strings"
)

// handleSpawnSubagent implements system-spawn_subagent.
func handleSpawnSubagent(ctx *HandlerCtx, name string) structs.ToolCallResult {
	if subagentBackend == nil || ctx.rt == nil {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "subagents are not available in this context"}}
	}
	task, _ := ctx.args["task"].(string)
	if strings.TrimSpace(task) == "" {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "'task' is required (what should the subagent do?)"}}
	}
	id, err := subagentBackend.Spawn(ctx.rt.RunID(), ctx.rt.OwnerModel(), ctx.args)
	if err != nil {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": err.Error()}}
	}
	return structs.ToolCallResult{
		Success: true,
		Result:  toResultMap(map[string]any{"status": "spawned", "id": id, "note": "subagent launched in the background (may be queued). Track it with system-subagent_status and collect its answer with system-await_subagent."}),
	}
}

// handleAwaitSubagent implements system-await_subagent.
func handleAwaitSubagent(ctx *HandlerCtx, name string) structs.ToolCallResult {
	if subagentBackend == nil {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "subagents are not available in this context"}}
	}
	id, _ := ctx.args["id"].(string)
	if strings.TrimSpace(id) == "" {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "'id' is required (the subagent to wait on)"}}
	}
	timeout := 0
	if n, ok := ctx.args["timeoutSeconds"]; ok {
		if f, ok2 := n.(float64); ok2 {
			timeout = int(f)
		} else if i, ok2 := n.(int); ok2 {
			timeout = i
		}
	}
	return structs.ToolCallResult{Success: true, Result: subagentBackend.Await(id, timeout)}
}

// handleSubagentStatus implements system-subagent_status.
func handleSubagentStatus(ctx *HandlerCtx, name string) structs.ToolCallResult {
	if subagentBackend == nil || ctx.rt == nil {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "subagents are not available in this context"}}
	}
	id, _ := ctx.args["id"].(string)
	all := subagentBackend.List(ctx.rt.RunID())
	if strings.TrimSpace(id) != "" {
		for _, m := range all {
			if m["id"] == id {
				return structs.ToolCallResult{Success: true, Result: toResultMap(map[string]any{"subagent": m})}
			}
		}
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "no subagent with id " + id}}
	}
	return structs.ToolCallResult{Success: true, Result: toResultMap(map[string]any{"subagents": all})}
}

// handleKillSubagent implements system-kill_subagent.
func handleKillSubagent(ctx *HandlerCtx, name string) structs.ToolCallResult {
	if subagentBackend == nil {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "subagents are not available in this context"}}
	}
	id, _ := ctx.args["id"].(string)
	if strings.TrimSpace(id) == "" {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "'id' is required (the subagent to kill)"}}
	}
	if !subagentBackend.Kill(id) {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": fmt.Sprintf("could not kill %s (unknown or already finished)", id)}}
	}
	return structs.ToolCallResult{Success: true, Result: toResultMap(map[string]any{"status": "kill_requested", "id": id, "note": "subagent is being stopped; its final status will be killed/timed_out."})}
}

// handleSubagentFinalize implements system-subagent_finalize: only valid inside
// a subagent run; records the deliverable and stops the run's turn loop.
func handleSubagentFinalize(ctx *HandlerCtx, name string) structs.ToolCallResult {
	fin, ok := ctx.rt.(SubagentFinalizer)
	if !ok {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "this run is not a subagent; finalize is only available inside subagents"}}
	}
	summary, _ := ctx.args["summary"].(string)
	if strings.TrimSpace(summary) == "" {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "'summary' is required (your final answer for the calling agent)"}}
	}
	fin.Finalize(summary)
	return structs.ToolCallResult{
		Success:   true,
		RawResult: func() *string { s := summary; return &s }(),
		Result:    toResultMap(map[string]any{"status": "finalized", "message": fmt.Sprintf("Your answer (%d chars) will be delivered to the calling agent. Stop now.", len(summary))}),
	}
}

func init() {
	registerTool("system-spawn_subagent", handleSpawnSubagent)
	registerTool("system-await_subagent", handleAwaitSubagent)
	registerTool("system-subagent_status", handleSubagentStatus)
	registerTool("system-kill_subagent", handleKillSubagent)
	registerTool("system-subagent_finalize", handleSubagentFinalize)
}
