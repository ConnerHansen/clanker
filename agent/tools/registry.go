package tools

import "clankerv2/agent/structs"

// HandlerCtx bundles everything a built-in tool handler needs for one call:
// the owning runtime, the event channel (permission prompts and UI events),
// the raw JSON arguments, the two injected requesters, and the injected flag
// (hook-injected calls bypass some gates). It is handed to every registered
// handler so each tool file stays self-contained.
type HandlerCtx struct {
	rt                ToolRuntime
	channel           chan any
	args              map[string]any
	requestPermission PermissionRequester
	askUser           AskUserRequester
	injected          bool
}

// ToolHandler executes one invocation of a built-in (non command-backed) tool.
type ToolHandler func(ctx *HandlerCtx, name string) structs.ToolCallResult

// toolRegistry maps a tool name to its handler. Built-in tools register from
// their own file's init block; anything unregistered falls through to the
// command-backed executor (see command_exec.go).
var toolRegistry = map[string]ToolHandler{}

// registerTool adds a built-in handler for the given name.
func registerTool(name string, h ToolHandler) { toolRegistry[name] = h }

// lookupToolHandler finds the registered handler for a tool name.
func lookupToolHandler(name string) (ToolHandler, bool) {
	h, ok := toolRegistry[name]
	return h, ok
}
