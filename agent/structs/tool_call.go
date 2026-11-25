package structs

// IncomingToolCall represents a tool call from the LLM, either as a delta chunk or accumulated result.
type IncomingToolCall struct {
	Index     int    `json:"index"`
	ID        string `json:"id,omitempty"`
	Type      string `json:"type,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// ToolCallResult represents the result of executing a tool call.
type ToolCallResult struct {
	Result    map[string]any     `json:"result"`
	RawResult *string            `json:"raw_result,omitempty"`
	Success   bool               `json:"-"` // Success is used for internal logic/logging but not part of the JSON response to client if any.
	ToolCall  IncomingToolCall   `json:"-"`
	// Summary is the tool's self-description of this call: a single short
	// line naming what it did and how it went (e.g. "Read ./data/engine.go",
	// "Modified 3 files: 2 ok, 1 failed"). Populated by each tool via
	// tools.SummarizeTool so consumers (UI preview, session summarizer,
	// memory extraction) never need to re-parse raw results.
	Summary string `json:"-"`
}
