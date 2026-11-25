package tools

import (
	"clankerv2/agent/structs"
	"encoding/json"
	"fmt"
)

// HandleToolCall executes a tool call requested by the MODEL and attaches the
// tool's self-summary (SummarizeTool) so every consumer — UI previews, session
// summarizer, memory extraction — gets the same one-line description without
// parsing raw results. Model calls are checked against the runtime's granted
// tool set first: a filtered agent (subagent) is denied tools it was never
// given.
func HandleToolCall(rt ToolRuntime, channel chan any, name string, args map[string]any, requestPermission PermissionRequester, askUser AskUserRequester) structs.ToolCallResult {
	if filter, ok := rt.(ToolFilterer); ok && !filter.HasTool(name) {
		return structs.ToolCallResult{
			Success: false,
			Result:  map[string]any{"error": fmt.Sprintf("tool %q is not available to this agent (restricted tool set)", name)},
		}
	}

	res := handleToolCallImpl(rt, channel, name, args, requestPermission, askUser, false)
	if res.Summary == "" {
		paramsJSON, err := json.Marshal(args)
		if err != nil {
			paramsJSON = []byte("{}")
		}
		res.Summary = SummarizeTool(name, string(paramsJSON), res.Result, res.Success)
	}
	return res
}

// HandleInjectedToolCall executes a tool call INJECTED BY A HOOK (the agent's
// after-turn post-steps: system-check_plan, system-build, system-tests) with
// the same self-summary attachment as HandleToolCall — but WITHOUT the granted
// tool-set gate and WITHOUT requestPermission gating. Internal post-step tools
// are deliberately in no model's tool set, so gating an injection against it
// would deny our own calls; these run on the runtime's behalf under agent
// control, so a flagged command tool (system-build/tests with
// requestPermission) prompts only when the MODEL calls it, never here.
func HandleInjectedToolCall(rt ToolRuntime, channel chan any, name string, args map[string]any, requestPermission PermissionRequester, askUser AskUserRequester) structs.ToolCallResult {
	res := handleToolCallImpl(rt, channel, name, args, requestPermission, askUser, true)
	if res.Summary == "" {
		paramsJSON, err := json.Marshal(args)
		if err != nil {
			paramsJSON = []byte("{}")
		}
		res.Summary = SummarizeTool(name, string(paramsJSON), res.Result, res.Success)
	}
	return res
}

// handleToolCallImpl executes one tool call for the owning runtime. The
// runtime is passed to the tool layer so per-run context (session plans,
// subagent lifecycle) stays with its conversation instead of a package global.
// It performs NO tool-availability gating: model calls are gated by
// HandleToolCall, and hook injections bypass the gate on purpose. injected
// marks a hook-injected call: command-backed tools skip requestPermission
// gating for those (see HandleInjectedToolCall).
//
// Dispatch is registry-based: built-in tools register handlers from their own
// file (tool_*.go); anything unregistered is treated as a command-backed tool
// and resolved against the configured run block (command_exec.go).
func handleToolCallImpl(rt ToolRuntime, channel chan any, name string, args map[string]any, requestPermission PermissionRequester, askUser AskUserRequester, injected bool) structs.ToolCallResult {
	ctx := &HandlerCtx{
		rt:                rt,
		channel:           channel,
		args:              args,
		requestPermission: requestPermission,
		askUser:           askUser,
		injected:          injected,
	}

	if h, ok := lookupToolHandler(name); ok {
		return h(ctx, name)
	}
	return runCommandArgs(ctx, name)
}
