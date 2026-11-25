package agent

import (
	"clankerv2/agent/structs"
	"clankerv2/data"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// AgentRuntime holds the per-run state of one agent execution: every field that
// used to live in process-global config (messages, session, prompt context,
// tool set, stealth flag) is per-conversation — two live runtimes must never
// observe each other's history or flags. Subagents get their own runtime; the
// interactive UI drives exactly one, the "main" runtime.
type AgentRuntime struct {
	ID            string // stable run ID: "main" for the interactive agent, unique per subagent
	Role          Role   // Main or Subagent
	ModelID       string // model selected for this run; "" until a prompt selects one
	Session       *data.Session
	Stealth       bool
	PromptContext *data.Context // interactive system context (main runtime)
	SystemPrompt  string        // explicit system instructions for one-shot/subagent runs; overrides PromptContext when set
	Messages      []map[string]any
	Tools         []data.Tool // effective tool set AFTER per-run filtering (subagents)
	ContextSize   int         // model context window (tokens) used for streaming payloads
	// ReasoningEffort is the chosen reasoning level for this run; "" means
	// inherit the current model's default (see resolveReasoningEffort).
	ReasoningEffort string

	// Slot is the executor map entry this run calls home: "chat" for main,
	// "summarizer" for background sweeps, unique per subagent so one
	// conversation's model can't be torn out from under another.
	Slot string
	// Isolation marks background runs that must own a PRIVATE executor rather
	// than cross-slot-join: the summarizer's post-sweep teardown must be able to
	// stop its server without touching a run still streaming from it; subagents
	// inheriting the chat model should share the warm server.
	Isolation bool
	// ExecutorHandle records what EnsureExecutorForRun acquired for this run;
	// nil means nothing was started (or already released).
	ExecutorHandle *executorAcquisition

	ParentID string // owner run ID; "" for the main runtime

	// stopRequested is set by Finalize so the turn loop exits after this call;
	// atomic to stay correct if dispatch ever becomes async.
	stopRequested atomic.Bool

	// compacting marks an in-flight compaction: system-compact fired and
	// system-register_compaction has not completed. compactAuto records which
	// path started it so registration can pick the continuation (auto continues,
	// manual stops and returns to the user). Same-goroutine access as stopRequested.
	compacting  atomic.Bool
	compactAuto atomic.Bool

	// queuedMu guards queuedMessages: user text submitted while a run is in
	// flight, drained by runTurnLoop at cycle boundaries so the model sees it
	// together with the latest tool results.
	queuedMu sync.Mutex
	// queuedMessages holds the pending text FIFO; drained in order.
	queuedMessages []string
}

// ContextUsagePercent reports stream usage as a percentage of this run's context
// window; 0 when no usage was reported, so callers treat it as "no data" and
// never trigger.
func (rt *AgentRuntime) ContextUsagePercent(usage *data.Usage) int {
	if usage == nil || rt.ContextSize <= 0 {
		return 0
	}
	return usage.TotalTokens * 100 / rt.ContextSize
}

// BeginCompaction marks a compaction in-flight; auto reports whether the
// usage-threshold injection (vs a model call) requested it, which registration
// uses to choose between continuing and stopping. Idempotent.
func (rt *AgentRuntime) BeginCompaction(auto bool) {
	rt.compacting.Store(true)
	rt.compactAuto.Store(auto)
	log.Printf("Run %s: context compaction started (auto=%v)", rt.ID, auto)
}

// RegisteringCompaction reports whether a compaction is in flight on this run.
func (rt *AgentRuntime) Compacting() bool {
	return rt.compacting.Load()
}

// CompactionSeedPrefix frames the single user message that re-seeds a compacted
// runtime's history (alias of data.CompactionSeedPrefix: the seed row is what
// reloads cut at, so the marker lives with the persistence layer).
const CompactionSeedPrefix = data.CompactionSeedPrefix

// RegisterCompaction finalizes an in-flight compaction: history is wiped and
// re-seeded with one user row carrying the summary — the sole surviving row,
// persisted so reloads rebuild from it (nothing may be appended after
// registration). Queued interjections are discarded as stale. Auto runs
// continue; manual ones stop (stopRequested) so flow returns to the user.
func (rt *AgentRuntime) RegisterCompaction(summary string) string {
	if !rt.compacting.Load() {
		return "No context compaction is in progress; call system-compact first."
	}
	auto := rt.compactAuto.Load()
	rt.compacting.Store(false)

	seed := CompactionSeedPrefix + summary
	rt.Messages = []map[string]any{{"role": "user", "content": seed}}
	if !rt.Stealth {
		data.SaveMessage(rt.Session, data.Message{
			Contents: seed,
			Model:    rt.ModelID,
			Source:   "user",
		})
	}

	// Interjections queued during the compacted context are stale: drop them so the next cycle is clean.
	rt.DiscardQueuedMessages()

	log.Printf("Run %s: context compacted (%d chars of summary, auto=%v)", rt.ID, len(summary), auto)
	if auto {
		return "Session has successfully compacted, continue"
	}
	rt.stopRequested.Store(true)
	return "Session has been compacted. The session history now starts from your summary; stop here and return to the user."
}

// EnqueueUserMessage queues user text submitted while the agent is running:
// persisted now, delivered later by the turn loop. False for empty text.
func (rt *AgentRuntime) EnqueueUserMessage(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}

	rt.queuedMu.Lock()
	defer rt.queuedMu.Unlock()
	rt.queuedMessages = append(rt.queuedMessages, text)
	log.Printf("Run %s: queued user message (%d pending): %q", rt.ID, len(rt.queuedMessages), truncateLog(text, 80))
	return true
}

