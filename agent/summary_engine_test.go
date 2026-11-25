package agent

import (
	"clankerv2/agent/tools"
	"clankerv2/data"
	"strings"
	"testing"
	"time"
)

func strPtr(s string) *string { return &s }

// syntheticStale builds a stale session with prev summary + fresh messages so
// buildSummaryTranscript can be exercised without a database.
func syntheticStale(prevContent string, prevLastID int64, msgs []data.Message) data.StaleSession {
	st := data.StaleSession{
		Session:                 data.Session{ID: 7, Name: "summaries feature"},
		PrevSummaryContent:      prevContent,
		PrevLastSummarizedMsgID: prevLastID,
	}
	if len(msgs) > 0 {
		st.LastMessageID = msgs[len(msgs)-1].ID
	}
	return st
}

func testMessages() []data.Message {
	ts := time.Date(2026, 7, 11, 9, 30, 0, 0, time.UTC)
	toolSum := "Read ./data/engine.go"
	return []data.Message{
		{ID: 10, InsertedAt: ts, Contents: "add a summaries feature", Model: "gemma-4-12b", Source: "user"},
		{ID: 11, InsertedAt: ts.Add(time.Minute), Contents: "I'll add a summaries table and engine.", Model: "qwen3.8-27b", Source: "assistant"},
		{ID: 12, InsertedAt: ts.Add(2 * time.Minute), Contents: `{"id":"t1","name":"system-read","success":true,"params":{"path":"./data/engine.go"},"result":"..."}`, Model: "qwen3.8-27b", Source: "tool", Summary: &toolSum},
		{ID: 13, InsertedAt: ts.Add(3 * time.Minute), Contents: "", Model: "qwen3.8-27b", Source: "assistant", ToolCalls: []data.ToolCall{{ID: "t1", Type: "function", Function: data.ToolCallFunction{Name: "system-read"}}}},
	}
}

func TestBuildTranscriptFirstSummary(t *testing.T) {
	st := syntheticStale("", 0, testMessages())
	out := buildSummaryTranscript(st, testMessages())

	if !strings.Contains(out, "Session ID: 7") || !strings.Contains(out, "summaries feature") {
		t.Errorf("missing session header:\n%s", out)
	}
	if !strings.Contains(out, "first summary for the session") {
		t.Errorf("should mark first summary:\n%s", out)
	}
	if !strings.Contains(out, "User (gemma-4-12b): add a summaries feature") {
		t.Errorf("missing user line with model:\n%s", out)
	}
	if !strings.Contains(out, "Tool: Read ./data/engine.go") {
		t.Errorf("tool row must use its stored summary:\n%s", out)
	}
	if strings.Contains(out, `{"id":"t1"`) {
		t.Errorf("raw tool envelope leaked into transcript:\n%s", out)
	}
	if !strings.Contains(out, "(requested 1 tool call(s))") {
		t.Errorf("assistant tool-request line missing:\n%s", out)
	}
}

func TestBuildTranscriptIncrementalKeepsPreviousSummary(t *testing.T) {
	msgs := testMessages()
	st := syntheticStale("Previously we designed the summaries table.", 5, msgs)
	out := buildSummaryTranscript(st, msgs)

	if !strings.Contains(out, "## Previous summary") || !strings.Contains(out, "Previously we designed the summaries table.") {
		t.Errorf("previous summary block missing:\n%s", out)
	}
	// Incremental: only messages after id 5 should appear; all test msgs are > 5.
	if !strings.Contains(out, "messages 10..13") {
		t.Errorf("expected incremental range header:\n%s", out)
	}
}

func TestBuildTranscriptDropsOldestWhenOverBudget(t *testing.T) {
	var msgs []data.Message
	for i := 0; i < 40; i++ {
		msgs = append(msgs, data.Message{
			ID: int64(i), InsertedAt: time.Now(),
			Contents: strings.Repeat("word ", 200), Model: "m", Source: "user",
		})
	}
	st := syntheticStale("", 0, msgs)

	old := summaryInputBudget
	summaryInputBudget = 3000 // force aggressive trimming for the test
	defer func() { summaryInputBudget = old }()

	out := buildSummaryTranscript(st, msgs)
	if !strings.Contains(out, "older message(s) dropped") {
		t.Fatalf("expected drop notice:\n%s", out)
	}
	if len(out) > summaryInputBudget+2000 { // budget + header/notice slack
		t.Errorf("transcript still far over budget: %d chars", len(out))
	}
	// Newest line must survive.
	if !strings.Contains(out, msgs[len(msgs)-1].Contents[:20]) {
		t.Errorf("newest message should be kept:\n%s", out)
	}
}

