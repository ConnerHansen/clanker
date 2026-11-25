package agent

import (
	"bufio"
	"bytes"
	"clankerv2/agent/structs"
	"clankerv2/agent/tools"
	"clankerv2/data"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// runTurnLoop orchestrates tool execution and recursive turns until the model
// produces a final response. The first payload comes from the caller (RunAgent);
// each subsequent one is rebuilt from rt's history. All state lives on rt, so
// multiple loops can run concurrently against different runtimes.
func runTurnLoop(ctx context.Context, url string, rt *AgentRuntime, channel chan any, engine AgentEngine, payload map[string]any) (string, error) {
	log.Printf("Running turn loop for model %s (run %s)", rt.ModelID, rt.ID)

	finalResponse := ""
	hasCalledTools := false
	turnToolCalls := make([]structs.IncomingToolCall, 0)

	turn := 0
	for {
		turn++
		log.Printf("Run %s: turn %d starting (payload messages=%d)", rt.ID, turn, payloadMessages(payload))
		response, toolCalls, streamUsage, err := StreamSSE(url, ctx, payload, channel, engine)
		if err != nil {
			log.Printf("Run %s: turn %d stream ended with error after %d tool call(s): %v", rt.ID, turn, len(toolCalls), err)
			return finalResponse, fmt.Errorf("stream error: %w", err)
		}
		if len(toolCalls) > 0 {
			log.Printf("Run %s: turn %d complete — response %d bytes, %d tool call(s) assembled", rt.ID, turn, len(response), len(toolCalls))
			if !hasCalledTools {
				log.Print("Tools are being called, running ExecuteBefore")
				Registry.ExecuteBefore()
			}
			hasCalledTools = true
			turnToolCalls = append(turnToolCalls, toolCalls...)

			respondState(channel, "executing")
			results := processToolCalls(rt, channel, toolCalls)

			// A compaction was registered in this batch: the history was wiped and
			// re-seeded with the summary row, so appending it would leave a dangling
			// assistant/tool pair — skip (manual path stops via stopRequested; auto
			// continues on the clean seed).
			registered := batchRegistersCompaction(toolCalls)
			if !registered {
				rt.AppendAgentMessage(rt.ModelID, response, results)
			}
			// The register turn's usage reflects the pre-compaction context; drop
			// it so the boundary check below cannot re-inject on stale pressure.
			if registered {
				streamUsage = nil
			}
			// system-subagent_finalize set stopRequested: return the finalized summary (or
			// this turn's text) as the final response and stop looping.
			if rt.stopRequested.Load() {
				finalResponse = response
				FlushPlanFor(rt)
				break
			}
		} else {
			rt.AppendAgentMessage(rt.ModelID, response, nil)
			finalResponse = response
		}

		// Drain any user messages that arrived during this turn and append them to history.
		queued := rt.DrainQueuedMessages()
		interjected := len(queued) > 0
		for _, msg := range queued {
			rt.AppendMessage("user", rt.ModelID, msg)
		}

		if compactInjection(rt, streamUsage) {
			// Injected under the bypass path (like hook injections): it must run regardless
			// of the runtime's granted tool set; the compact handler starts the compaction
			// and its raw result is what the model answers via system-register_compaction.
			channel <- data.Response{Type: "console", Message: fmt.Sprintf("Context usage at %d%% of the window; injecting context compaction...", rt.ContextUsagePercent(streamUsage))}
			log.Printf("Run %s: context at %d%% of window, auto-compacting", rt.ID, rt.ContextUsagePercent(streamUsage))
			injected := []structs.IncomingToolCall{{
				ID:        fmt.Sprintf("compact-%d", time.Now().UnixNano()),
				Type:      "function",
				Name:      "system-compact",
				Arguments: "{}",
			}}
			respondState(channel, "executing")
			hookResults := processInjectedToolCalls(rt, channel, injected)
			rt.AppendAgentMessage(rt.ModelID, "[System context compaction triggered, re-entering loop]", hookResults)

			payload = assembleRunToolResponsePayload(rt)
			continue
		}

		if len(toolCalls) == 0 {
			if hasCalledTools {
				log.Println("Tools were called, running ExecuteAfter")
				injected, err := Registry.ExecuteAfter(turnToolCalls, rt)
				if err != nil {
					return finalResponse, fmt.Errorf("post-turn hook error: %w", err)
				}

				if len(injected) > 0 {
					channel <- data.Response{Type: "console", Message: fmt.Sprintf("Hooks injected %d corrective tools; executing and re-entering loop...", len(injected))}
					log.Printf("Hooks injected %d corrective tools; executing and re-entering loop...", len(injected))
					turnToolCalls = append(turnToolCalls, injected...)

					// Hook injections are not model requests: they run under the bypass path so
					// internal post-step tools (system-check_plan, in no tool set) execute anyway.
					respondState(channel, "executing")
					hookResults := processInjectedToolCalls(rt, channel, injected)
					rt.AppendAgentMessage(rt.ModelID, "[System corrective action triggered, re-entering loop]", hookResults)

					payload = assembleRunToolResponsePayload(rt)
					continue
				} else {
					// No post-step corrections: done unless an interjection arrived. Retire the
					// active plan so it can't leak into future prompts (also clears plans left
					// behind by interrupted runs).
					FlushPlanFor(rt)
					log.Println("No post execution tools invoked")
				}
			}

			if !interjected {
				break
			}
		}

		payload = assembleRunToolResponsePayload(rt)
	}

	return finalResponse, nil
}

// processToolCalls executes a batch of MODEL-REQUESTED calls: the runtime is passed
// to the tool layer so per-run state stays with its conversation, and model calls
// are gated against its granted tool set.
func processToolCalls(rt *AgentRuntime, channel chan any, toolCalls []structs.IncomingToolCall) []structs.ToolCallResult {
	var results []structs.ToolCallResult
	for _, tc := range toolCalls {
		results = append(results, executeSingleToolCall(rt, channel, tc, tools.HandleToolCall, "model"))
	}
	return results
}

// batchRegistersCompaction reports whether any call is system-register_compaction;
// that handler wipes and re-seeds history, so the batch must not be appended after it.
func batchRegistersCompaction(toolCalls []structs.IncomingToolCall) bool {
	for _, tc := range toolCalls {
		if tc.Name == "system-register_compaction" {
			return true
		}
	}
	return false
}

// processInjectedToolCalls executes HOOK-INJECTED corrective calls without gating
// them against the granted tool set: internal post-step tools like system-check_plan
// exist only to be injected and are in no model's tool list.
func processInjectedToolCalls(rt *AgentRuntime, channel chan any, toolCalls []structs.IncomingToolCall) []structs.ToolCallResult {
	var results []structs.ToolCallResult
	for _, tc := range toolCalls {
		results = append(results, executeSingleToolCall(rt, channel, tc, tools.HandleInjectedToolCall, "injected"))
	}
	return results
}

// executeSingleToolCall runs one call through the given handler: parses args (malformed
// payloads reported as failures so the model sees them), executes it with a self-summary,
// and emits UI events. The origin parameter tags logs so stuck-session diagnosis can tell
// who drove each execution.
func executeSingleToolCall(rt *AgentRuntime, channel chan any, tc structs.IncomingToolCall, handler func(tools.ToolRuntime, chan any, string, map[string]any, tools.PermissionRequester, tools.AskUserRequester) structs.ToolCallResult, origin string) structs.ToolCallResult {
	args, err := parseToolArgs(tc)
	if err != nil {
		log.Printf("Warning: failed to parse tool call arguments for tool '%s': %v", tc.Name, tc.Arguments)
		// Report the dropped call so the model sees a failure instead of a silent skip
		// (skips desync the UI's pending list and hide malformed payloads).
		channel <- data.ToolStart{ID: tc.ID, Name: tc.Name}
		return structs.ToolCallResult{
			Success:  false,
			Result:   map[string]any{"error": fmt.Sprintf("invalid tool arguments: %v", err)},
			ToolCall: tc,
		}
	}

	channel <- data.ToolStart{ID: tc.ID, Name: tc.Name, Arguments: args}

	log.Printf("Tool call %q [%s] starting execution (id=%s)", tc.Name, origin, tc.ID)

	outcome := handler(rt, channel, tc.Name, args, RequestPermission, AskUser)
	outcome.ToolCall = tc

	resultStr, _ := json.Marshal(outcome.Result)
	log.Printf("Tool call %q [%s] finished: success=%v (%d bytes) | %s", tc.Name, origin, outcome.Success, len(resultStr), outcome.Summary)

	channel <- data.ToolCallSummary{
		ID:            tc.ID,
		Name:          tc.Name,
		Arguments:     args,
		Success:       outcome.Success,
		ResultPreview: outcome.Summary,
		ParamsPreview: tools.SummarizeParams(tc.Name, args),
	}

	return outcome
}

// parseToolArgs decodes a call's JSON args; empty payloads become an empty object so
// argument-less tools still get a valid map.
func parseToolArgs(tc structs.IncomingToolCall) (map[string]any, error) {
	args := make(map[string]any)
	if tc.Arguments == "" || strings.TrimSpace(tc.Arguments) == "" {
		return args, nil
	}
	if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
		return nil, err
	}
	return args, nil
}

