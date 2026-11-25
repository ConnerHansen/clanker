package tui

import (
	"clankerv2/data"
	"fmt"
	"testing"
)

func sampleAskRequest() data.AskUserRequest {
	return data.AskUserRequest{
		ID: "ask-1",
		Questions: []data.AskUserQuestion{
			{
				ID:     "model",
				Type:   "select",
				Prompt: "Which model should I use?",
				Options: []data.AskOption{
					{Label: "Small", Value: "small"},
					{Label: "Large", Value: "large"},
				},
			},
			{
				ID:     "features",
				Type:   "multi-select",
				Prompt: "Which features?",
				Options: []data.AskOption{
					{Label: "Auth", Value: "auth"},
					{Label: "Billing", Value: "billing"},
					{Label: "Exports", Value: "exports"},
				},
			},
			{
				ID:          "notes",
				Type:        "free-response",
				Prompt:      "Anything else?",
				Placeholder: "optional",
			},
		},
	}
}

func TestAskNewStateDefaults(t *testing.T) {
	a := newAskState(sampleAskRequest())

	if len(a.fields) != 3 {
		t.Fatalf("expected 3 fields, got %d", len(a.fields))
	}
	if a.phase != askPhaseAnswers || a.active != 0 {
		t.Fatalf("expected answers phase, active question 0; got phase=%d active=%d", a.phase, a.active)
	}

	// Options start uncommitted; enter selects (the highlighted row shows
	// the current position, not a tentative pick).
	if len(a.fields[0].picked) != 0 {
		t.Fatalf("select should start with nothing picked: %v", a.fields[0].picked)
	}

	// Multi-select starts with nothing picked.
	if len(a.fields[1].picked) != 0 {
		t.Fatalf("multi-select should start empty: %v", a.fields[1].picked)
	}

	// Free-response opens in editing mode.
	if !a.fields[2].editing {
		t.Fatal("free-response field should start in editing mode")
	}
}

func TestAskMoveCursorWrap(t *testing.T) {
	a := newAskState(sampleAskRequest())
	f := &a.fields[0]

	// Two options: down wraps 0 -> 1. Move only shifts the highlight; it
	// does not change the selection (enter commits).
	f.moveCursor(1)
	if f.cursor != 1 || len(f.picked) != 0 {
		t.Fatalf("down: cursor=%d picked=%v; want cursor=1 picked={}", f.cursor, f.picked)
	}

	// Up wraps back around.
	f.moveCursor(-1)
	if f.cursor != 0 || len(f.picked) != 0 {
		t.Fatalf("up: cursor=%d picked=%v; want cursor=0 picked={}", f.cursor, f.picked)
	}

	// Multi-select navigation must NOT clear other picks.
	m := &a.fields[1]
	m.picked[0] = true
	m.moveCursor(1)
	if m.cursor != 1 || !m.picked[0] {
		t.Fatalf("multi-select move should preserve picks: cursor=%d picked=%v", m.cursor, m.picked)
	}
}

func TestAskNavRowsAndRowAt(t *testing.T) {
	req := data.AskUserRequest{ID: "ask-1", Questions: []data.AskUserQuestion{{
		ID: "q", Type: "select", Prompt: "p", AllowFreeForm: true,
		Options: []data.AskOption{{Label: "a", Value: "a"}},
	}}}
	f := &newAskState(req).fields[0]

	// A select with free-form navigates option 0 then the free-form row.
	rows := f.navRows()
	if len(rows) != 2 || rows[0] != 0 || rows[1] != freeFormIndex {
		t.Fatalf("navRows: got %v, want [0 %d]", rows, freeFormIndex)
	}
	if got := f.rowAt(0); got != 0 {
		t.Fatalf("rowAt(0): got %d, want 0", got)
	}
	if got := f.rowAt(1); got != freeFormIndex {
		t.Fatalf("rowAt(1): got %d, want %d", got, freeFormIndex)
	}
	if got := f.rowAt(2); got != -1 {
		t.Fatalf("rowAt(2) out-of-range: got %d, want -1", got)
	}

	// A multi-select appends the Next (Tab) sentinel after the free-form row.
	mreq := data.AskUserRequest{ID: "ask-2", Questions: []data.AskUserQuestion{{
		ID: "q", Type: "multi-select", Prompt: "p", AllowFreeForm: true,
		Options: []data.AskOption{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}},
	}}}
	m := &newAskState(mreq).fields[0]
	mrows := m.navRows()
	want := []int{0, 1, freeFormIndex, nextIndex}
	if len(mrows) != len(want) {
		t.Fatalf("multi navRows: got %v, want %v", mrows, want)
	}
	for i := range want {
		if mrows[i] != want[i] {
			t.Fatalf("multi navRows[%d]: got %d, want %d", i, mrows[i], want[i])
		}
	}
}