// DrainQueuedMessages returns all pending user text in FIFO order and empties
// the queue; the turn loop calls it at cycle boundaries so an interjection lands
// between tool results.
func (rt *AgentRuntime) DrainQueuedMessages() []string {
	rt.queuedMu.Lock()
	defer rt.queuedMu.Unlock()
	if len(rt.queuedMessages) == 0 {
		return nil
	}

	out := make([]string, len(rt.queuedMessages))
	copy(out, rt.queuedMessages)
	rt.queuedMessages = nil
	return out
}

// PendingUserMessages reports how many queued user messages await injection;
// UIs poll it for the pending indicator.
func (rt *AgentRuntime) PendingUserMessages() int {
	rt.queuedMu.Lock()
	defer rt.queuedMu.Unlock()
	return len(rt.queuedMessages)
}

// DiscardQueuedMessages drops queued user text without delivering it: called
// at cancel/interrupt so held messages don't silently fire the next cycle on their own.
func (rt *AgentRuntime) DiscardQueuedMessages() {
	rt.queuedMu.Lock()
	defer rt.queuedMu.Unlock()
	rt.queuedMessages = nil
}

// truncateLog bounds a log line to max runes.
func truncateLog(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}

	return string(runes[:max]) + "…"
}

// Role distinguishes an interactive top-level agent from a subagent it
// launched.
type Role string

const (
	RoleMain       Role = "main"
	RoleSubagent   Role = "subagent"
	RoleBackground Role = "background" // non-interactive one-shots (e.g. the summary sweep)
)

// NewAgentRuntime returns a fresh in-memory-only runtime (no session, default
// context size, full configured tool set); callers fill Session/Stealth/
// PromptContext before first use.
func NewAgentRuntime() *AgentRuntime {
	return &AgentRuntime{
		ID:          "main",
		Role:        RoleMain,
		Messages:    make([]map[string]any, 0),
		Tools:       DefaultTools(),
		ContextSize: data.DefaultContextSize,
	}
}

// MainRuntime returns the interactive (user-facing) runtime, creating it on
// first access. Legacy package-level state mutators delegate to it so existing
// behavior is preserved while subagents run on their own runtimes.
func MainRuntime() *AgentRuntime {
	config.mu.Lock()
	defer config.mu.Unlock()
	if config.mainRuntime == nil {
		config.mainRuntime = NewAgentRuntime()
	}
	return config.mainRuntime
}

// SetMainRuntime replaces the main runtime (used by tests). Nil clears it, so
// the next MainRuntime() call starts fresh.
func SetMainRuntime(rt *AgentRuntime) {
	config.mu.Lock()
	defer config.mu.Unlock()
	config.mainRuntime = rt
}

// GetMainRuntimeInternal returns the current main runtime WITHOUT creating one
// (nil when unused). Test helpers use it to snapshot/restore state.
func GetMainRuntimeInternal() *AgentRuntime {
	config.mu.Lock()
	defer config.mu.Unlock()
	return config.mainRuntime
}

// DefaultTools returns the configured tool set as a copy, or nil when no app
// config is loaded. Subagent launch code filters this set per profile.
func DefaultTools() []data.Tool {
	cfg := GetAppConfig()
	if cfg == nil || len(cfg.Tools) == 0 {
		return nil
	}
	out := make([]data.Tool, len(cfg.Tools))
	copy(out, cfg.Tools)
	return out
}

// ActiveModelID reports the model currently selected by the user (the main
// runtime's last prompt). Empty before any prompt; subagent launch code uses
// it to implement "model: null inherits the calling agent's model".
func (rt *AgentRuntime) ActiveModelID() string {
	return MainRuntime().ModelID
}

