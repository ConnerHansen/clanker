package tools

import "clankerv2/data"

// PermissionRequester handles a user permission request during tool execution.
// prompt is the human-readable panel header (e.g. "Delete file: x.go");
// level/coloration derives from action. viewport is the optional payload for
// the UI's content pane (a diff or an assembled command); nil means no content
// pane — the UI prompts without opening its alt viewport.
type PermissionRequester func(channel chan any, tool, path, action, prompt string, viewport *data.PermissionViewport) string

// AskUserRequester backs system-ask: present questions and block until answered or
// cancelled. Implemented by the agent package (tools can't import it without a cycle).
type AskUserRequester func(channel chan any, questions []any) string

// ToolRuntime is the subset of the OWNING runtime tool execution needs: its run ID,
// model (for subagent inheritance), and plan recording. Injected per call so
// concurrent conversations each act on their own state instead of a shared global.
type ToolRuntime interface {
	// RunID identifies the owning run for logging (e.g. "main", "sub-3").
	RunID() string
	// OwnerModel returns this run's model, used to resolve subagent model inheritance.
	OwnerModel() string
	// RegisterPlan stores an execution plan on the owning run for the plan-check hook.
	RegisterPlan(plan string)
}

// SubagentFinalizer is implemented by subagent runtimes; a narrow interface type-asserted
// only when system-subagent_finalize fires, so non-subagent runtimes don't implement it.
type SubagentFinalizer interface {
	// Finalize records this run's deliverable and stops its turn loop.
	Finalize(summary string)
}

// ContextCompactor is implemented by runtimes that support compaction; a narrow
// interface type-asserted only when the system-compact / register_compaction tools fire.
// Begin marks it in-flight (auto = usage-threshold injection, not a model call);
// Register wipes and re-seeds history, returning the continue/stop instruction.
type ContextCompactor interface {
	BeginCompaction(auto bool)
	RegisterCompaction(summary string) string
}

// SubagentBackend is the subagent lifecycle injected by the agent package (tools can't
// import it without a cycle), backing the spawn/await/status/kill tools. Nil where subagents are unavailable.
type SubagentBackend interface {
	Spawn(callerID, callerModel string, args map[string]any) (string, error)
	Await(id string, timeoutSeconds int) map[string]any
	List(callerID string) []map[string]any
	Kill(id string) bool
}

// subagentBackend is set by the agent package at init; tests may replace it.
var subagentBackend SubagentBackend

// SetSubagentBackend wires (or clears, with nil) the subagent lifecycle into
// the tool layer.
func SetSubagentBackend(b SubagentBackend) { subagentBackend = b }

// CommandSource resolves a command-style tool's configured run block by name
// (system-build, system-tests), injected at config load; nil in tests, where
// handlers fall back to built-in defaults.
type CommandSource func(toolName string) *data.ToolRun

// commandSource is set via SetCommandSource; tests may replace or clear it.
var commandSource CommandSource

// SetCommandSource wires (or clears, with nil) the config-backed command
// lookup into the tool layer.
func SetCommandSource(fn CommandSource) { commandSource = fn }

// permissionGated reports whether a tool's config flagged requestPermission, so its
// executions pass a user approval first. Injected at config load; nil gates nothing.
var permissionGated func(toolName string) bool

// SetPermissionGated wires (or clears, with nil) the requestPermission lookup
// into the tool layer.
func SetPermissionGated(fn func(string) bool) { permissionGated = fn }

// readNumbered switches system-read between the numbered reader (default) and the
// legacy plain-content reader; wired from the config's "system.read" block at boot.
var readNumbered = true

// SetReadNumbered toggles numbered output for system-read.
func SetReadNumbered(b bool) { readNumbered = b }

// SkillSource returns the loaded skill set; injected at config load (tools can't reach
// the loader without a cycle), nil in tests where system-skills reports none.
type SkillSource func() []data.Skill

// skillSource is set via SetSkillSource; tests may replace or clear it.
var skillSource SkillSource

// SetSkillSource wires (or clears, with nil) the loaded-skill lookup into the
// tool layer.
func SetSkillSource(fn SkillSource) { skillSource = fn }

// ToolFilterer is implemented by runtimes with an EXPLICITLY filtered tool set
// (subagents): any MODEL call to an ungranted tool is denied before execution.
// Nil Tools (main runtime) means "everything configured" and never gates. Hook
// injections do NOT consult this gate.
type ToolFilterer interface {
	HasTool(name string) bool
}
