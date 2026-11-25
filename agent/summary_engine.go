package agent

import (
	"clankerv2/agent/tools"
	"clankerv2/data"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// SummaryIdleInterval is how often the summary engine sweeps for stale
	// sessions; a session is summarized on the first sweep after it goes idle.
	SummaryIdleInterval = 10 * time.Minute

	// SummarySessionIdleWindow is how long a session must be quiet (no new
	// message) before it qualifies as stale; aligned with the sweep interval so
	// active conversations can't be summarized mid-activity.
	SummarySessionIdleWindow = 10 * time.Minute

	// SummarizerPromptPath is read as the summarizer's system prompt.
	SummarizerPromptPath = ".clanker/summarizer.md"
)

// summaryInputBudget bounds the user payload (previous summary + new
// activity) so a very long session can't overflow even a small model. Var
// (not const) so tests can shrink it.
var summaryInputBudget = 60000

const (
	summaryPrevBlockMax   = 8000
	summaryLineMax        = 1500 // per-message text truncation for the transcript
	summaryRequestTimeout = 5 * time.Minute
)

// fallbackSummarizerPrompt keeps the engine functional if the config file is
// missing or unreadable; the real instructions live in .clanker/summarizer.md.
const fallbackSummarizerPrompt = `You summarize an AI coding-agent conversation for long-term memory.
Write concise markdown prose (about 300-600 words) covering: the intent behind
the session, its date span, files read, files modified, corrections made along
the way (what was attempted, what worked, what failed), and which models were
used including any mid-session switches. No preamble, no meta commentary.`

// summarizingActive tracks an in-flight sweep so both statusbars (TUI and
// Wails) can show "summarizing..." while the summarizer model is loaded.
var summarizingActive atomic.Bool

// IsSummarizing reports whether a summary sweep is currently running.
func IsSummarizing() bool {
	return summarizingActive.Load()
}

// StartSummaryEngine launches the background sweep that incrementally regenerates
// summaries for stale sessions. Failures are logged only: a broken summarizer must
// never take the app down, and failed runs leave the row untouched for retry.
func StartSummaryEngine() {
	ticker := time.NewTicker(SummaryIdleInterval)
	go func() {
		for range ticker.C {
			runSummarySweep(context.Background())
		}
	}()
	log.Printf("Summary engine started (sweep every %s, sessions idle >= %s)", SummaryIdleInterval, SummarySessionIdleWindow)
}