func TestBuildTranscriptTruncatesLongUserMessage(t *testing.T) {
	msgs := []data.Message{{
		ID: 1, InsertedAt: time.Now(),
		Contents: strings.Repeat("a b ", 1000), Model: "m", Source: "user",
	}}
	st := syntheticStale("", 0, msgs)
	out := buildSummaryTranscript(st, msgs)

	if !strings.Contains(out, "… (truncated)") {
		t.Errorf("long user message should be truncated:\n%s", out[:min(len(out), 400)])
	}
}

func TestBuildTranscriptWriteAndModifySummaries(t *testing.T) {
	// A realistic editing session: the model writes a new file, modifies two
	// (one failing), then reports back. The tool rows carry EXACTLY the
	// summaries tools.SummarizeTool produces at save time, so this asserts
	// what actually gets sent to the summarizer model.
	writeSum := tools.SummarizeTool("system-write", `{"path":"./agent/summary_engine.go"}`, map[string]any{
		"status": "success", "path": "./agent/summary_engine.go", "bytes_written": 11468,
	}, true)
	modSum := tools.SummarizeTool("system-modify", `{"changes":[{"file":"./data/engine.go","type":"replace","old":"a","new":"b"},{"file":"./data/sessions.go","type":"replace","old":"c","new":"d"}]}`, map[string]any{
		"status": "error",
		"results": []any{
			map[string]any{"file": "./data/engine.go", "successful": true},
			map[string]any{"file": "./data/sessions.go", "successful": false, "error": "'old' block not found (file has 311 lines)"},
		},
	}, false)

	ts := time.Date(2026, 7, 11, 9, 30, 0, 0, time.UTC)
	msgs := []data.Message{
		{ID: 1, InsertedAt: ts, Contents: "add a summary engine to the agent package", Model: "qwen3.8-27b", Source: "user"},
		{ID: 2, InsertedAt: ts.Add(time.Minute), Contents: "I'll create the engine first, then wire it into the schema.", Model: "qwen3.8-27b", Source: "assistant"},
		{ID: 3, InsertedAt: ts.Add(2 * time.Minute), Contents: `{"id":"t1","name":"system-write","success":true,"params":{"path":"./agent/summary_engine.go"},"result":"..."}`, Model: "qwen3.8-27b", Source: "tool", Summary: &writeSum},
		{ID: 4, InsertedAt: ts.Add(3 * time.Minute), Contents: ``, Model: "qwen3.8-27b", Source: "assistant",
			ToolCalls: []data.ToolCall{{ID: "t1", Type: "function", Function: data.ToolCallFunction{Name: "system-write"}}}},
		{ID: 5, InsertedAt: ts.Add(4 * time.Minute), Contents: `{"id":"t2","name":"system-modify","success":false,"params":{"changes":[]},"result":"..."}`, Model: "qwen3.8-27b", Source: "tool", Summary: &modSum},
		{ID: 6, InsertedAt: ts.Add(5 * time.Minute), Contents: `The sessions.go edit failed on an ambiguous anchor; retrying with more context.`, Model: "qwen3.8-27b", Source: "assistant"},
	}

	st := syntheticStale("", 0, msgs)
	out := buildSummaryTranscript(st, msgs)

	// Full payload, visible via `go test -v ./agent -run TestBuildTranscriptWriteAndModifySummaries`:
	t.Log(strings.Repeat("=", 72))
	t.Log("PAYLOAD SENT TO THE SUMMARIZER MODEL (user message):")
	t.Log(out)
	t.Log(strings.Repeat("=", 72))

	// The write row: file + bytes, no content.
	if want := "Tool: Wrote 11468 bytes to ./agent/summary_engine.go"; !strings.Contains(out, want) {
		t.Errorf("missing write summary line %q:\n%s", want, out)
	}
	// The modify row: every requested file plus which one failed.
	if want := "Tool: Failed 2 file(s): ./data/engine.go, ./data/sessions.go"; !strings.Contains(out, want) {
		t.Errorf("missing modify summary line %q:\n%s", want, out)
	}
	// Raw envelopes must never reach the model.
	for _, leak := range []string{`{"id":"t1"`, `"params"`, `"type":"replace"`} {
		if strings.Contains(out, leak) {
			t.Errorf("raw tool data leaked into transcript (%q):\n%s", leak, out)
		}
	}
}

