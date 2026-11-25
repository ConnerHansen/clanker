package tools

import (
	"clankerv2/agent/structs"
	"clankerv2/data"
	"strings"
)

// CompactPrompt is the model-facing instruction delivered as system-compact's
// raw result — both for manual calls and the auto-injection fired when context
// usage crosses 4/5 of the window. It requires the summary to be returned via
// system-register_compaction, which is the only way a compaction completes.
const CompactPrompt = `You are running out of context. Please compact your current context so that we can launch a clean session. If you are working through a plan, please provide:

1. Your plan
2. The current step and what problem you are actively working through
3. What you did to complete the previous steps
4. Pertinent lessons learned or caveats to keep in mind
5. What files you've needed to read and approximate line ranges for the next session to pull in

If you are simply working through a discussion, provide:

1. A _detailed_ summary of the discussion
2. Capture decision points where you or the user arrived at some point of distinction
3. Pertinent lessons learned or caveats to keep in mind
4. Technologies discussed or algorithmic concepts being discussed
5. What files you've needed to read and approximate line ranges for the next session to pull in

When your compaction is ready, call system-register_compaction with 'summary' set to it. Do not take any other action until that call completes.`

// handleCompact implements system-compact: marks a compaction in-flight on the
// owning runtime and delivers the compaction prompt as this tool's raw result,
// so the model answers it via system-register_compaction. Fires manually (model
// call) or through the usage-threshold injection (injected path, auto kind).
func handleCompact(ctx *HandlerCtx, name string) structs.ToolCallResult {
	comp, ok := ctx.rt.(ContextCompactor)
	if !ok {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "this runtime does not support context compaction"}}
	}
	comp.BeginCompaction(ctx.injected)
	prompt := CompactPrompt
	return structs.ToolCallResult{
		Success:   true,
		RawResult: &prompt,
		Result:    toResultMap(map[string]any{"status": "success", "message": "Context compaction requested; call system-register_compaction with your summary when ready."}),
	}
}

// handleRegisterCompaction implements system-register_compaction: finalizes an
// in-flight compaction — the owning runtime wipes and re-seeds its history from
// the summary (persisting the seed row) and returns the continuation
// instruction (continue for auto-compact, stop for manual).
func handleRegisterCompaction(ctx *HandlerCtx, name string) structs.ToolCallResult {
	comp, ok := ctx.rt.(ContextCompactor)
	if !ok {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "this runtime does not support context compaction"}}
	}
	summary, _ := ctx.args["summary"].(string)
	if strings.TrimSpace(summary) == "" {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "'summary' is required (the full compaction text)"}}
	}
	instruction := comp.RegisterCompaction(strings.TrimSpace(summary))

	// Announce the seeded summary to the UIs so it renders mid-run as a user
	// message — identical to how the persisted seed row displays on session
	// reload (the register_compaction tool card alone would not surface the
	// summary text). The channel is always live in production; nil is only
	// passed by tests.
	if ctx.channel != nil {
		ctx.channel <- data.Response{Type: "compaction", Message: data.CompactionSeedPrefix + strings.TrimSpace(summary)}
	}

	return structs.ToolCallResult{
		Success:   true,
		RawResult: &instruction,
		Result:    toResultMap(map[string]any{"status": "success", "message": instruction}),
	}
}

func init() {
	registerTool("system-compact", handleCompact)
	registerTool("system-register_compaction", handleRegisterCompaction)
}