// HasTool implements tools.ToolFilterer: an empty/nil Tools slice means "all
// configured" (main runtime); the gate only ever restricts filtered subagents.
func (rt *AgentRuntime) HasTool(name string) bool {
	if len(rt.Tools) == 0 {
		return true
	}
	for _, t := range rt.Tools {
		if t.Function.Name == name {
			return true
		}
	}
	return false
}

// RunID identifies this run for the tool layer, so every tool call executes
// against the OWNING conversation's state — a subagent's plan never lands on the main runtime and vice versa.
func (rt *AgentRuntime) RunID() string { return rt.ID }

// OwnerModel implements tools.ToolRuntime so spawn_subagent can resolve "inherit
// the calling agent's model" without a global lookup.
func (rt *AgentRuntime) OwnerModel() string { return rt.ModelID }

// Finalize implements tools.SubagentFinalizer: records this run's deliverable
// and stops the turn loop, so system-subagent_finalize both ends the subagent
// and becomes its output. No-op for non-subagent runs.
func (rt *AgentRuntime) Finalize(summary string) {
	if rt.Role == RoleSubagent {
		recordSubagentFinalize(rt.ID, summary)
		rt.stopRequested.Store(true)
	}
}

// RegisterPlan implements tools.ToolRuntime: store an agreed-upon execution
// plan on this run's session so the plan-check hook can verify it before the
// turn closes.
func (rt *AgentRuntime) RegisterPlan(plan string) { RegisterPlanFor(rt, plan) }

// InferSession creates or adopts a session for the run, respecting stealth mode
// (dummy session ID -1); leaving stealth discards the dummy so a real one is created.
func (rt *AgentRuntime) InferSession(prompt string) {
	substr := min(len(prompt), 120)
	nameStr := prompt[:substr]

	// Capture an out-of-band reasoning choice (set before the first prompt);
	// session adoption below resets it, and a freshly created session starts empty.
	preReasoning := rt.ReasoningEffort

	if rt.Stealth {
		if rt.Session == nil {
			rt.Session = &data.Session{
				ID:         -1,
				Name:       nameStr,
				InsertedAt: time.Now(),
			}
			log.Println("Stealth mode: using dummy session")
		}
		return
	}

	if rt.Session != nil && rt.Session.ID == -1 {
		log.Println("Stealth mode deactivated: creating new real session")
		rt.Session = nil
	}

	if rt.Session == nil {
		newSession, err := data.NewSession(nameStr)
		if err != nil {
			log.Println("Well shit", err)
		}

		rt.SetSession(newSession, make([]data.Message, 0))
	} else {
		if rt.Session.Name == "" {
			rt.Session.Name = nameStr
			session, err := data.UpdateSession(rt.Session)

			if err != nil {
				log.Println("Couldn't update session", err)
			}

			rt.SetSession(session, nil)
		}
	}

	if preReasoning != "" {
		// rt.ReasoningEffort was reset by SetSession above; re-apply the
		// out-of-band choice and persist against the real session if one exists.
		rt.ReasoningEffort = preReasoning
		if rt.Session != nil && rt.Session.ID > 0 {
			cfg := GetAppConfig()
			if cfg != nil {
				if mc := cfg.GetModelByID(rt.ModelID); mc != nil && mc.ReasoningIsConfigurable() {
					stored := ""
					if preReasoning != mc.ReasoningDefaultLevel() {
						stored = preReasoning
					}
					_ = data.SetReasoningEffort(rt.Session.ID, stored)
					rt.Session.ReasoningEffort = stored
				}
			}
		}
	}

	if rt.Session != nil && rt.Session.ID > 0 {
		sessionLog.OpenSessionFile(rt.Session.ID)
	}
}

// AppendMessage appends a message to the model context and persists it unless stealth.
func (rt *AgentRuntime) AppendMessage(source string, model string, content string) {
	if content != "" {
		if !rt.Stealth {
			data.SaveMessage(rt.Session, data.Message{
				Contents: content,
				Model:    model,
				Source:   source,
			})
		} else {
			log.Println("Stealth mode: skipping message save to DB")
		}

		rt.Messages = append(rt.Messages, map[string]any{"role": source, "content": content})
	} else {
		log.Println("Skipping empty prompt")
	}
}