func TestAskCollectAnswers(t *testing.T) {
	a := newAskState(sampleAskRequest())

	// Select the second single-choice option.
	a.fields[0].picked = map[int]bool{1: true}

	// Toggle two multi-select options.
	a.fields[1].picked = map[int]bool{0: true, 2: true}

	// Type into free-response (with surrounding whitespace to exercise trimming).
	a.fields[2].text = "  use the staging env  "

	answers := a.collectAnswers()

	if answers["model"] != "large" {
		t.Fatalf("model: got %q, want large", answers["model"])
	}
	if answers["features"] != "auth,exports" {
		t.Fatalf("features: got %q, want auth,exports", answers["features"])
	}
	if answers["notes"] != "use the staging env" {
		t.Fatalf("notes: got %q, want trimmed text", answers["notes"])
	}
}

// TestAskFreeCommittedSemantics pins the single-choice rule that a committed
// free-form answer supersedes a picked option, while an empty commit leaves the
// pick intact (landing on the free-form row must never drop an answer).
func TestAskFreeCommittedSemantics(t *testing.T) {
	req := data.AskUserRequest{ID: "ask-fc", Questions: []data.AskUserQuestion{{
		ID: "q", Type: "select", Prompt: "p", AllowFreeForm: true,
		Options: []data.AskOption{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}},
	}}}

	// Committed free-form text supersedes the picked option.
	a := newAskState(req)
	a.fields[0].picked = map[int]bool{1: true}
	a.fields[0].freeCommitted = true
	a.fields[0].text = "custom"
	if got := a.collectAnswers()["q"]; got != "custom" {
		t.Fatalf("committed free-form: got %q, want custom", got)
	}
	if got := a.fields[0].currentAnswer(); got != "custom" {
		t.Fatalf("committed free-form display: got %q, want custom", got)
	}

	// Empty commit leaves the picked option intact.
	a = newAskState(req)
	a.fields[0].picked = map[int]bool{1: true}
	a.fields[0].freeCommitted = true
	a.fields[0].text = "   "
	if got := a.collectAnswers()["q"]; got != "b" {
		t.Fatalf("empty commit: got %q, want b (pick retained)", got)
	}
	if got := a.fields[0].currentAnswer(); got != "b" {
		t.Fatalf("empty commit display: got %q, want b", got)
	}

	// Uncommitted typed text with no pick is the answer.
	a = newAskState(req)
	a.fields[0].text = "draft"
	if got := a.collectAnswers()["q"]; got != "draft" {
		t.Fatalf("uncommitted text, no pick: got %q, want draft", got)
	}

	// Uncommitted typed text with a pick keeps the pick.
	a = newAskState(req)
	a.fields[0].picked = map[int]bool{0: true}
	a.fields[0].text = "draft"
	if got := a.collectAnswers()["q"]; got != "a" {
		t.Fatalf("uncommitted text with pick: got %q, want a", got)
	}
}

