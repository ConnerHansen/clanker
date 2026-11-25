package tui

import (
	"regexp"
	"strings"
	"testing"

	"clankerv2/data"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"
)

// multiFreeRequest builds a multi-select with two options plus a free-form row.
func multiFreeRequest() data.AskUserRequest {
	return data.AskUserRequest{ID: "ask-mf", Questions: []data.AskUserQuestion{{
		ID: "q", Type: "multi-select", Prompt: "p", AllowFreeForm: true,
		Options: []data.AskOption{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}},
	}}}
}

// singleFreeRequest builds a single select with two options plus a free-form row.
// The placeholder mirrors what real requests carry so the gray prompt label
// renders (bubbles only shows placeholder text that was set on the widget).
func singleFreeRequest() data.AskUserRequest {
	return data.AskUserRequest{ID: "ask-sf", Questions: []data.AskUserQuestion{{
		ID: "q", Type: "select", Prompt: "p", AllowFreeForm: true,
		Placeholder: "Type your own answer…",
		Options:     []data.AskOption{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}},
	}}}
}

// pressKey drives one key message through the model and adopts the result.
func pressKey(m *Model, msg tea.KeyMsg) {
	updated, _ := m.Update(msg)
	adopt(m, updated)
}

var askAnsire = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// askPlain strips ANSI escapes so rendered rows can be compared as plain text.
func askPlain(s string) string { return askAnsire.ReplaceAllString(s, "") }

// askColOf returns the display column (0-based, box padding included) of the
// first occurrence of probe in a rendered row, or -1. runewidth keeps
// multi-cell glyphs measured correctly.
func askColOf(row, probe string) int {
	p := askPlain(row)
	idx := strings.Index(p, probe)
	if idx < 0 {
		return -1
	}
	return runewidth.StringWidth(p[:idx])
}

// multiThreeRequest builds a three-field form: multi-select with free-form,
// single select (no free-form), and a bare free-response.
func multiThreeRequest() data.AskUserRequest {
	return data.AskUserRequest{ID: "ask-mt", Questions: []data.AskUserQuestion{{
		ID: "q1", Type: "multi-select", Prompt: "p1", AllowFreeForm: true,
		Options: []data.AskOption{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}},
	}, {
		ID: "q2", Type: "select", Prompt: "p2",
		Options: []data.AskOption{{Label: "x", Value: "x"}},
	}, {
		ID: "q3", Type: "free-response", Prompt: "p3",
		Placeholder: "type something",
	}}}
}

