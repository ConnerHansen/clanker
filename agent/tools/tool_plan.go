package tools

import (
	"clankerv2/agent/structs"
	"fmt"
	"strings"
)

// handleRegisterPlan implements system-register_plan: stores an approved plan
// on the owning run for the end-of-turn completion check.
func handleRegisterPlan(ctx *HandlerCtx, name string) structs.ToolCallResult {
	plan, ok := ctx.args["plan"].(string)
	if !ok || strings.TrimSpace(plan) == "" {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "plan is required (non-empty string)"}}
	}
	if ctx.rt == nil {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "no owning runtime to register the plan against"}}
	}
	ctx.rt.RegisterPlan(plan)
	return structs.ToolCallResult{
		Success: true,
		Result:  toResultMap(map[string]any{"status": "success", "message": fmt.Sprintf("Plan registered (%d chars). A completion check will run at the end of this turn.", len(plan))}),
	}
}

// handleCheckPlan implements system-check_plan — an internal-only post-step
// injected by the agent's PlanCheck hook with a pre-built question in its
// arguments. It is not part of the model's tool set, so this branch exists
// purely to serve hook injections.
func handleCheckPlan(ctx *HandlerCtx, name string) structs.ToolCallResult {
	question, _ := ctx.args["question"].(string)
	if question == "" {
		return structs.ToolCallResult{Success: true, Result: toResultMap(map[string]any{"status": "success", "message": "No plan check pending."})}
	}
	qPtr := question
	return structs.ToolCallResult{
		Success:   true,
		Result:    toResultMap(map[string]any{"status": "success"}),
		RawResult: &qPtr,
	}
}

func init() {
	registerTool("system-register_plan", handleRegisterPlan)
	registerTool("system-check_plan", handleCheckPlan)
}
