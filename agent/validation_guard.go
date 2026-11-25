package agent

import (
	"clankerv2/agent/structs"
	"fmt"
	"log"
	"math/rand"
	"slices"
)

// ValidationGuard ensures that any file modifications are followed by a build
// validation. The runtime parameter is carried for hook-uniformity and future
// per-run policy (e.g. skipping the guard for read-only subagents); current
// behavior applies to every run that performed writes.
func ValidationGuard(toolsCalled []structs.IncomingToolCall, rt *AgentRuntime) ([]structs.IncomingToolCall, error) {
	if len(toolsCalled) == 0 {
		return nil, nil
	}

	needsValidation := false

	// Scan tool calls in reverse order (newest to oldest)
searchLoop:
	for _, tool := range slices.Backward(toolsCalled) {
		log.Printf("Checking tool call %s", tool.Name)
		switch tool.Name {
		case "system-build", "system-tests":
			break searchLoop
		case "system-modify", "system-write":
			needsValidation = true
			break searchLoop
		}
	}

	if needsValidation {
		log.Println("ValidationGuard: Detected writes without subsequent build/test; injecting system-build")
		return []structs.IncomingToolCall{
			{
				Name:      "system-build",
				Arguments: "{}", // No args required for this tool
				ID:        fmt.Sprintf("hook-%d", rand.Int()),
				Type:      "function",
			},
			{
				Name:      "system-tests",
				Arguments: "{}", // No args required for this tool
				ID:        fmt.Sprintf("hook-%d", rand.Int()),
				Type:      "function",
			},
		}, nil
	} else {
		log.Println("ValidationGuard: No validation required")
	}

	return nil, nil
}

func init() {
	Registry.RegisterAfter(AfterToolTurnHook{Priority: 2, Fn: ValidationGuard})
}