// TestAskMultiFreeFormToggle drives the multi-select free-form row through the
// full key pipeline: highlighting it is NOT selected, typing auto-selects it,
// and enter just toggles it (never advancing). The user can check and uncheck
// the text entry as often as they like; tab moves on.
func TestAskMultiFreeFormToggle(t *testing.T) {
	nm := NewModel("dev")
	m := &nm
	m.handleResize(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.handleAskRequest(multiFreeRequest())
	f := &m.askState.fields[0]

	// Navigate from option 0 to the free-form row.
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if f.cursor != 2 || f.rowAt(f.cursor) != freeFormIndex {
		t.Fatalf("after two downs: cursor=%d row=%d; want the free-form row", f.cursor, f.rowAt(f.cursor))
	}
	if len(f.picked) != 0 {
		t.Fatalf("highlighting the free-form row must not select it: %v", f.picked)
	}

	// Typing auto-selects the row.
	pressKey(m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if !f.picked[freeFormIndex] || f.text != "g" {
		t.Fatalf("after typing 'g': picked=%v text=%q; want free-form row selected with text", f.picked, f.text)
	}

	// Enter on a checked row deselects it and STAYS (no advance).
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.askState.phase != askPhaseAnswers {
		t.Fatalf("enter must not advance from the free-form row, phase=%d", m.askState.phase)
	}
	if _, on := f.picked[freeFormIndex]; on {
		t.Fatalf("enter should deselect the free-form row: %v", f.picked)
	}

	// Enter again re-selects it (toggle on).
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !f.picked[freeFormIndex] {
		t.Fatalf("second enter should re-select the free-form row: %v", f.picked)
	}

	// Tab advances to the confirm phase.
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.askState.phase != askPhaseConfirm {
		t.Fatalf("tab from a multi-select should reach confirm, phase=%d", m.askState.phase)
	}

	m.closeAsk()
}

// TestAskMultiFreeFormToggleOffKeepsCursor covers turning an empty free-form
// row on: enter selects it and stays put so typing can begin.
func TestAskMultiFreeFormToggleOffKeepsCursor(t *testing.T) {
	nm := NewModel("dev")
	m := &nm
	m.handleResize(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.handleAskRequest(multiFreeRequest())
	f := &m.askState.fields[0]

	pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown})

	// Enter on the unselected (empty) row selects it and does NOT advance.
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.askState.phase != askPhaseAnswers {
		t.Fatalf("enter on empty free-form row should not advance, phase=%d", m.askState.phase)
	}
	if !f.picked[freeFormIndex] {
		t.Fatalf("enter should select the free-form row: %v", f.picked)
	}

	m.closeAsk()
}

// TestAskMultiFreeFormCollect pins the answer-collection rule: typed free-form
// text is submitted only while its row is toggled on.
func TestAskMultiFreeFormCollect(t *testing.T) {
	// Row toggled on with text: the text is part of the answer.
	a := newAskState(multiFreeRequest())
	a.fields[0].picked = map[int]bool{freeFormIndex: true}
	a.fields[0].text = "  custom thing  "
	if got := a.collectAnswers()["q"]; got != "custom thing" {
		t.Fatalf("toggled-on free-form: got %q, want %q", got, "custom thing")
	}

	// Row toggled off (empty after deselect) but text retained in state: the
	// text is NOT submitted.
	a = newAskState(multiFreeRequest())
	a.fields[0].picked = map[int]bool{}
	a.fields[0].text = "custom thing"
	if got := a.collectAnswers()["q"]; got != "" {
		t.Fatalf("toggled-off free-form must not submit text: got %q", got)
	}

	// Mixed: one real option plus the toggled-on free-form row.
	a = newAskState(multiFreeRequest())
	a.fields[0].picked = map[int]bool{0: true, freeFormIndex: true}
	a.fields[0].text = "extra"
	if got := a.collectAnswers()["q"]; got != "a,extra" {
		t.Fatalf("option + free-form: got %q, want a,extra", got)
	}

	// Confirm display mirrors the row state.
	if got := a.fields[0].currentAnswer(); got != "a, extra" {
		t.Fatalf("confirm display: got %q, want %q", got, "a, extra")
	}
}

// TestAskFreeTextPerFieldAndCollect covers the shared-widget value lifecycle:
// each page shows its own text (no inheritance from the previous field), text
// survives tabbing away and back (and re-renders on return), and the
// assembled answer rollup includes every typed value.
func TestAskFreeTextPerFieldAndCollect(t *testing.T) {
	nm := NewModel("dev")
	m := &nm
	m.handleResize(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.handleAskRequest(multiThreeRequest())
	a := m.askState

	// Field 1 (multi): land on the free-form row and type.
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	for _, r := range "custom thing" {
		pressKey(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if got := m.components.askTextInput.Value(); got != "custom thing" {
		t.Fatalf("widget value after typing: %q", got)
	}

	// Tab to field 2 (select): the input must show ITS text (empty), not the
	// previous field's.
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if a.active != 1 {
		t.Fatalf("tab should move to field 2, active=%d", a.active)
	}
	if got := m.components.askTextInput.Value(); got != "" {
		t.Fatalf("select page must not inherit the previous field's text: %q", got)
	}

	// Tab to field 3 (bare free-response): also its own empty text.
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if a.active != 2 {
		t.Fatalf("tab should move to field 3, active=%d", a.active)
	}
	if got := m.components.askTextInput.Value(); got != "" {
		t.Fatalf("free-response must not inherit text: %q", got)
	}

	// Type on the bare field.
	for _, r := range "ideal llama" {
		pressKey(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if got := m.components.askTextInput.Value(); got != "ideal llama" {
		t.Fatalf("bare field value after typing: %q", got)
	}

	// Tab to review.
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if a.phase != askPhaseConfirm {
		t.Fatalf("tab from the last field should reach confirm, phase=%d", a.phase)
	}
	// The bare field's text must show on the confirm summary (it is no longer
	// in editing mode once the page advanced away).
	if got := a.fields[2].currentAnswer(); got != "ideal llama" {
		t.Fatalf("confirm display for bare field: got %q, want ideal llama", got)
	}

	// Shift+tab back: the bare field re-renders with its text.
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if a.phase != askPhaseAnswers || a.active != 2 {
		t.Fatalf("shift+tab should return to field 3, phase=%d active=%d", a.phase, a.active)
	}
	f3 := &a.fields[2]
	if f3.text != "ideal llama" {
		t.Fatalf("bare field lost its text: %q", f3.text)
	}
	panel := m.renderAskForm()
	if !strings.Contains(panel, "ideal llama") {
		t.Fatalf("returning to the bare field must render its text:\n%s", panel)
	}

	// Back to review and check the rollup includes every typed value.
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if a.phase != askPhaseConfirm {
		t.Fatalf("expected confirm phase, got %d", a.phase)
	}
	got := a.collectAnswers()
	want := map[string]string{
		"q1": "custom thing", // free-form row was auto-selected by typing
		"q2": "",             // nothing picked on the select
		"q3": "ideal llama",  // bare free-response text
	}
	if len(got) != len(want) {
		t.Fatalf("collected %d answers, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("answer[%s] = %q, want %q (full: %v)", k, got[k], v, got)
		}
	}

	m.closeAsk()
}

// TestAskMultiFreeFormRenderBox checks the free-form row carries the □/■ box
// like any other multi-select option - including while the input is focused -
// and that the label follows the checked state.
func TestAskMultiFreeFormRenderBox(t *testing.T) {
	nm := NewModel("dev")
	m := &nm
	m.handleResize(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.handleAskRequest(multiFreeRequest())
	f := &m.askState.fields[0]

	// Cursor on the free-form row, unselected: empty box + prompt label.
	f.cursor = 2 // freeFormIndex position
	panel := m.renderAskForm()
	if !strings.Contains(panel, "□") || !strings.Contains(panel, "Type your own answer…") {
		t.Fatalf("unselected free-form row missing □/label:\n%s", panel)
	}

	// Focused with the row checked: the inline input line must keep a FILLED
	// box so the toggle is visible while typing.
	f.picked[freeFormIndex] = true
	f.text = "gofmt"
	m.setAskTextEditing(f, true)
	panel = m.renderAskForm()
	if !strings.Contains(panel, "■") || !strings.Contains(panel, "gofmt") {
		t.Fatalf("focused checked free-form row missing ■/typed text:\n%s", panel)
	}

	// Focused with the row unchecked: the box is empty again.
	f.picked = map[int]bool{}
	m.setAskTextEditing(f, true)
	panel = m.renderAskForm()
	if strings.Contains(panel, "■") {
		t.Fatalf("focused unchecked free-form row must not show a filled box:\n%s", panel)
	}

	m.closeAsk()
}

// TestAskInputWidthSizing pins the SetWidth math that keeps the inline
// free-form row (margin + prefix + view) exactly at the box inner width for
// every row shape, so the terminal never wraps it.
func TestAskInputWidthSizing(t *testing.T) {
	for _, termW := range []int{120, 80, 60, 40} {
		// Multi-select: margin(2) + box(1) + gap(1) = 4-col prefix.
		nm := NewModel("dev")
		m := &nm
		m.handleResize(tea.WindowSizeMsg{Width: termW, Height: 30})
		m.handleAskRequest(multiFreeRequest())

		prefixW := lipgloss.Width(styleAskSelected.Render("  ") + "□" + styleAskSelected.Render(" "))
		viewW := m.components.askTextInput.Width() + 1 // textinput reserves a cursor slot
		if total := prefixW + viewW; total != termW-4 {
			t.Fatalf("termW=%d: multi inline row is %d cols, want inner width %d", termW, total, termW-4)
		}
		if got := m.components.askTextInput.Width(); got != askInputWidth(termW, true) {
			t.Fatalf("termW=%d: multi input width %d, want %d", termW, got, askInputWidth(termW, true))
		}
		m.closeAsk()

		// Single-select: margin(2) + arrow(2) + " ✓ " marker slot(3) = 7-col prefix.
		nm = NewModel("dev")
		m = &nm
		m.handleResize(tea.WindowSizeMsg{Width: termW, Height: 30})
		m.handleAskRequest(singleFreeRequest())

		prefixW = lipgloss.Width(styleAskSelected.Render("  ") + selectedPrefix + " " + checkPrefix + " ")
		viewW = m.components.askTextInput.Width() + 1
		if total := prefixW + viewW; total != termW-4 {
			t.Fatalf("termW=%d: single inline row is %d cols, want inner width %d", termW, total, termW-4)
		}
		if got := m.components.askTextInput.Width(); got != askInputWidth(termW, false) {
			t.Fatalf("termW=%d: single input width %d, want %d", termW, got, askInputWidth(termW, false))
		}
		m.closeAsk()

		// Bare free-response: 2-col indent; the shared sizing keeps it within bounds.
		nm = NewModel("dev")
		m = &nm
		m.handleResize(tea.WindowSizeMsg{Width: termW, Height: 30})
		m.handleAskRequest(data.AskUserRequest{ID: "ask-fr", Questions: []data.AskUserQuestion{{
			ID: "q", Type: "free-response", Prompt: "p",
		}}})
		if got := m.components.askTextInput.Width(); got != askInputWidth(termW, false) {
			t.Fatalf("termW=%d: bare input width %d, want %d", termW, got, askInputWidth(termW, false))
		}
		m.closeAsk()
	}
}

// TestAskSingleFreeFormCheckedRow pins the single-choice free-form row's checked
// state and its editability: no mark until committed, then a check bumping the
// label over a column - and picking an option afterwards supersedes the commit
// (last explicit action wins), so the form can always be changed again.
func TestAskSingleFreeFormCheckedRow(t *testing.T) {
	nm := NewModel("dev")
	m := &nm
	m.handleResize(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.handleAskRequest(singleFreeRequest())
	f := &m.askState.fields[0]

	// Cursor on the free-form row (two options below) with nothing committed:
	// no check mark.
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if f.cursor != 2 || f.rowAt(f.cursor) != freeFormIndex {
		t.Fatalf("downs should land on the free-form row: cursor=%d row=%d", f.cursor, f.rowAt(f.cursor))
	}
	panel := m.renderAskForm()
	if strings.Count(panel, "✓") != 0 {
		t.Fatalf("uncommitted free-form row must not show a check:\n%s", panel)
	}

	// Type + enter commits and advances to confirm; the answer is the text.
	for _, r := range "custom" {
		pressKey(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.askState.phase != askPhaseConfirm {
		t.Fatalf("enter on the free-form row should reach confirm, phase=%d", m.askState.phase)
	}
	if got := f.currentAnswer(); got != "custom" {
		t.Fatalf("confirm display: got %q, want custom", got)
	}

	// Back: the free-form row carries the check + typed label; no other marks.
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	panel = m.renderAskForm()
	if !strings.Contains(panel, "✓") || !strings.Contains(panel, "custom") {
		t.Fatalf("committed free-form row missing check/label:\n%s", panel)
	}
	if n := strings.Count(panel, "✓"); n != 1 {
		t.Fatalf("exactly one check expected (on the free-form row), got %d:\n%s", n, panel)
	}

	// Pick an option afterwards: the last explicit action wins - the pick
	// supersedes the committed text in both the answer and the render (the
	// check moves to the picked option, the free row back to its gray prompt).
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyUp}) // option b
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyUp}) // back to option a
	if f.rowAt(f.cursor) != 0 {
		t.Fatalf("up should land on option 0, row=%d", f.rowAt(f.cursor))
	}
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // pick option a -> confirm
	if m.askState.phase != askPhaseConfirm || !f.picked[0] {
		t.Fatalf("enter on option 0 should pick it and reach confirm: picked=%v", f.picked)
	}
	if f.freeCommitted {
		t.Fatal("picking an option must clear the free-form commit")
	}
	if got := m.askState.collectAnswers()["q"]; got != "a" {
		t.Fatalf("the pick must supersede the committed text: got %q, want a", got)
	}
	if got := f.currentAnswer(); got != "a" {
		t.Fatalf("confirm display after re-pick: got %q, want a", got)
	}

	pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	panel = m.renderAskForm()
	if n := strings.Count(panel, "✓"); n != 1 {
		t.Fatalf("exactly one check expected (on option a), got %d:\n%s", n, panel)
	}
	if strings.Contains(askPlain(panel), "custom") {
		t.Fatalf("superseded committed text must not render on the free row:\n%s", askPlain(panel))
	}

	m.closeAsk()
}

// TestAskSingleFreeFormInlineAlignment pins that the inline free-form entry sits
// at the same column whether or not a check is present: uncommitted input starts
// where the static row's gray label does, and only a committed " ✓ " slot bumps
// it one column right (matching the static checked row). The caret offset is
// data-driven, so pinning it keeps cursor placement in step.
func TestAskSingleFreeFormInlineAlignment(t *testing.T) {
	const termW = 100
	nm := NewModel("dev")
	m := &nm
	m.handleResize(tea.WindowSizeMsg{Width: termW, Height: 30})
	m.handleAskRequest(singleFreeRequest())
	f := &m.askState.fields[0]

	pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // option b
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // free row (inline)
	panel := m.renderAskForm()

	// Uncommitted: marker slot is 1 col; placeholder/input start at the same
	// column as the static gray label (which sits with the option labels).
	uncommittedPrefix := "  " + selectedPrefix + " "
	if got, want := m.askState.inputColOffset, 2+runewidth.StringWidth(uncommittedPrefix); got != want {
		t.Fatalf("uncommitted input offset: got %d, want %d", got, want)
	}
	inlineRow := ""
	for _, line := range strings.Split(panel, "\n") {
		if askColOf(line, "Type your own answer…") >= 0 && strings.Contains(askPlain(line), selectedPrefix) {
			inlineRow = line
		}
	}
	if inlineRow == "" {
		t.Fatalf("no inline placeholder row found in:\n%s", askPlain(panel))
	}
	inlineCol := askColOf(inlineRow, "Type your own answer…")

	// The static (non-cursor) free row must put its label at the same column.
	f.cursor = 0 // back on option a so the free row renders statically
	panel = m.renderAskForm()
	staticCol := -1
	for _, line := range strings.Split(panel, "\n") {
		if c := askColOf(line, "Type your own answer…"); c >= 0 && !strings.Contains(askPlain(line), selectedPrefix) {
			staticCol = c
		}
	}
	if staticCol < 0 || inlineCol != staticCol {
		t.Fatalf("inline placeholder col %d must match static label col %d", inlineCol, staticCol)
	}

	// Commit: land back on the free row (the static check above parked the
	// cursor on option a), type, and enter to commit -> confirm.
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // option b
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // free row
	for _, r := range "custom" {
		pressKey(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // commit -> confirm
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}) // back (re-focuses free row)
	m.renderAskForm()
	if f.rowAt(f.cursor) != freeFormIndex || !f.editing {
		t.Fatalf("back should land editing on the free row: cursor=%d editing=%v", f.cursor, f.editing)
	}
	committedPrefix := "  " + selectedPrefix + " " + checkPrefix + " "
	if got, want := m.askState.inputColOffset, 2+runewidth.StringWidth(committedPrefix); got != want {
		t.Fatalf("committed input offset: got %d, want %d", got, want)
	}

	m.closeAsk()
}