// AppendAgentMessage appends an assistant message (optionally with tool calls)
// to the model context and persists it; each completed call goes through
// AppendToolCall, the single persistence point for calls.
func (rt *AgentRuntime) AppendAgentMessage(model string, content string, toolCalls []structs.ToolCallResult) {
	if len(toolCalls) == 0 {
		if content != "" {
			if !rt.Stealth {
				data.SaveMessage(rt.Session, data.Message{
					Contents: content,
					Model:    model,
					Source:   "assistant",
				})
			} else {
				log.Println("Stealth mode: skipping assistant message save to DB")
			}

			rt.Messages = append(rt.Messages, map[string]any{"role": "assistant", "content": content})
		} else {
			log.Println("Skipping empty prompt")
		}
	} else {
		toolCallsContent := make([]data.ToolCall, len(toolCalls))
		for i, output := range toolCalls {
			call := output.ToolCall
			toolCallsContent[i] = data.ToolCall{
				ID:   call.ID,
				Type: call.Type,
				Function: data.ToolCallFunction{
					Name:      call.Name,
					Arguments: call.Arguments,
				},
			}
		}

		if !rt.Stealth {
			data.SaveMessage(rt.Session, data.Message{
				Contents:  content,
				Model:     model,
				Source:    "assistant",
				ToolCalls: toolCallsContent,
			})
		} else {
			log.Println("Stealth mode: skipping assistant tool message save to DB")
		}

		rt.Messages = append(rt.Messages, map[string]any{"role": "assistant", "content": content, "tool_calls": toolCallsContent})
		for _, output := range toolCalls {
			resultStr := ""
			if output.Result != nil {
				b, err := json.Marshal(output.Result)
				if err == nil {
					resultStr = string(b)
				}
			}

			call := output.ToolCall
			rt.AppendToolCall(model, call.ID, call.Name, output.Success, output.Summary, call.Arguments, resultStr, output.RawResult)
		}
	}
}

// AppendToolCall persists a completed tool call (envelope row: name, success,
// params, raw result — the success field doubles as the format marker display
// code keys on) and appends it to the model context; the model sees only the
// raw result in OpenAI tool-message format.
func (rt *AgentRuntime) AppendToolCall(model string, toolCallID string, name string, success bool, summary string, paramsJSON string, result string, rawResult *string) {
	// Determine the content to use: prefer RawResult if provided
	finalContent := result
	if rawResult != nil {
		finalContent = *rawResult
	}

	var summaryPtr *string
	if summary != "" {
		summaryPtr = &summary
	}

	if !rt.Stealth {
		_, err := data.SaveMessage(rt.Session, data.Message{
			Contents: storedToolRow(toolCallID, name, success, paramsJSON, finalContent),
			Model:    model,
			Source:   "tool",
			Summary:  summaryPtr,
		})

		if err != nil {
			log.Println("Error inserting tool call", err)
		}
	} else {
		log.Println("Stealth mode: skipping tool call save to DB")
	}

	rt.Messages = append(rt.Messages, map[string]any{"role": "tool", "tool_call_id": toolCallID, "content": finalContent})
}

// storedToolRow wraps a completed tool call in a small JSON envelope for
// persistence. The boolean success field doubles as the format marker that
// display code uses to distinguish these rows from legacy content shapes.
func storedToolRow(id, name string, success bool, paramsJSON, result string) string {
	var params any
	if err := json.Unmarshal([]byte(paramsJSON), &params); err != nil || params == nil {
		params = paramsJSON // keep the raw string; display code degrades gracefully
	}

	envelope := map[string]any{
		"id":      id,
		"name":    name,
		"success": success,
		"params":  params,
		"result":  result,
	}
	b, err := json.Marshal(envelope)
	if err != nil {
		return result
	}
	return string(b)
}

// SetSession adopts a session and its history; nil session with no messages resets to fresh state, otherwise the loaded history replaces the in-memory transcript.
func (rt *AgentRuntime) SetSession(session *data.Session, sessionMessages []data.Message) {
	rt.Session = session

	// Adopt the session's context window; nil sessions fall back to the default so a fresh conversation never inherits another session's sizing.
	if session != nil && session.ContextSize > 0 {
		rt.ContextSize = session.ContextSize
	} else {
		rt.ContextSize = data.DefaultContextSize
	}

	// Adopt the session's reasoning level; empty (or no session) resets to model-default inheritance so a fresh conversation never inherits another session's choice.
	rt.ReasoningEffort = ""
	if session != nil && session.ReasoningEffort != "" {
		rt.ReasoningEffort = session.ReasoningEffort
	}

	// Rows before the compaction cut are display-only: compacted away from the model context.
	cut := data.CompactionCutIndex(sessionMessages)
	messages := make([]map[string]any, 0)
	for i, msg := range sessionMessages {
		if cut >= 0 && i < cut {
			continue
		}
		output := map[string]any{}
		switch msg.Source {
		case "user":
			output["role"] = "user"
		case "tool":
			output["role"] = "tool"
		default:
			output["role"] = "assistant"
		}

		output["content"] = msg.Contents
		messages = append(messages, output)
	}
	rt.Messages = messages
	log.Println("Loaded session:")
	log.Println(session)
}
