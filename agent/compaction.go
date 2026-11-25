package agent

import (
	"clankerv2/agent/structs"
	"clankerv2/agent/tools"
	"clankerv2/data"
	"context"
	"fmt"
	"log"
	"time"
)

// RunManualCompaction drives a user-initiated context compaction on the MAIN
// runtime (the /compact command). It mirrors the model's MANUAL system-compact
// call: the tool runs through the model-call path (tools.HandleToolCall,
// injected=false) so it starts a manual compaction (auto=false), its RAW result
// — the tools.CompactPrompt text — is appended as the tool message, and a
// single turn loop then lets the model write the summary via
// system-register_compaction, which stops the run (manual path) and returns
// flow to the user.
//
// The seed call is executed directly (not through executeSingleToolCall), so no
// ToolStart/ToolCallSummary UI events fire for a synthetic call — only the real
// model response streams to the interface. It also runs BEFORE engine/executor
// provisioning: it is cheap and local, so the in-flight flag is set immediately
// (a second /compact refuses until it clears) and history carries the prompt
// even if the model turns out to be unavailable.
//
// It RETURNS IMMEDIATELY like RunDiscoverTask: all work runs on a background
// goroutine and streams through resp exactly like an interactive turn. The
// returned CancelFunc stops the compaction mid-flight.
func RunManualCompaction(resp chan any, model string) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer cancel()

		rt := MainRuntime()
		if rt.Compacting() {
			resp <- data.Response{Type: "console", Message: "/compact: a compaction is already in flight; it will finish on its own."}
			return
		}

		if model == "" {
			model = modelIDForRun(rt)
		}
		rt.ModelID = model

		var sessionID int64
		if rt.Session != nil {
			sessionID = rt.Session.ID
		}
		BeginSessionRun(sessionID)
		defer EndSessionRun(sessionID)

		res := seedManualCompactionCall(rt)
		rt.AppendAgentMessage(model, "", []structs.ToolCallResult{res})

		engine := GetEngineForModel(model)
		if engine == nil {
			resp <- data.Response{Type: "console", Message: fmt.Sprintf("/compact: no engine configured for model %q", model)}
			return
		}

		url, headers, err := EnsureExecutorForRun(ctx, rt, resp)
		if err != nil {
			log.Printf("Compaction run %s: executor error: %v", rt.ID, err)
			resp <- data.Response{Type: "console", Message: fmt.Sprintf("/compact: model unavailable: %v", err)}
			return
		}
		defer ReleaseRunExecutors(rt)

		payload := assembleRunToolResponsePayload(rt)
		// The terminal eof sentinel is what flips both UIs back to idle (Prompt
		// stays locked until an eol arrives); /compact bypasses PromptWithContext,
		// so it must emit one itself or the session hangs in "active".
		defer func() {
			resp <- data.Response{Type: "eof", Message: ""}
		}()
		if _, err := engine.Stream(ctx, rt, url, headers, payload, resp); err != nil {
			log.Printf("Compaction run %s: stream error: %v", rt.ID, err)
			resp <- data.Response{Type: "console", Message: fmt.Sprintf("/compact: %v", err)}
		}
	}()

	return cancel
}

// seedManualCompactionCall executes a manual system-compact against the owning
// runtime and returns its result so the caller can append it as the assistant's
// tool-call message (RawResult carries the compaction prompt). It goes through
// the model-call path for the auto=false behavior and its self-summary; if that
// path is unavailable (restricted tool set), it falls back to starting the
// manual compaction directly so /compact still works.
func seedManualCompactionCall(rt *AgentRuntime) structs.ToolCallResult {
	id := fmt.Sprintf("compact-%d", time.Now().UnixNano())
	call := structs.IncomingToolCall{
		ID:        id,
		Type:      "function",
		Name:      "system-compact",
		Arguments: "{}",
	}

	res := tools.HandleToolCall(rt, nil, "system-compact", map[string]any{}, nil, nil)
	if res.Success {
		res.ToolCall = call
		return res
	}

	// Model-call path refused (e.g. system-compact not in this runtime's granted
	// set): start the manual compaction directly and mirror the handler's result
	// so history stays consistent with a normal compact call.
	log.Printf("Compaction run %s: model-call path unavailable (%v); starting manually", rt.ID, res.Result)
	rt.BeginCompaction(false)
	prompt := tools.CompactPrompt
	return structs.ToolCallResult{
		Success:   true,
		RawResult: &prompt,
		Result:    map[string]any{"status": "success", "message": "Context compaction requested."},
		Summary:   tools.SummarizeTool("system-compact", "{}", map[string]any{"status": "success"}, true),
		ToolCall:  call,
	}
}
