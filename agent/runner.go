package agent

import (
	"clankerv2/data"
	"context"
	"fmt"
	"log"
	"strings"
)

// executorAcquisition is one unit of ownership over an executor slot, as
// acquired by a single run. The same model can be live in multiple slots at
// once (e.g. the chat slot and a subagent's private slot), so ownership is
// tracked per (slot, owner): each run that joined or started an executor gets
// its own handle naming exactly which slot it must release later.
type executorAcquisition struct {
	Slot  string
	Owner string // run ID (AgentRuntime.ID)
}

// EnsureExecutorForRun provisions the executor for this run's model through
// the refcounted pool. The same model can be live in multiple slots at once
// (e.g. the chat slot and a subagent's private slot), so ownership is tracked
// per (slot, owner): each run that started or joined an executor gets its own
// acquisition naming exactly which slot it must release later. A run whose
// model is already live ANYWHERE joins the first live instance — inheriting a
// model implies sharing its executor, never starting a duplicate server.
//
// ctx ties the (possibly multi-minute) boot to this run: cancelling it kills
// any process/container launched for the run and unwinds every pool entry,
// so an escaped run can never leave a zombie boot holding RAM/GPU. Cancellation
// while WAITING behind another run's in-flight launch is a clean no-op (that
// launch belongs to its starter, not this run).
func EnsureExecutorForRun(ctx context.Context, rt *AgentRuntime, channel chan any) (string, map[string]string, error) {
	slot := rt.Slot
	if slot == "" {
		slot = "chat"
	}
	owner := rt.ID
	if owner == "" {
		owner = "anonymous"
	}

	url, headers, joinedSlot, err := ensureExecutor(ctx, modelIDForRun(rt), slot, owner, rt.Isolation, rt.Role == RoleMain, channel)
	if err != nil {
		return "", nil, err
	}
	// The interactive (main) conversation's executor is persistent: finishing a
	// chat turn releases the handle but keeps the server warm for the next
	// prompt (the executor carries the Persistent mark, see
	// releaseExecutorOwnership). Subagent and background runs keep the
	// teardown-at-zero behavior.
	rt.ExecutorHandle = &executorAcquisition{Slot: joinedSlot, Owner: owner}
	log.Printf("Run %s (model %s) using executor slot %q", rt.ID, modelIDForRun(rt), joinedSlot)
	return url, headers, nil
}

// ReleaseRunExecutors returns any executor ownership this run acquired (see
// EnsureExecutorForRun) back to the pool. RunAgent calls it automatically on
// completion; manual callers (tests, forced teardowns) may call it directly.
// Idempotent: the handle is cleared after the first release.
func ReleaseRunExecutors(rt *AgentRuntime) {
	handle := rt.ExecutorHandle
	if handle == nil {
		return
	}
	rt.ExecutorHandle = nil
	releaseExecutorOwnership(handle)
}

// modelIDForRun resolves the run's model, falling back to the app default so
// a runtime launched without an explicit model still runs. Subagent launch
// code is expected to resolve "inherit" explicitly before calling RunAgent;
// this fallback only covers misconfiguration.
func modelIDForRun(rt *AgentRuntime) string {
	if rt.ModelID != "" {
		return rt.ModelID
	}
	if cfg := GetAppConfig(); cfg != nil {
		id := cfg.GetDefaultModel()
		log.Printf("Run %s has no model set; falling back to default model %q", rt.ID, id)
		return id
	}
	return rt.ModelID
}

// assembleRunPayload builds the initial model payload for an interactive run:
// it infers/creates the session for the prompt and appends the user message
// to the runtime's history (persisting per stealth). Subagents and one-shots
// seed their messages directly and skip this.
func assembleRunPayload(rt *AgentRuntime, prompt string) map[string]any {
	rt.InferSession(prompt)
	rt.AppendMessage("user", rt.ModelID, prompt)
	return assembleRunToolResponsePayload(rt)
}

