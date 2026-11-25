package agent

import (
	"clankerv2/agent/tools"
	"clankerv2/data"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestBeginAndRegisterCompactionAuto locks the auto path: Begin(auto=true) then
// Register wipes the history, seeds the framed user message, clears the
// flag, and returns the continuation instruction (run keeps going).
func TestBeginAndRegisterCompactionAuto(t *testing.T) {
	rt := NewAgentRuntime()
	rt.Stealth = true // hermetic: no DB writes
	rt.ModelID = "m1"
	rt.AppendMessage("user", "m1", "old question")
	rt.AppendAgentMessage("m1", "old answer", nil)
	if len(rt.Messages) != 2 {
		t.Fatalf("expected 2 seed messages, got %d", len(rt.Messages))
	}

	rt.BeginCompaction(true)
	if !rt.Compacting() {
		t.Fatal("compaction flag not set after Begin")
	}

	out := rt.RegisterCompaction("1. plan\n2. state")
	if out != "Session has successfully compacted, continue" {
		t.Fatalf("auto instruction wrong: %q", out)
	}
	if rt.Compacting() {
		t.Fatal("flag must clear after registration (re-arms the threshold)")
	}
	if len(rt.Messages) != 1 {
		t.Fatalf("history must be wiped and re-seeded with one message, got %d", len(rt.Messages))
	}
	seed, _ := rt.Messages[0]["content"].(string)
	if seed != CompactionSeedPrefix+"1. plan\n2. state" {
		t.Fatalf("seed row wrong: %q", seed)
	}
	if rt.stopRequested.Load() {
		t.Fatal("auto compaction must NOT stop the run")
	}
}

// TestManualCompactionStopsRun locks the manual path: Begin(auto=false) then
// Register returns a stop instruction and sets stopRequested so the turn loop
// ends and flow returns to the user.
func TestManualCompactionStopsRun(t *testing.T) {
	rt := NewAgentRuntime()
	rt.Stealth = true
	rt.ModelID = "m1"
	rt.AppendMessage("user", "m1", "old")

	rt.BeginCompaction(false)
	out := rt.RegisterCompaction("summary text")
	if !rt.stopRequested.Load() {
		t.Fatal("manual compaction must set stopRequested (run ends at registration)")
	}
	if out == "Session has successfully compacted, continue" {
		t.Fatalf("manual instruction must not be the auto one: %q", out)
	}
}

// TestRegisterCompactionWithoutBegin locks the refusal path: registering with
// no in-flight compaction is a no-op (history untouched, flag stays clear).
func TestRegisterCompactionWithoutBegin(t *testing.T) {
	rt := NewAgentRuntime()
	rt.Stealth = true
	rt.AppendMessage("user", "m1", "keep me")

	out := rt.RegisterCompaction("should not apply")
	if len(rt.Messages) != 1 {
		t.Fatalf("history must be untouched on refusal, got %d message(s)", len(rt.Messages))
	}
	if c, _ := rt.Messages[0]["content"].(string); c != "keep me" {
		t.Fatalf("message content altered: %q", c)
	}
	if rt.Compacting() {
		t.Fatal("flag must stay clear on refusal")
	}
	t.Logf("refusal instruction: %q", out)
}

// TestContextUsagePercent locks the threshold arithmetic used by the auto-
// injection (4/5 = 80% of total_tokens against the window).
func TestContextUsagePercent(t *testing.T) {
	rt := NewAgentRuntime()
	rt.ContextSize = 256000
	if got := rt.ContextUsagePercent(&data.Usage{TotalTokens: 204800}); got != 80 {
		t.Fatalf("204800/256000 = %d%%, want 80", got)
	}
	if got := rt.ContextUsagePercent(nil); got != 0 {
		t.Fatalf("nil usage must report 0: %d", got)
	}
}

// TestCompactInjectionThreshold locks the boundary: below 4/5 no injection, at
// or above it one fires; missing usage never triggers.
func TestCompactInjectionThreshold(t *testing.T) {
	rt := NewAgentRuntime()
	rt.ContextSize = 1000

	if compactInjection(rt, &data.Usage{TotalTokens: 799}) {
		t.Fatal("below 80%% must not inject")
	}
	if !compactInjection(rt, &data.Usage{TotalTokens: 800}) {
		t.Fatal("at 80%% must inject")
	}
	if compactInjection(rt, nil) {
		t.Fatal("nil usage must not inject")
	}
}

// fakeCompactorRuntime implements tools.ContextCompactor with recording state
// so the handler's compaction cases can be exercised without a real runtime.
type fakeCompactorRuntime struct {
	begins     []bool
	registered string
	inFlight   bool
	fakeRuntimeNoFilter
}

func (f *fakeCompactorRuntime) BeginCompaction(auto bool) { f.begins = append(f.begins, auto) }
func (f *fakeCompactorRuntime) RegisterCompaction(summary string) string {
	f.registered = summary
	return "ok"
}

// TestHandlerCompactSetsFlag locks that system-compact starts a compaction on
// the owning runtime: model path is manual (auto=false), injected path is auto.
func TestHandlerCompactSetsFlag(t *testing.T) {
	manual := &fakeCompactorRuntime{}
	res := tools.HandleToolCall(manual, nil, "system-compact", map[string]any{}, nil, nil)
	if !res.Success {
		t.Fatalf("compact must succeed: %v", res.Result)
	}
	if len(manual.begins) != 1 || manual.begins[0] {
		t.Fatalf("model call must BeginCompaction(false): %+v", manual.begins)
	}
	if res.RawResult == nil || *res.RawResult == "" {
		t.Fatal("raw result must carry the compaction prompt (the model sees it as the tool message)")
	}

	auto := &fakeCompactorRuntime{}
	res = tools.HandleInjectedToolCall(auto, nil, "system-compact", map[string]any{}, nil, nil)
	if len(auto.begins) != 1 || !auto.begins[0] {
		t.Fatalf("injected call must BeginCompaction(true): %+v", auto.begins)
	}
}

// TestHandlerRegisterCompaction locks argument validation + the runtime handoff:
// an empty summary fails, a real one is registered and its instruction comes
// back as both raw result and message. A successful registration also emits a
// "compaction" UI event carrying the full seeded summary (the framed user
// message), so both interfaces can render it live.
func TestHandlerRegisterCompaction(t *testing.T) {
	rt := &fakeCompactorRuntime{}

	if res := tools.HandleToolCall(rt, nil, "system-register_compaction", map[string]any{"summary": "  "}, nil, nil); res.Success {
		t.Fatal("blank summary must be rejected")
	}

	ch := make(chan any, 4)
	res := tools.HandleToolCall(rt, ch, "system-register_compaction", map[string]any{"summary": "  my summary  "}, nil, nil)
	if !res.Success {
		t.Fatalf("register must succeed: %v", res.Result)
	}
	if rt.registered != "my summary" {
		t.Fatalf("summary must be trimmed before registration: %q", rt.registered)
	}
	if res.RawResult == nil || *res.RawResult != "ok" {
		t.Fatal("instruction must come back as the raw result (the model's tool message)")
	}

	// The seeded summary reaches the UI on its own event so it renders mid-run.
	select {
	case ev := <-ch:
		r, ok := ev.(data.Response)
		if !ok || r.Type != "compaction" {
			t.Fatalf("registration must emit a compaction UI event, got %#v", ev)
		}
		want := data.CompactionSeedPrefix + "my summary"
		if r.Message != want {
			t.Fatalf("compaction event must carry the full seeded summary, got %q", r.Message)
		}
	default:
		t.Fatal("a compaction UI event must be emitted on successful registration")
	}

	// A runtime without compactor support is refused cleanly.
	if res := tools.HandleToolCall(&fakeRuntimeNoFilter{}, nil, "system-register_compaction", map[string]any{"summary": "x"}, nil, nil); res.Success {
		t.Fatal("non-compacting runtime must be refused")
	}
}

// TestSetSessionCutsAtCompaction locks the reload rule: the model payload starts
// at the LATEST compaction seed user row (the framed summary); older rows are
// display-only. The register_compaction tool call is NOT persisted (the run
// loop skips appending it), so the seed prefix is the only cut marker.
func TestSetSessionCutsAtCompaction(t *testing.T) {
	mk := func(source, contents string, calls ...data.ToolCall) data.Message {
		return data.Message{Source: source, Contents: contents, Model: "m1", ToolCalls: calls}
	}

	msgs := []data.Message{
		mk("user", "ancient question"),
		mk("assistant", "ancient answer"),
		mk("tool", `{"name":"system-read"}`),
		mk("user", CompactionSeedPrefix+"the summary"),
		mk("user", "new question after compaction"),
	}

	rt := NewAgentRuntime()
	rt.SetSession(&data.Session{ID: 7, ContextSize: 262144}, msgs)

	if len(rt.Messages) != 2 {
		t.Fatalf("payload must start at the seed row (2 rows), got %d: %+v", len(rt.Messages), rt.Messages)
	}
	seed, _ := rt.Messages[0]["content"].(string)
	if seed != CompactionSeedPrefix+"the summary" {
		t.Fatalf("first payload message must be the seed row: %q", seed)
	}
	role, _ := rt.Messages[0]["role"].(string)
	if role != "user" {
		t.Fatalf("seed row role must be user, got %q", role)
	}

	// No compaction: full history is the payload.
	rt2 := NewAgentRuntime()
	rt2.SetSession(&data.Session{ID: 8}, msgs[:3])
	if len(rt2.Messages) != 3 {
		t.Fatalf("no-compaction session must load fully, got %d", len(rt2.Messages))
	}
}

// TestRunManualCompactionSeedsManualPath locks the /compact seeding contract:
// RunManualCompaction starts a MANUAL compaction (auto=false) — proven by a
// subsequent RegisterCompaction yielding the stop/return-to-user instruction,
// NOT the auto-continue one — and appends the system-compact call plus its
// CompactPrompt tool message to the model context. No engine is configured so
// the run returns right after seeding (no stream, no executor boot); the "no
// engine" console message doubles as the completion signal that makes reading
// rt.Messages race-free.
func TestRunManualCompactionSeedsManualPath(t *testing.T) {
	prevRT := GetMainRuntimeInternal()
	rt := NewAgentRuntime()
	rt.Stealth = true // hermetic: no DB writes
	rt.SystemPrompt = "test-system" // keeps the payload assembler off any real file
	rt.ModelID = "m1"
	SetMainRuntime(rt)
	defer func() {
		SetMainRuntime(prevRT)
		ReleaseRunExecutors(MainRuntime())
	}()

	prevConfig := config.appConfig
	config.appConfig = &data.AppConfig{} // no models -> GetEngineForModel nils after seeding
	defer func() { config.appConfig = prevConfig }()

	resp := make(chan any, 8)
	if cf := RunManualCompaction(resp, "m1"); cf == nil {
		t.Fatal("RunManualCompaction must return a non-nil cancel function")
	}

	// Wait for the run to finish seeding (it bails at the engine check with no
	// models configured). Receiving from resp is a happens-after sync point, so
	// by the time this returns all of rt.Messages is safely visible.
	select {
	case msg := <-resp:
		if r, ok := msg.(data.Response); !ok || r.Type != "console" {
			t.Fatalf("expected a console notice from the no-engine bail, got %#v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the /compact run to finish seeding")
	}

	if !rt.Compacting() {
		t.Fatal("manual compaction must be in flight after RunManualCompaction seeds")
	}

	// History must carry the seeded system-compact call + its CompactPrompt tool message.
	if len(rt.Messages) != 2 {
		t.Fatalf("expected assistant + tool seed messages, got %d: %+v", len(rt.Messages), rt.Messages)
	}
	assistant, _ := rt.Messages[0]["role"].(string)
	if assistant != "assistant" {
		t.Fatalf("first seed message must be assistant, got %q", assistant)
	}
	calls, ok := rt.Messages[0]["tool_calls"].([]data.ToolCall)
	if !ok || len(calls) != 1 || calls[0].Function.Name != "system-compact" {
		t.Fatalf("assistant seed must carry one system-compact call: %+v", rt.Messages[0]["tool_calls"])
	}
	toolMsg, _ := rt.Messages[1]["role"].(string)
	if toolMsg != "tool" {
		t.Fatalf("second seed message must be the tool result, got %q", toolMsg)
	}
	content, _ := rt.Messages[1]["content"].(string)
	if content != tools.CompactPrompt {
		t.Fatalf("tool message must be the CompactPrompt, got %q", content)
	}

	// The seed is a manual (auto=false) compaction: registration must return the
	// stop-and-return instruction, not the auto-continue one.
	out := rt.RegisterCompaction("summary text")
	if out == "Session has successfully compacted, continue" {
		t.Fatalf("/compact must take the MANUAL path (auto=false), got the auto-continue instruction: %q", out)
	}
	if !rt.stopRequested.Load() {
		t.Fatal("manual compaction registration must set stopRequested (run returns to user)")
	}
}

// TestRunManualCompactionRefusesInFlight locks the double-compaction guard:
// when a compaction is already in flight, RunManualCompaction refuses (a single
// console notice) and seeds nothing.
func TestRunManualCompactionRefusesInFlight(t *testing.T) {
	prevRT := GetMainRuntimeInternal()
	rt := NewAgentRuntime()
	rt.Stealth = true
	SetMainRuntime(rt)
	defer func() {
		SetMainRuntime(prevRT)
		ReleaseRunExecutors(MainRuntime())
	}()

	prevConfig := config.appConfig
	config.appConfig = &data.AppConfig{}
	defer func() { config.appConfig = prevConfig }()

	rt.BeginCompaction(false) // simulate an in-flight compaction
	before := len(rt.Messages)

	resp := make(chan any, 4)
	if cf := RunManualCompaction(resp, "m1"); cf == nil {
		t.Fatal("RunManualCompaction must return a non-nil cancel function")
	}

	select {
	case msg := <-resp:
		respMsg, ok := msg.(data.Response)
		if !ok || respMsg.Type != "console" {
			t.Fatalf("in-flight refusal must emit a console notice, got %#v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected an in-flight refusal notice on the channel")
	}

	if len(rt.Messages) != before {
		t.Fatalf("in-flight refusal must not seed history (was %d, now %d)", before, len(rt.Messages))
	}
}

// TestRunTurnLoopCompactionLeavesCleanHistory drives the REAL turn loop against
// a fake OpenAI-compatible SSE server that answers with a manual
// register_compaction call. It locks the post-compaction invariant: the run's
// history ends as EXACTLY the seed user row — the register batch must NOT be
// appended on top of the wiped+re-seeded history (a dangling assistant tool-
// calls row + stop-instruction tool row would poison the next model payload).
// It also locks the live-display contract: the run emits a "compaction" UI
// event carrying the seeded summary so both interfaces show it mid-run.
func TestRunTurnLoopCompactionLeavesCleanHistory(t *testing.T) {
	registerSSE := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"tc-1\",\"type\":\"function\",\"function\":{\"name\":\"system-register_compaction\",\"arguments\":\"{\\\"summary\\\": \\\"the compacted summary\\\"}\"}}]}}}\n\ndata: [DONE]\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, registerSSE)
	}))
	defer server.Close()

	setTestAppConfig(t, &data.AppConfig{Models: []data.ModelConfig{{
		ID: "m-compact", Executor: "llama.cpp", URL: server.URL, Managed: false,
	}}})

	rt := NewAgentRuntime()
	rt.Stealth = true // hermetic: no DB writes
	rt.ModelID = "m-compact"
	rt.SystemPrompt = "test-system" // keeps the payload assembler off any real file
	rt.AppendMessage("user", "m-compact", "old question")
	rt.BeginCompaction(false) // /compact started a manual compaction

	ch := make(chan any, 64)
	payload := assembleRunToolResponsePayload(rt)
	final, err := runTurnLoop(context.Background(), server.URL+"/v1/chat/completions", rt, ch, &LlamaCpp{}, payload)
	if err != nil {
		t.Fatalf("turn loop errored: %v", err)
	}
	// Manual registration stops the run with no final text.
	if final != "" {
		t.Fatalf("manual compaction must end without a final response, got %q", final)
	}
	if !rt.stopRequested.Load() {
		t.Fatal("manual compaction must set stopRequested (run ends at registration)")
	}

	// The history is exactly the re-seeded summary user row: nothing dangling.
	if len(rt.Messages) != 1 {
		t.Fatalf("history must be exactly the seed row, got %d: %+v", len(rt.Messages), rt.Messages)
	}
	role, _ := rt.Messages[0]["role"].(string)
	if role != "user" {
		t.Fatalf("seed row role must be user, got %q", role)
	}
	content, _ := rt.Messages[0]["content"].(string)
	if !strings.HasPrefix(content, CompactionSeedPrefix) {
		t.Fatalf("seed row must carry the framed summary, got %q", content)
	}

	// The seeded summary must have reached the channel as a "compaction" event
	// (live display in both UIs).
	sawCompaction := false
	drain:
	for {
		select {
		case msg := <-ch:
			r, ok := msg.(data.Response)
			if ok && r.Type == "compaction" {
				sawCompaction = true
				break drain
			}
		default:
			break drain
		}
	}
	if !sawCompaction {
		t.Fatal("the turn loop must emit the compaction UI event (seeded summary) on registration")
	}
}