// compactInjection reports whether reported usage crossed the auto-compaction threshold
// (4/5 of the window); no data, no trigger. Checked at the turn boundary — the flag
// resets on each registration, so a run that regrows past it may compact again.
func compactInjection(rt *AgentRuntime, usage *data.Usage) bool {
	if usage == nil || rt.ContextSize <= 0 {
		return false
	}
	return rt.ContextUsagePercent(usage) >= 80
}

// StreamSSE reads a model endpoint's SSE stream, returning its text response,
// accumulated tool calls, and the FINAL usage report (nil when none sent) — used
// for context-pressure checks.
func StreamSSE(url string, ctx context.Context, payload map[string]any, channel chan any, engine AgentEngine) (string, []structs.IncomingToolCall, *data.Usage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	log.Printf("POSTing to %s", url)
	respondState(channel, "posting")
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	cfg := GetAppConfig()
	if cfg != nil {
		modelCfg := cfg.GetModelByID(extractModelFromPayload(payload))
		if modelCfg != nil {
			headers := substituteHeaders(modelCfg.Headers)
			for k, v := range headers {
				req.Header.Set(k, v)
			}
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to perform HTTP request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 300 {
		respondState(channel, "ingest")
		streamResponse, toolCalls, streamUsage, err := readStream(resp, channel, engine)
		if err != nil {
			log.Println("Error during streaming?", err)
		}

		return streamResponse, toolCalls, streamUsage, err
	} else {
		respondState(channel, "error")
		respond(channel, "console", "Response: "+resp.Status)
	}

	errBody, _ := io.ReadAll(resp.Body)
	return "", nil, nil, fmt.Errorf("could not stream response, received status code %d with response %s", resp.StatusCode, string(errBody))
}

// extractModelFromPayload pulls the model ID out of a payload for header lookup.
func extractModelFromPayload(payload map[string]any) string {
	m, _ := payload["model"].(string)
	return m
}

func readStream(resp *http.Response, channel chan any, engine AgentEngine) (string, []structs.IncomingToolCall, *data.Usage, error) {
	scanner := bufio.NewScanner(resp.Body)
	// Large tool payloads (whole files, long diffs) exceed the default 64KB token limit;
	// a single overlong SSE line must not kill the stream mid-turn. Cap at 10MB.
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var message strings.Builder
	var thinkingMessage strings.Builder
	toolCalls := make([]structs.IncomingToolCall, 0)
	indexToToolCall := make(map[int]int)
	// lastState dedups repeated state emissions within one stream so the UI
	// gets a transition, not a flood (the run loop re-arms between streams).
	lastState := ""
	// finalUsage is the LAST usage report of the stream (endpoints send one per turn
	// with include_usage); it's what callers judge context pressure against.
	var finalUsage *data.Usage

	for scanner.Scan() {
		line := scanner.Text()
		result := engine.processLine(&line)
		if !result.success {
			continue
		}

		if result.usage != nil {
			channel <- *result.usage
			finalUsage = result.usage
		}

		if result.toolCallChunk != nil {
			tc := result.toolCallChunk
			// First chunk that names a function is the moment tool parsing begins:
			// the model has stopped prose and is now assembling calls. Emit once.
			if tc.Name != "" && lastState != "parsing" {
				respondState(channel, "parsing")
				lastState = "parsing"
			}
			pos, exists := indexToToolCall[tc.Index]
			if !exists {
				toolCalls = append(toolCalls, structs.IncomingToolCall{
					Index:     tc.Index,
					ID:        tc.ID,
					Type:      tc.Type,
					Name:      tc.Name,
					Arguments: tc.Arguments,
				})
				indexToToolCall[tc.Index] = len(toolCalls) - 1
			} else {
				entry := &toolCalls[pos]
				// Some gateways omit the tool_call index or reuse 0 for a parallel batch: a
				// delta naming a DIFFERENT function at an already-used index is a new call,
				// not more of the old one — without this guard it clobbers the previous entry.
				if tc.Name != "" && entry.Name != "" && tc.Name != entry.Name {
					toolCalls = append(toolCalls, structs.IncomingToolCall{
						Index:     tc.Index,
						ID:        tc.ID,
						Type:      tc.Type,
						Name:      tc.Name,
						Arguments: tc.Arguments,
					})
					indexToToolCall[tc.Index] = len(toolCalls) - 1
				} else {
					if tc.ID != "" {
						entry.ID = tc.ID
					}
					if tc.Type != "" {
						entry.Type = tc.Type
					}
					if tc.Name != "" {
						entry.Name = tc.Name
					}
					if tc.Arguments != "" {
						entry.Arguments += tc.Arguments
					}
				}
			}
		}

		if result.thinking != "" {
			content := result.thinking
			respond(channel, "debug", content)
			thinkingMessage.WriteString(content)
			// First thinking token of this stream: the model is in its reasoning phase.
			if lastState != "thinking" {
				respondState(channel, "thinking")
				lastState = "thinking"
			}
		}

		if result.response != "" {
			if thinkingMessage.Len() > 0 {
				respond(channel, "debug", "\n")
				thinkingMessage.Reset()
			}

			content := result.response
			respond(channel, "response", content)
			message.WriteString(content)
			// First non-thinking token: visible response output begins.
			if lastState != "active" {
				respondState(channel, "active")
				lastState = "active"
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return "", nil, finalUsage, err
	}

	// Per-call args are logged before deserialization: this is exactly what parseToolArgs receives.
	for i, tc := range toolCalls {
		log.Printf("SSE: assembled call[%d] name=%q id=%s argsLen=%d args=%q", i, tc.Name, tc.ID, len(tc.Arguments), truncateLog(tc.Arguments, 2048))
	}

	return message.String(), toolCalls, finalUsage, nil
}

func respond(channel chan any, t string, message string) {
	channel <- data.Response{Type: t, Message: message}
}

// respondState emits an explicit run-state transition (see data.StateMsg).
// The UI drives its status indicator from these rather than inferring state
// from the kinds of messages it receives.
func respondState(channel chan any, state string) {
	channel <- data.StateMsg{State: state}
}

// payloadMessages counts the messages array in a loop payload for diagnostics.
func payloadMessages(payload map[string]any) int {
	msgs, _ := payload["messages"].([]any)
	return len(msgs)
}