// runSummarySweep summarizes every stale session in oldest-activity order. "Stale"
// means: no summary covering the latest message, last message older than the idle
// window (SQL gate), and no agent run live against it (long thinking stretches and
// pending system-ask forms write nothing to the DB, so recency alone misclassifies).
// The summarizer executor is torn down after every sweep, even on failure.
func runSummarySweep(ctx context.Context) {
	if !summarizingActive.CompareAndSwap(false, true) {
		return
	}
	defer summarizingActive.Store(false)
	// Slot stays alive for the whole sweep (model loads once), then releases so idle
	// time doesn't hold RAM. LIFO order means teardown runs before the flag reset.
	defer releaseExecutor("summarizer")

	stale, err := data.FindStaleSessions(SummarySessionIdleWindow)
	if err != nil {
		log.Printf("summary sweep: failed to find stale sessions: %v", err)
		return
	}
	if len(stale) == 0 {
		return
	}

	modelID := resolveSummarizerModel()
	if modelID == "" {
		log.Printf("summary sweep: %d stale session(s) but no summarizer configured (system.defaultSummarizer/defaultModel); retrying next sweep", len(stale))
		return
	}

	systemPrompt, err := os.ReadFile(SummarizerPromptPath)
	if err != nil || strings.TrimSpace(string(systemPrompt)) == "" {
		log.Printf("summary sweep: cannot read %s (%v); using built-in fallback prompt", SummarizerPromptPath, err)
		systemPrompt = []byte(fallbackSummarizerPrompt)
	}

	// Provision the summarizer executor ONCE for the sweep; the log-only channel swallows
	// boot/stream output so a background run never writes into the live UI stream.
	sweepRT := &AgentRuntime{
		ID:        "summary-sweep",
		Role:      RoleBackground,
		ModelID:   modelID,
		Stealth:   true, // one-shot: no DB session, no persistence
		Slot:      "summarizer",
		Isolation: true, // own a private executor; never join/tear the chat slot
	}
	logChan := make(chan any, 256)
	go func() {
		for msg := range logChan {
			if r, ok := msg.(data.Response); ok && r.Message != "" {
				log.Printf("[summary] %s", r.Message)
			}
		}
	}()

	url, headers, err := EnsureExecutorForRun(ctx, sweepRT, logChan)
	if err != nil {
		log.Printf("summary sweep: failed to provision summarizer executor: %v", err)
		return
	}
	defer ReleaseRunExecutors(sweepRT) // LIFO: runs before the slot release below

	engine := GetEngineForModel(modelID)

	for _, st := range stale {
		if IsSessionBusy(st.Session.ID) {
			log.Printf("summary sweep: session %d is active (mid-turn or waiting on user input), skipping until it settles", st.Session.ID)
			continue
		}
		messages, err := data.LoadMessagesAfter(st.Session.ID, st.PrevLastSummarizedMsgID)
		if err != nil {
			log.Printf("summary sweep: session %d failed to load messages: %v", st.Session.ID, err)
			continue
		}
		if len(messages) == 0 {
			continue
		}

		// Re-check right before summarizing: a batch can run for minutes and this
		// session may have gone live while earlier ones were processed.
		if IsSessionBusy(st.Session.ID) {
			log.Printf("summary sweep: session %d became active mid-sweep, skipping", st.Session.ID)
			continue
		}
		transcript := buildSummaryTranscript(st, messages)
		start := time.Now()
		content, err := summarizeWithModel(ctx, engine, url, headers, modelID, string(systemPrompt), transcript)
		if err != nil {
			log.Printf("summary sweep: session %d failed (will retry next sweep): %v", st.Session.ID, err)
			continue
		}

		if err := data.UpsertSummary(st.Session.ID, content, st.LastMessageID, modelID); err != nil {
			log.Printf("summary sweep: session %d generated but failed to save: %v", st.Session.ID, err)
			continue
		}
		log.Printf("summary sweep: session %d summarized (%s, %d new message(s), %s)", st.Session.ID, modelID, len(messages), time.Since(start).Round(time.Second))
	}
}

// resolveSummarizerModel picks the summarizing model: system.defaultSummarizer
// with fallback to the currently-selected chat model, then system.defaultModel.
func resolveSummarizerModel() string {
	cfg := GetAppConfig()
	if cfg == nil {
		return ""
	}
	if id := cfg.GetDefaultSummarizer(); id != "" {
		return id
	}
	// Reuse the interactive conversation's model when one is selected (its
	// executor may already be warm), else fall back to the configured default.
	if id := MainRuntime().ModelID; id != "" {
		return id
	}
	return cfg.GetDefaultModel()
}

// summarizeWithModel generates one summary through the same runTurnLoop path every
// other run uses (stealth, tool-less AgentRuntime), so engine selection stays
// config-driven with zero provider special-casing. The executor is shared across
// sessions; only per-session streaming happens here.
func summarizeWithModel(ctx context.Context, engine AgentEngine, url string, headers map[string]string, modelID, systemPrompt, userContent string) (string, error) {
	// Log-only channel: background runs must never write into the live UI stream.
	logChan := make(chan any, 64)
	go func() {
		for msg := range logChan {
			if r, ok := msg.(data.Response); ok && r.Message != "" {
				log.Printf("[summary] %s", r.Message)
			}
		}
	}()

	rt := &AgentRuntime{
		ID:           fmt.Sprintf("summary-%d", time.Now().UnixNano()),
		Role:         RoleBackground,
		ModelID:      modelID,
		Stealth:      true, // one-shot: no DB session, no persistence
		SystemPrompt: systemPrompt,
	}
	rt.Messages = append(rt.Messages, map[string]any{"role": "user", "content": userContent})

	reqCtx, cancel := context.WithTimeout(ctx, summaryRequestTimeout)
	defer cancel()

	payload := assembleRunToolResponsePayload(rt)
	content, err := runTurnLoop(reqCtx, url, rt, logChan, engine, payload)
	if err != nil {
		return "", fmt.Errorf("summarization stream: %w", err)
	}

	content = strings.TrimSpace(content)
	if content == "" {
		return "", fmt.Errorf("model returned an empty summary")
	}
	return content, nil
}

