package data

import (
	"time"
)

type Response struct {
	Type    string
	Message string
	// AgentID/AgentName identify which run produced this event; empty means the
	// main (interactive) agent. Subagent relays stamp every forwarded event so a
	// UI can tell overlapping runs apart.
	AgentID   string `json:"agent_id,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
}

// ToolStart is emitted on the response channel with Type "tool_start" when a
// tool call begins execution. The UI renders these in its pending-tools panel.
type ToolStart struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
	AgentID   string         `json:"agent_id,omitempty"`
	AgentName string         `json:"agent_name,omitempty"`
}

// ToolCallSummary is emitted on the response channel with Type "tool" once a
// tool call has finished. It intentionally excludes the full result output so
// the UI can render a compact inline entry instead of potentially extensive
// output; ResultPreview carries a bounded one-line preview of the raw result
// for display only. The full result is still persisted to the model context
// and session database via AppendToolCall.
type ToolCallSummary struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Arguments     map[string]any `json:"arguments,omitempty"`
	Success       bool           `json:"success"`
	ResultPreview string         `json:"result_preview,omitempty"`
	// ParamsPreview carries a one-line identifying summary of the call's
	// parameters (agent/tools.SummarizeParams) for display: paths and counts,
	// never content. Empty for tools with no params.
	ParamsPreview string `json:"params_preview,omitempty"`
	AgentID       string `json:"agent_id,omitempty"`
	AgentName     string `json:"agent_name,omitempty"`
}

// PermissionViewport is the optional content payload a permission request can
// carry for display in the UI's alt viewport (TUI) or an expandable panel
// (web). Kind names what the content is so each UI picks its header and
// styling: "diff" for file-change previews, "script" for command-backed tools
// showing the assembled command. An absent/nil Viewport means the request has
// no display content — the UI must not open an alt viewport for it.
type PermissionViewport struct {
	Kind    string `json:"kind,omitempty"`  // "diff" | "script"
	Title   string `json:"title,omitempty"` // Tool-provided header; empty = UI derives one from Kind
	Content string `json:"content"`
}

// PermissionRequest is sent when a tool needs user approval before proceeding
type PermissionRequest struct {
	ID        string              `json:"id"`               // Unique ID for this request
	Tool      string              `json:"tool"`             // Name of the tool requesting permission
	Path      string              `json:"path"`             // File path being affected
	Action    string              `json:"action"`           // "create", "overwrite", "update", or "delete"
	Prompt    string              `json:"prompt,omitempty"` // Human-readable descriptor from the caller (e.g. "Delete file: x.go")
	Level     string              `json:"level"`            // "info" | "warning" | "danger" — drives the prompt's coloration
	Viewport  *PermissionViewport `json:"viewport,omitempty"`
	Timestamp time.Time           `json:"timestamp"`
	AgentID   string              `json:"agent_id,omitempty"`
	AgentName string              `json:"agent_name,omitempty"`
}

// AskOption is one selectable choice inside an AskUserQuestion.
type AskOption struct {
	Label string `json:"label"` // Text shown to the user
	Value string `json:"value"` // Value returned to the LLM
	Style string `json:"style,omitempty"`
}

// AskUserQuestion is a single question from the system-ask tool.
type AskUserQuestion struct {
	ID          string      `json:"id"`
	Type        string      `json:"type"` // "confirm", "select", "multi-select", "free-response"
	Prompt      string      `json:"prompt"`
	Context     string      `json:"context,omitempty"`
	Placeholder string      `json:"placeholder,omitempty"`
	Options     []AskOption `json:"options,omitempty"`
	// AllowFreeForm appends a free-text answer row to select / multi-select
	// questions. The model-facing argument defaults to TRUE (omitted = on); it
	// is only false when the caller explicitly disabled it.
	AllowFreeForm bool `json:"allow_free_form,omitempty"`
}

// AskUserRequest is emitted on the response channel by the agent when the
// system-ask tool fires. The UI renders the questions as a form and resolves
// the blocked agent goroutine with agent.ResolveAsk(id, answers) or cancels
// it with agent.CancelAsk(id).
type AskUserRequest struct {
	ID        string            `json:"id"`
	Questions []AskUserQuestion `json:"questions"`
	AgentID   string            `json:"agent_id,omitempty"`
	AgentName string            `json:"agent_name,omitempty"`
}

// Usage represents token usage metrics returned by OpenAI-compatible APIs at
// the end of a stream.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// StateMsg is emitted on the response channel to explicitly signal an agent run
// state transition. The UI consumes these to update its status indicator rather
// than inferring state from the type of message being sent. States: posting,
// ingest, error, thinking, active, parsing, executing, waiting, idle.
type StateMsg struct {
	State     string `json:"state"`
	AgentID   string `json:"agent_id,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
}

// SubagentEvent announces a lifecycle transition of a subagent (queued ->
// running -> completed/failed/killed/timed_out). Emitted on the Wails event
// channel "subagent-event"; LastActivity names what it was doing, so a death
// report can explain what stopped. SessionID is set for persistent-mode runs.
// Color/ThinkingColor are the per-subagent display colors assigned at spawn
// (9-F hex channels; thinking is the dimmed same-hue variant) so every UI can
// color the feed consistently.
type SubagentEvent struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	CallerID      string    `json:"caller_id,omitempty"`
	ModelID       string    `json:"model_id,omitempty"`
	Status        string    `json:"status"`
	Color         string    `json:"color,omitempty"`
	ThinkingColor string    `json:"thinking_color,omitempty"`
	LastActivity  string    `json:"last_activity,omitempty"`
	SessionID     int64     `json:"session_id,omitempty"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	EndedAt       time.Time `json:"ended_at,omitempty"`
}