// TestRunTurnLoopNoDoubleAutoCompact locks the auto-compaction re-fire guard:
// a turn that registers a compaction reports STALE usage (measured against
// the pre-compaction context), so the next boundary check must not re-inject.
func TestRunTurnLoopNoDoubleAutoCompact(t *testing.T) {
	registerSSE := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"arguments\":\"{\\\"summary\\\": \\\"the compacted summary\\\"}\",\"name\":\"system-register_compaction\"},\"id\":\"tc-reg\",\"index\":0,\"type\":\"function\"}]}}],\"usage\":{\"completion_tokens\":1,\"prompt_tokens\":1,\"total_tokens\":220}}\n\ndata: [DONE]\n\n"
	text := func(content string, total int) string {
		return fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":%q}}],\"usage\":{\"completion_tokens\":1,\"prompt_tokens\":1,\"total_tokens\":%d}}\n\ndata: [DONE]\n\n", content, total)
	}

	var n int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Content-Type", "text/event-stream")
		switch n {
		case 1:
			fmt.Fprint(w, text("working", 220)) // high usage, no tool calls
		case 2:
			// Register turn reports the SAME (stale, pre-compaction) total.
			fmt.Fprint(w, registerSSE)
		default:
			fmt.Fprint(w, text("done", 10)) // clean context, low usage
		}
	}))
	defer server.Close()

	setTestAppConfig(t, &data.AppConfig{Models: []data.ModelConfig{{
		ID: "m-double-compact", Executor: "llama.cpp", URL: server.URL, Managed: false,
	}}})

	rt := NewAgentRuntime()
	rt.Stealth = true // hermetic: no DB writes
	rt.SystemPrompt = "test-system"
	rt.ModelID = "m-double-compact"
	rt.ContextSize = 256 // 220/256 = 85% >= 80% threshold
	rt.AppendMessage("user", rt.ModelID, "old question")

	ch := make(chan any, 64)
	final, err := runTurnLoop(context.Background(), server.URL+"/v1/chat/completions", rt, ch, &LlamaCpp{}, assembleRunToolResponsePayload(rt))
	if err != nil {
		t.Fatalf("turn loop errored: %v", err)
	}
	// Three model calls total: text turn, register turn, final text turn.
	if n != 3 {
		t.Fatalf("expected 3 model calls, got %d — stale usage re-fired compaction", n)
	}
	if final != "done" {
		t.Fatalf("final response = %q, want \"done\"", final)
	}

	// Registration cleared the in-flight flag and nothing re-armed it.
	if rt.Compacting() {
		t.Fatal("a compaction is still in flight — stale usage report re-fired a second auto-compaction")
	}

	// A spurious injected system-compact would leave marker rows behind.
	if len(rt.Messages) != 2 {
		t.Fatalf("history must be seed + final response, got %d rows: %+v", len(rt.Messages), rt.Messages)
	}
	role, _ := rt.Messages[0]["role"].(string)
	content, _ := rt.Messages[0]["content"].(string)
	if role != "user" || !strings.HasPrefix(content, CompactionSeedPrefix) {
		t.Fatalf("first row must be the seed user row, got %+v", rt.Messages[0])
	}
}