func TestBuildTranscriptIncrementalWriteFollowUp(t *testing.T) {
	// Revived session: the previous summary already mentions the engine file;
	// only the follow-up modify (one ok, one failed) is new activity.
	prev := "Session added a summaries feature. Modified files: ./data/engine.go, ./agent/summary_engine.go. Models: qwen3.8-27b."
	modSum := tools.SummarizeTool("system-modify", `{"changes":[{"file":"./data/sessions.go","type":"replace","old":"x","new":"y"},{"file":"./app.go","type":"replace","old":"p","new":"q"}]}`, map[string]any{
		"status": "error",
		"results": []any{
			map[string]any{"file": "./data/sessions.go", "successful": false, "error": "'old' block not found"},
			map[string]any{"file": "./app.go", "successful": true},
		},
	}, false)

	ts := time.Date(2026, 7, 13, 14, 0, 0, 0, time.UTC) // two days later: multi-day session
	msgs := []data.Message{
		{ID: 50, InsertedAt: ts, Contents: "the modify failed, can you fix it?", Model: "qwen3.8-27b", Source: "user"},
		{ID: 51, InsertedAt: ts.Add(time.Minute), Contents: ``, Model: "qwen3.8-27b", Source: "assistant",
			ToolCalls: []data.ToolCall{{ID: "t9", Type: "function", Function: data.ToolCallFunction{Name: "system-modify"}}}},
		{ID: 52, InsertedAt: ts.Add(2 * time.Minute), Contents: `legacy envelope`, Model: "qwen3.8-27b", Source: "tool",
			Summary: strPtr(modSum)},
	}

	st := syntheticStale(prev, 40, msgs)
	out := buildSummaryTranscript(st, msgs)

	t.Log("--- incremental payload (previous summary + follow-up only) ---")
	t.Log(out)

	if !strings.Contains(out, "merge the new activity into it and keep it current") {
		t.Errorf("incremental instruction missing:\n%s", out)
	}
	if !strings.Contains(out, "Failed 2 file(s): ./data/sessions.go, ./app.go") {
		t.Errorf("follow-up modify summary missing:\n%s", out)
	}
	// Only messages after id 40 are sent.
	if !strings.Contains(out, "messages 50..52") {
		t.Errorf("expected incremental range header:\n%s", out)
	}
}

func TestStoredToolRowSummaryLegacyFallback(t *testing.T) {
	env := `{"id":"t1","name":"system-read","success":true,"params":{"path":"./data/engine.go"},"result":"big blob"}`
	m := data.Message{ID: 1, InsertedAt: time.Now(), Contents: env, Model: "m", Source: "tool"} // no Summary set

	// Legacy rows (predating messages.summary) are backfilled with the real
	// tools.SummarizeTool — same code path production uses at save time.
	if got := StoredToolRowSummary(m); got != "Read ./data/engine.go" {
		t.Errorf("legacy re-derivation got %q", got)
	}

	m.Summary = strPtr("stored summary wins")
	if got := StoredToolRowSummary(m); got != "stored summary wins" {
		t.Errorf("stored summary should take precedence: %q", got)
	}

	// Unparseable contents yield no summary (callers choose their own display).
	m2 := data.Message{ID: 2, InsertedAt: time.Now(), Contents: "not a json row", Source: "tool"}
	if got := StoredToolRowSummary(m2); got != "" {
		t.Errorf("unparseable row got %q, want empty", got)
	}
}

func TestResolveSummarizerModelPrecedence(t *testing.T) {
	prev := config.appConfig
	defer func() { config.appConfig = prev }()

	config.appConfig = &data.AppConfig{System: &data.SystemSettings{DefaultModel: "dm", DefaultSummarizer: "ds"}}
	if got := resolveSummarizerModel(); got != "ds" {
		t.Errorf("summarizer should win: %q", got)
	}

	config.appConfig = &data.AppConfig{System: &data.SystemSettings{DefaultModel: "dm"}}
	if got := resolveSummarizerModel(); got != "dm" {
		t.Errorf("fallback to default model: %q", got)
	}

	config.appConfig = &data.AppConfig{}
	if got := resolveSummarizerModel(); got != "" {
		t.Errorf("empty when unset: %q", got)
	}
}