// buildSummaryTranscript assembles the summarization payload: the previous summary
// (to merge into) plus new activity since the last summarized message, rendered via
// stored one-line tool summaries. Over summaryInputBudget, the OLDEST lines are
// dropped first (their facts survive in the previous summary block).
func buildSummaryTranscript(st data.StaleSession, messages []data.Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Session ID: %d\nSession name: %s\n", st.Session.ID, st.Session.Name)
	if len(messages) > 0 {
		fmt.Fprintf(&b, "New activity covers messages %d..%d.\n", messages[0].ID, st.LastMessageID)
	}

	prev := strings.TrimSpace(st.PrevSummaryContent)
	if prev != "" {
		b.WriteString("\n## Previous summary (merge the new activity into it and keep it current):\n")
		b.WriteString(truncateSummaryText(prev, summaryPrevBlockMax))
	} else {
		b.WriteString("\nThis is the first summary for the session; summarize all of the activity below.\n")
	}

	lines := make([]string, 0, len(messages))
	for _, m := range messages {
		if line := renderMessageLine(m); line != "" {
			lines = append(lines, line)
		}
	}

	header := b.String() + "\n## New conversation activity (chronological):\n"
	total := len(header) + 2 // closing newline budget
	for _, l := range lines {
		total += len(l) + 1
	}
	drop := 0
	for total > summaryInputBudget && drop < len(lines)-1 {
		total -= len(lines[drop]) + 1
		drop++
	}

	b.Reset()
	b.WriteString(header)
	if drop > 0 {
		fmt.Fprintf(&b, "(%d older message(s) dropped to fit the context budget)\n", drop)
	}
	for _, l := range lines[drop:] {
		fmt.Fprintf(&b, "%s\n", l)
	}
	return b.String()
}

// renderMessageLine renders one stored message as a transcript line; tool rows use
// their stored self-summary (legacy rows are re-derived) and text is truncated.
func renderMessageLine(m data.Message) string {
	ts := m.InsertedAt.Format("2006-01-02 15:04")

	switch m.Source {
	case "user":
		if strings.TrimSpace(m.Contents) == "" {
			return ""
		}
		return fmt.Sprintf("[%s] User (%s): %s", ts, m.Model, truncateSummaryText(m.Contents, summaryLineMax))
	case "tool":
		line := StoredToolRowSummary(m)
		if line == "" {
			line = "tool call (unparsed result)"
		}
		return fmt.Sprintf("[%s] Tool: %s", ts, line)
	default: // assistant
		var parts []string
		text := strings.TrimSpace(m.Contents)
		if text != "" {
			parts = append(parts, "Assistant ("+m.Model+"): "+truncateSummaryText(text, summaryLineMax))
		}
		if len(m.ToolCalls) > 0 {
			parts = append(parts, fmt.Sprintf("(requested %d tool call(s))", len(m.ToolCalls)))
		}
		if len(parts) == 0 {
			return ""
		}
		return fmt.Sprintf("[%s] %s", ts, strings.Join(parts, " "))
	}
}

// storedToolRowFormat mirrors the JSON envelope persisted by agent.storedToolRow.
type storedToolRowFormat struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Success bool           `json:"success"`
	Params  map[string]any `json:"params"`
	Result  any            `json:"result"`
}

// StoredToolRowSummary returns the one-line self-summary for a stored tool row:
// messages.summary when present, otherwise re-derived from the persisted envelope
// so legacy rows pick up a real summary without a DB change. "" when unrecognized.
func StoredToolRowSummary(m data.Message) string {
	if m.Summary != nil && *m.Summary != "" {
		return *m.Summary
	}
	return backfillToolRowSummary(m.Contents)
}

func backfillToolRowSummary(contents string) string {
	var env storedToolRowFormat
	if err := json.Unmarshal([]byte(contents), &env); err != nil || env.Name == "" {
		return ""
	}

	var paramsJSON string
	if b, err := json.Marshal(env.Params); err == nil {
		paramsJSON = string(b)
	}
	var resultMap map[string]any
	if env.Result != nil {
		if b, err := json.Marshal(env.Result); err == nil {
			json.Unmarshal(b, &resultMap)
		}
	}
	return tools.SummarizeTool(data.CanonicalToolName(env.Name), paramsJSON, resultMap, env.Success)
}

func truncateSummaryText(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "… (truncated)"
}
