package tools

import (
	"clankerv2/agent/structs"
	"encoding/json"
)

// handleAsk implements system-ask: presents a set of questions in the UI and
// blocks until the user answers or cancels. The result envelope's "status"
// field distinguishes success, cancellation (not an execution failure — the
// user declined; the model can re-ask or proceed), and anything else as error.
func handleAsk(ctx *HandlerCtx, name string) structs.ToolCallResult {
	if ctx.askUser == nil {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "no UI is connected to answer questions"}}
	}
	rawQuestions, ok := ctx.args["questions"]
	if !ok {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "'questions' (array) is required"}}
	}
	questionArr, ok := rawQuestions.([]any)
	if !ok || len(questionArr) == 0 {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "'questions' must be a non-empty array"}}
	}

	// Blocks here until the UI resolves or cancels the form.
	resultStr := ctx.askUser(ctx.channel, questionArr)

	var envelope map[string]any
	if err := json.Unmarshal([]byte(resultStr), &envelope); err != nil {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "malformed user response", "raw": resultStr}}
	}
	status, _ := envelope["status"].(string)

	switch status {
	case "success":
		return structs.ToolCallResult{Success: true, Result: toResultMap(envelope)}
	case "cancelled":
		return structs.ToolCallResult{Success: true, Result: toResultMap(envelope)}
	default:
		return structs.ToolCallResult{Success: false, Result: toResultMap(envelope)}
	}
}

func init() { registerTool("system-ask", handleAsk) }