// TestRunManualCompactionEmitsEof locks the lockup regression: /compact streams
// through the run channel like an interactive turn, so it MUST emit the trailing
// eof sentinel (PromptWithContext does this for normal prompts). Without it both UIs
// stay locked in "active" forever — the web statusbar and the TUI state machine
// only return to idle on an eol.
func TestRunManualCompactionEmitsEof(t *testing.T) {
	registerSSE := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"tc-1\",\"type\":\"function\",\"function\":{\"name\":\"system-register_compaction\",\"arguments\":\"{\\\"summary\\\": \\\"s\\\"}\"}}]}}}\n\ndata: [DONE]\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, registerSSE)
	}))
	defer server.Close()

	prevRT := GetMainRuntimeInternal()
	rt := NewAgentRuntime()
	rt.Stealth = true
	rt.SystemPrompt = "test-system"
	rt.ModelID = "m-eof"
	SetMainRuntime(rt)
	defer func() { SetMainRuntime(prevRT) }()

	setTestAppConfig(t, &data.AppConfig{Models: []data.ModelConfig{{
		ID: "m-eof", Executor: "llama.cpp", URL: server.URL, Managed: false,
	}}})

	resp := make(chan any, 64)
	cf := RunManualCompaction(resp, "m-eof")
	if cf == nil {
		t.Fatal("RunManualCompaction must return a non-nil cancel function")
	}

	// The run ends with exactly one terminal eof sentinel. Anything after it is
	// a double-emit; absence is the original lockup. Along the way the
	// register_compaction handler must have emitted the "compaction" UI event
	// (the seeded summary) so both interfaces render it live.
	var sawEof, sawCompaction bool
	for i := 0; i < 64; i++ {
		select {
		case msg := <-resp:
			r, ok := msg.(data.Response)
			if !ok {
				continue
			}
			if r.Type == "compaction" {
				sawCompaction = true
			}
			if r.Type == "eof" {
				if sawEof {
					t.Fatal("eof sentinel emitted more than once")
				}
				sawEof = true
				break
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for the /compact run to finish; eof seen=%v", sawEof)
		}
		if sawEof {
			break
		}
	}
	if !sawEof {
		t.Fatal("/compact must emit a terminal eof sentinel so the UIs return to idle")
	}
	if !sawCompaction {
		t.Fatal("/compact run must emit the compaction UI event (the seeded summary) for live display")
	}

	// The run must also have completed the compaction cleanly.
	if !rt.stopRequested.Load() {
		t.Fatal("manual compaction must stop the run")
	}
}
