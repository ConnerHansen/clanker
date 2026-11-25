package agent

import (
	"clankerv2/agent/structs"
	"clankerv2/data"
)

// engineResponse is returned by AgentEngine.processLine for every line in a stream.
type engineResponse struct {
	response      string
	success       bool
	thinking      string
	toolCallChunk *structs.IncomingToolCall // pointer to nil if not present
	usage         *data.Usage               // Pointer to usage metrics returned by API
}

// toolCallResult represents a completed tool call in a way that's easy to return from the engine.
type ToolCallResult structs.ToolCallResult