// assembleRunToolResponsePayload builds the payload for a model call from the
// runtime's current state. The system block comes from rt.SystemPrompt when
// set (one-shot / subagent instructions), otherwise from the attached
// PromptContext (file layer + its prompts, appended). When neither is set,
// it falls back to the effective system prompt file directly (local .clanker/
// system.md, else global) so interactive runs never go out bare. Nothing is
// persisted here: persistence and display-facing "tool" summaries happen as
// each tool call completes.
func assembleRunToolResponsePayload(rt *AgentRuntime) map[string]any {
	var system string
	switch {
	case rt.SystemPrompt != "":
		system = rt.SystemPrompt
	case rt.PromptContext != nil:
		// Reload prompt each time to get file changes
		system = rt.PromptContext.FullPrompt()
	default:
		system = data.SystemPromptContent()
	}

	// Append the runtime environment block (cwd, git branch, session id/start)
	// so every kind of run — interactive, subagent, one-shot — knows where on
	// the system it is and which session it belongs to.
	if env := environmentContext(rt); env != "" {
		if system == "" {
			system = env
		} else {
			system = system + "\n\n" + env
		}
	}

	messagesToSend := rt.Messages
	if system != "" {
		messagesToSend = append([]map[string]any{{"role": "system", "content": system}}, messagesToSend...)
	}

	payload := map[string]any{
		"model":          modelIDForRun(rt),
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
		"messages":       messagesToSend,
		"tools":          rt.Tools,
	}

	// Reasoning effort is model-driven: only present when the model has a
	// reasoning block (see resolveReasoningEffort); omitted otherwise so the
	// provider falls back to its own default.
	if level, ok := resolveReasoningEffort(rt); ok {
		payload["reasoning_effort"] = level
	}

	return payload
}

// resolveReasoningEffort computes the reasoning level for this run and whether
// it should be sent. A model with no reasoning block returns ("", false) — the
// key is omitted entirely. A disabled block always forces "none" (thinking off).
// Otherwise the effective level is the run's choice, falling back to the
// model's default; a non-none level outside the model's options clamps to that
// default.
func resolveReasoningEffort(rt *AgentRuntime) (string, bool) {
	cfg := GetAppConfig()
	if cfg == nil {
		return "", false // no config: cannot know the model's reasoning block
	}
	mc := cfg.GetModelByID(modelIDForRun(rt))
	if mc == nil || mc.Reasoning == nil {
		return "", false // no reasoning block: provider default
	}
	return resolveReasoningForModel(mc, rt.ReasoningEffort)
}

// resolveReasoningForModel resolves the level for a specific model given a
// chosen (possibly empty) run value. A nil model or block returns ("", false);
// a disabled block forces "none"; otherwise the chosen value is used, falling
// back to the model default and clamping an unrecognized non-none value to it.
func resolveReasoningForModel(mc *data.ModelConfig, chosen string) (string, bool) {
	if mc == nil || mc.Reasoning == nil {
		return "", false
	}
	if !mc.Reasoning.Enabled {
		return "none", true // disabled: thinking forced off
	}

	eff := chosen
	if eff == "" {
		eff = mc.ReasoningDefaultLevel()
	}
	// Only non-none levels are validated against the options; "none" is always
	// inherited. An unrecognized level falls back to the model default.
	if eff != "none" && !data.ContainsString(mc.ReasoningSelectableLevels(), eff) {
		eff = mc.ReasoningDefaultLevel()
	}
	return eff, true
}

// environmentContext builds the runtime-environment block appended to every
// run's system prompt: the working directory, the git branch (when the lookup
// succeeds), and the session's identity. The session line carries the ID and
// start date so the model can locate its own log file
// (.clanker/logs/{id}.log); stealth runs have no persisted session, so the
// line is labeled instead.
func environmentContext(rt *AgentRuntime) string {
	lines := make([]string, 0, 4)
	if dir := data.WorkingDirectory(); dir != "" {
		lines = append(lines, "Working directory: "+dir)
	}
	if branch := data.GitBranch(); branch != "" {
		lines = append(lines, "Git branch: "+branch)
	}

	s := rt.Session
	switch {
	case s == nil || s.ID <= 0:
		// Stealth or not-yet-created session: no DB row, no log file.
		lines = append(lines, "Session: stealth (no persisted session)")
	default:
		lines = append(lines, fmt.Sprintf(
			"Session: %d (started %s), log file .clanker/logs/%d.log",
			s.ID, s.InsertedAt.Format("2006-01-02 15:04"), s.ID))
	}

	return "# Environment\n" + strings.Join(lines, "\n")
}