func TestAskPhaseTransitions(t *testing.T) {
	a := newAskState(sampleAskRequest())

	a.enterConfirm()
	if a.phase != askPhaseConfirm || a.confirmSelected != 0 {
		t.Fatalf("enterConfirm: phase=%d selected=%d; want confirm + yes default", a.phase, a.confirmSelected)
	}

	a.goBackFromConfirm()
	if a.phase != askPhaseAnswers {
		t.Fatalf("goBackFromConfirm should return to answers")
	}
	if a.active != len(a.fields)-1 {
		t.Fatalf("goBackFromConfirm should refocus the last question: active=%d, want %d", a.active, len(a.fields)-1)
	}
}

// longSelectQuestion builds a single select with n options.
func longSelectQuestion(n int) data.AskUserRequest {
	opts := make([]data.AskOption, 0, n)
	for i := 0; i < n; i++ {
		opts = append(opts, data.AskOption{Label: fmt.Sprintf("opt-%d", i), Value: fmt.Sprintf("v%d", i)})
	}
	return data.AskUserRequest{ID: "ask-1", Questions: []data.AskUserQuestion{{
		ID: "q", Type: "select", Prompt: "many options", Options: opts,
	}}}
}

func TestAskWindowedPositions(t *testing.T) {
	a := newAskState(longSelectQuestion(20))
	f := &a.fields[0]

	// Cursor at top: window starts at position 0, 14 hidden.
	pos, hidden := f.windowedPositions()
	if len(pos) != askMaxVisibleRows || pos[0] != 0 {
		t.Fatalf("top window: len=%d first=%v; want %d positions starting at 0", len(pos), pos[:1], askMaxVisibleRows)
	}
	if hidden != 20-askMaxVisibleRows {
		t.Fatalf("hidden: got %d, want %d", hidden, 20-askMaxVisibleRows)
	}

	// Cursor mid-list: window is centered on the cursor (cursor-3 .. cursor+2).
	f.cursor = 10
	pos, _ = f.windowedPositions()
	if pos[0] != 7 || pos[len(pos)-1] != 12 {
		t.Fatalf("centered window: got [%d..%d], want [7..12]", pos[0], pos[len(pos)-1])
	}

	// Cursor near the bottom: window anchors to the end of the list.
	f.cursor = 19
	pos, hidden = f.windowedPositions()
	if f.rowAt(pos[len(pos)-1]) != 19 {
		t.Fatalf("bottom window should include the last option: got last row %d, want 19", f.rowAt(pos[len(pos)-1]))
	}
	if hidden <= 0 {
		t.Fatalf("expected hidden rows near bottom, got %d hidden", hidden)
	}

	// Short lists are never windowed (multi-select adds a Next sentinel row).
	short := newAskState(sampleAskRequest())
	sf := &short.fields[1]
	sp, h := sf.windowedPositions()
	if h != 0 {
		t.Fatalf("3-option multi-select should not hide rows, got %d hidden", h)
	}
	if len(sp) != 4 || sf.rowAt(sp[len(sp)-1]) != nextIndex {
		t.Fatalf("3-option multi-select should expose the Next row: %v", sp)
	}
}

func TestAskCurrentAnswerDisplay(t *testing.T) {
	a := newAskState(sampleAskRequest())

	// An uncommitted select shows a dash until enter picks an option.
	if got := a.fields[0].currentAnswer(); got != "—" {
		t.Fatalf("uncommitted answer display: %q", got)
	}
	a.fields[0].picked = map[int]bool{0: true}
	if got := a.fields[0].currentAnswer(); got != "Small" {
		t.Fatalf("selected answer display: %q", got)
	}

	a.fields[1].picked = map[int]bool{0: true, 2: true}
	if got := a.fields[1].currentAnswer(); got != "Auth, Exports" {
		t.Fatalf("multi-select display: %q", got)
	}

	if got := a.fields[2].currentAnswer(); got != "(empty)" {
		t.Fatalf("empty free-response display: %q", got)
	}
	a.fields[2].text = "hello"
	if got := a.fields[2].currentAnswer(); got != "hello" {
		t.Fatalf("free-response display: %q", got)
	}
}
