package tui

import (
	"clankerv2/agent"
	"clankerv2/data"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// askPhase tracks which stage of the system-ask form is active.
type askPhase int

const (
	askPhaseAnswers askPhase = iota // navigating/answering the questions
	askPhaseConfirm                 // "Ready to submit responses?" summary
)

// Reserved row sentinels appended to a field's navigable row list. Real options
// use their slice index; these mark the synthetic rows.
const (
	freeFormIndex = -1 // "Type your own answer…" row (allow_free_form)
	nextIndex     = -2 // multi-select "Next (Tab)" row
)

type askQuestionState struct {
	q       data.AskUserQuestion
	isMulti bool
	hasFree bool
	// cursor is a POSITIONAL index into navRows() (the ordered list of real
	// option indices plus any sentinel rows), not an option slice index.
	// Multi-select fields may also hold the freeFormIndex key in picked while
	// their free-form row is toggled on.
	cursor int
	picked map[int]bool
	// editing means free-form text entry is active: always for a bare
	// free-response field, otherwise only while the cursor sits on the
	// freeFormIndex row. While editing, the shared askTextInput owns the
	// field's text; text mirrors its value for answer collection and sync.
	editing bool
	// freeCommitted marks that typed free-form text is this field's committed
	// answer: on single-choice it supersedes a previously picked option until
	// an option is picked again (the last explicit action wins).
	// (Multi-select free-form rows track their selection via
	// picked[freeFormIndex] instead.)
	freeCommitted bool
	text          string
}

// AskState is the full UI state of one pending system-ask request. Only one
// question is shown at a time (a.active); answers are held per-field and
// persist while tabbing around; nothing resolves until the user confirms in
// askPhaseConfirm (or cancels).
type AskState struct {
	request         *data.AskUserRequest
	fields          []askQuestionState
	active          int
	phase           askPhase
	confirmSelected int // 0 = Yes (default), 1 = No
	// inputContentLine / inputColOffset are the content-line index and column
	// offset of the free-text caret within the rendered panel for the current
	// frame (set by renderAskForm); View uses them to place the terminal caret.
	inputContentLine int
	inputColOffset   int
}

// ---------------------------------------------------------------------------
// Lifecycle

func newAskState(req data.AskUserRequest) *AskState {
	fields := make([]askQuestionState, 0, len(req.Questions))
	for _, q := range req.Questions {
		isMulti := q.Type == "multi-select"
		hasFree := q.AllowFreeForm && (q.Type == "select" || q.Type == "multi-select")

		f := askQuestionState{
			q:       q,
			isMulti: isMulti,
			hasFree: hasFree,
			cursor:  0,
			picked:  make(map[int]bool),
		}

		switch q.Type {
		case "free-response":
			f.editing = true
		default:
			// Options start uncommitted; enter is required to select
			// (the highlighted row shows the current position).
		}

		fields = append(fields, f)
	}

	return &AskState{
		request:         &req,
		fields:          fields,
		active:          0,
		phase:           askPhaseAnswers,
		confirmSelected: 0,
	}
}

// enterConfirm moves to the review phase with Yes pre-selected. The active
// field is left (text persisted, widget blurred) so confirm mode never owns a
// focused input.
func (a *AskState) enterConfirm() {
	a.phase = askPhaseConfirm
	a.confirmSelected = 0
}

// goBackFromConfirm returns to the answers phase focused on the last question.
func (a *AskState) goBackFromConfirm() {
	a.phase = askPhaseAnswers
	if len(a.fields) > 0 {
		a.active = len(a.fields) - 1
	}
}

// navRows returns the ordered list of navigable row values for a field: real
// options as their slice index, then the freeFormIndex sentinel (when
// allow_free_form), then the nextIndex sentinel (multi-select only). A bare
// free-response field has no rows.
func (f *askQuestionState) navRows() []int {
	if f.q.Type == "free-response" {
		return nil
	}
	rows := make([]int, 0, len(f.q.Options)+2)
	for i := range f.q.Options {
		rows = append(rows, i)
	}
	if f.hasFree {
		rows = append(rows, freeFormIndex)
	}
	if f.isMulti {
		rows = append(rows, nextIndex)
	}
	return rows
}

// rowAt maps a cursor position to its row value (option index or sentinel);
// -1 when the position is out of range.
func (f *askQuestionState) rowAt(pos int) int {
	rows := f.navRows()
	if pos < 0 || pos >= len(rows) {
		return -1
	}
	return rows[pos]
}

// moveCursor moves the highlight by one row, wrapping at the ends, without
// changing any selection. Enter commits (see processAskAnswers).
func (f *askQuestionState) moveCursor(dir int) {
	total := len(f.navRows())
	if total == 0 {
		return
	}
	f.cursor = ((f.cursor+dir)%total + total) % total
}

// currentAnswer renders the field's current value as a display string.
func (f *askQuestionState) currentAnswer() string {
	switch {
	case f.editing:
		if strings.TrimSpace(f.text) == "" {
			return "(empty)"
		}
		return truncateDisplay(f.text, 60)
	case !f.isMulti && f.hasFree && f.hasFreeText() && (f.freeCommitted || len(f.picked) == 0):
		// Free-form text is the answer for a select: committed (superseding any
		// pick) or typed with nothing picked; show it on the confirm screen.
		return truncateDisplay(f.text, 60)
	case f.q.Type == "free-response" && f.hasFreeText():
		// Bare field left (tabbed away / enter advanced): its text is the
		// answer on the confirm screen.
		return truncateDisplay(f.text, 60)
	case len(f.picked) == 0:
		return "—"
	default:
		labels := make([]string, 0, len(f.picked)+1)
		for i := range f.q.Options {
			if f.picked[i] {
				labels = append(labels, truncateDisplay(f.q.Options[i].Label, 40))
			}
		}
		if f.isMulti && f.hasFree && f.picked[freeFormIndex] {
			// Toggled-on free-form row: show the typed text (plain twin of
			// freeRowLabel, which returns styled text for the form).
			if t := strings.TrimSpace(f.text); t != "" {
				labels = append(labels, truncateDisplay(t, 40))
			} else {
				labels = append(labels, "Type your own answer…")
			}
		}
		return strings.Join(labels, ", ")
	}
}

// freeRowLabel is the rendered label for a free-form row in the form: the typed
// text (in the option foreground) while the row is checked, otherwise the gray
// prompt. The confirm summary uses plain text (see currentAnswer).
func (f *askQuestionState) freeRowLabel(checked, isCursor bool) string {
	if checked && strings.TrimSpace(f.text) != "" {
		s := styleAskOption
		if isCursor {
			s = styleAskSelOption
		}
		return s.Render(truncateDisplay(strings.TrimSpace(f.text), 40))
	}
	s := styleAskPlaceholder
	if isCursor {
		s = styleAskSelPlaceholder
	}
	return s.Render("Type your own answer…")
}

// hasFreeText reports whether the field carries non-empty free-form text.
func (f *askQuestionState) hasFreeText() bool {
	return strings.TrimSpace(f.text) != ""
}

// collectAnswers assembles the LLM-facing answer map (question ID -> value).
func (a *AskState) collectAnswers() map[string]string {
	answers := make(map[string]string, len(a.fields))
	// Seed every question ID with an empty value so the rollup always names
	// each question; per-type logic below fills in whatever was answered.
	for _, f := range a.fields {
		answers[f.q.ID] = ""
	}
	for _, f := range a.fields {
		switch f.q.Type {
		case "multi-select":
			var vals []string
			for i := range f.q.Options {
				if f.picked[i] {
					vals = append(vals, f.q.Options[i].Value)
				}
			}
			// The free-form row is an option like any other: its text counts
			// only while the row is toggled on (typing auto-selects it).
			if f.hasFree && f.picked[freeFormIndex] && strings.TrimSpace(f.text) != "" {
				vals = append(vals, strings.TrimSpace(f.text))
			}
			answers[f.q.ID] = strings.Join(vals, ",")
		case "select", "confirm":
			// Base answer: the picked option, if any.
			for i := range f.q.Options {
				if f.picked[i] {
					answers[f.q.ID] = f.q.Options[i].Value
					break
				}
			}
			// Free-form text supersedes the pick only when it was explicitly
			// committed (Enter on the free-form row with text typed); an empty
			// commit leaves the pick intact, so landing on the free-form row
			// drops nothing.
			if f.freeCommitted {
				if t := strings.TrimSpace(f.text); t != "" {
					answers[f.q.ID] = t
				}
			} else if len(f.picked) == 0 && f.hasFree {
				// Uncommitted typed text (tabbed away before Enter) is the answer
				// when nothing was picked.
				if t := strings.TrimSpace(f.text); t != "" {
					answers[f.q.ID] = t
				}
			}
		default: // free-response
			answers[f.q.ID] = strings.TrimSpace(f.text)
		}
	}
	return answers
}

func (m *Model) handleAskRequest(req data.AskUserRequest) {
	m.askState = newAskState(req)
	m.listMode = listModeAsk
	m.components.input.Blur()
	if m.components.askTextInputReady && m.components.width > 0 {
		m.components.askTextInput.SetWidth(askInputWidth(m.components.width, m.askState.fields[m.askState.active].isMulti))
	}
	if f := &m.askState.fields[m.askState.active]; f.editing {
		m.setAskTextEditing(f, true)
	}
	m.state.state = stateWaiting
}

// closeAsk tears down the ask UI and restores normal input. The agent emits its
// own post-resolution state transition (executing, or idle when the run ends),
// so this does not touch m.state.
func (m *Model) closeAsk() {
	if m.components.askTextInputReady {
		m.components.askTextInput.Blur()
	}
	m.askState = nil
	m.listMode = listModeNone
	m.restoreOverlaySource()
	m.components.input.Focus()
}

// cancelAsk resolves the blocked agent goroutine as cancelled, then closes.
func (m *Model) cancelAsk() {
	if m.askState != nil {
		agent.CancelAsk(m.askState.request.ID)
	}
	m.closeAsk()
}

// setAskTextEditing focuses or blurs the shared free-text input at the moment
// a field enters or leaves text-entry mode. Entering loads the field's stored
// answer with the caret at the end and sizes the widget for this field's row
// shape; leaving persists it back.
func (m *Model) setAskTextEditing(f *askQuestionState, on bool) {
	if !m.components.askTextInputReady {
		return
	}
	ti := &m.components.askTextInput
	if on {
		if m.components.width > 0 {
			ti.SetWidth(askInputWidth(m.components.width, f.isMulti))
		}
		ti.SetValue(f.text)
		ti.CursorEnd()
		ti.Placeholder = f.q.Placeholder
		_ = ti.Focus()
	} else {
		f.text = ti.Value()
		ti.Blur()
	}
}

// processAsk handles keys (and paste) while a system-ask form is active.
func (m *Model) processAsk(msg tea.Msg) (tea.Model, tea.Cmd) {
	a := m.askState
	if a == nil {
		return m, nil
	}

	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		// Non-key input (paste): text mode lands in the input, option mode
		// ignores it.
		if f := &a.fields[a.active]; a.phase == askPhaseAnswers && f.editing {
			return m.processAskText(msg)
		}
		return m, nil
	}

	switch keyMsg.String() {
	case "esc":
		m.cancelAsk()
		return m, nil
	case "ctrl+c":
		m.cancelAsk()
		return m, tea.Quit
	}

	switch a.phase {
	case askPhaseAnswers:
		return m.processAskAnswers(keyMsg)
	default:
		return m.processAskConfirm(keyMsg)
	}
}

// processAskText forwards text-input messages to the free-text input. The
// field's stored text is updated from the widget after each change; for a
// multi-select field, typing auto-selects the free-form row (it behaves like
// a checked option until enter toggles it back off).
func (m *Model) processAskText(msg tea.Msg) (tea.Model, tea.Cmd) {
	f := &m.askState.fields[m.askState.active]
	if !f.editing || !m.components.askTextInputReady {
		return m, nil
	}
	m.components.askTextInput, _ = m.components.askTextInput.Update(msg)
	f.text = m.components.askTextInput.Value()
	if f.isMulti && strings.TrimSpace(f.text) != "" {
		f.picked[freeFormIndex] = true // typing arms the row; enter toggles it
	}
	return m, nil
}

// syncFieldEditing reconciles a field's editing state with its cursor position:
// for non-bare fields, the free-form input is focused exactly while the cursor
// sits on the freeFormIndex row, and blurred (persisting text) when it leaves.
func (m *Model) syncFieldEditing(f *askQuestionState) {
	if f.q.Type == "free-response" {
		return // bare field: editing is managed at focus/blur boundaries
	}
	onFree := f.rowAt(f.cursor) == freeFormIndex
	if onFree && !f.editing {
		f.editing = true
		m.setAskTextEditing(f, true)
	} else if !onFree && f.editing {
		f.editing = false
		m.setAskTextEditing(f, false)
	}
}

// leaveField persists, clears and blurs the free-form input if a field is
// editing, so tabbing/advancing never strands the widget focused on a hidden
// field - and the next page's input starts empty (the shared widget would
// otherwise inherit this field's text).
func (m *Model) leaveField(f *askQuestionState) {
	if f.editing {
		m.setAskTextEditing(f, false)
		f.editing = false
		if m.components.askTextInputReady {
			m.components.askTextInput.SetValue("")
		}
	}
}

// focusActiveField reconciles the newly-active field's editing state with its
// cursor. A bare free-response field is always (re)focused and reloaded from
// its stored text - a field that was never entered keeps editing=true from
// construction, so a plain `if !f.editing` guard would leave the shared widget
// unfocused and drop typed keys. For option fields, a cursor sitting on the
// free-form row re-focuses the input.
func (m *Model) focusActiveField() {
	f := &m.askState.fields[m.askState.active]
	if f.q.Type == "free-response" {
		f.editing = true
		m.setAskTextEditing(f, true)
		return
	}
	if f.rowAt(f.cursor) == freeFormIndex && !f.editing {
		f.editing = true
		m.setAskTextEditing(f, true)
	} else if f.editing {
		f.editing = false
		m.setAskTextEditing(f, false)
	}
}

// advanceFromField commits the current field and moves to the next question (or
// the confirm phase on the last one), then focuses the successor as needed.
func (m *Model) advanceFromField(f *askQuestionState) {
	a := m.askState
	m.leaveField(f)
	if a.active < len(a.fields)-1 {
		a.active++
		m.focusActiveField()
	} else {
		a.enterConfirm()
	}
}

func (m *Model) processAskAnswers(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a := m.askState
	f := &a.fields[a.active]

	// Navigation between questions (one question is shown at a time). Text stays
	// in the field state; the input re-syncs from it on re-entry.
	switch msg.String() {
	case "tab":
		m.leaveField(f)
		if a.active < len(a.fields)-1 {
			a.active++
			m.focusActiveField()
		} else {
			a.enterConfirm()
		}
		return m, nil
	case "shift+tab":
		m.leaveField(f)
		if a.active > 0 {
			a.active--
			m.focusActiveField()
		}
		return m, nil
	}

	switch msg.String() {
	case "up", "down":
		if f.q.Type == "free-response" {
			return m, nil // bare field: arrows do nothing
		}
		dir := -1
		if msg.String() == "down" {
			dir = 1
		}
		f.moveCursor(dir)
		m.syncFieldEditing(f)
		return m, nil

	case "space":
		// Space is typed text while the free-form field is active; on option
		// rows it does nothing (enter selects/toggles).
		if f.editing {
			return m.processAskText(msg)
		}
		return m, nil

	case "enter":
		if f.q.Type == "free-response" {
			// Bare field: submit the typed text and advance.
			m.advanceFromField(f)
			return m, nil
		}
		if f.editing {
			if f.isMulti {
				// Multi-select free-form row: enter just toggles it, like any
				// other option - it never advances. The user can check and
				// uncheck the text entry as often as they like; tab (or the Next
				// row) moves on.
				if f.picked[freeFormIndex] {
					delete(f.picked, freeFormIndex)
				} else {
					f.picked[freeFormIndex] = true
				}
			} else {
				// Single-choice: commit the typed text as this field's answer
				// (superseding any earlier pick) and advance. An empty commit
				// leaves the prior pick intact; a later non-empty commit wins.
				f.freeCommitted = true
				m.advanceFromField(f)
			}
			return m, nil
		}

		rowVal := f.rowAt(f.cursor)
		switch {
		case rowVal == freeFormIndex:
			// Defensive: landing here auto-activates editing (syncFieldEditing).
			f.editing = true
			m.setAskTextEditing(f, true)
			return m, nil
		case rowVal == nextIndex:
			m.advanceFromField(f)
			return m, nil
		default:
			if rowVal < 0 || rowVal >= len(f.q.Options) {
				return m, nil
			}
			if f.isMulti {
				// Multi-select: enter toggles the highlighted row.
				f.picked[rowVal] = !f.picked[rowVal]
				return m, nil
			}
			// Single-choice: select the highlighted row and advance. The last
			// explicit action wins: picking an option supersedes any committed
			// free-form answer (answer AND display), so clear the commit.
			for k := range f.picked {
				delete(f.picked, k)
			}
			f.picked[rowVal] = true
			f.freeCommitted = false
			m.advanceFromField(f)
			return m, nil
		}
	}

	// Text entry: everything else (printables, left/right/home/end, backspace,
	// delete) belongs to the input itself while editing.
	if f.editing {
		return m.processAskText(msg)
	}
	return m, nil
}

func (m *Model) processAskConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a := m.askState

	switch msg.String() {
	case "up", "down", "k":
		a.confirmSelected = 1 - a.confirmSelected
		return m, nil
	case "y":
		m.submitAsk()
		return m, nil
	case "n", "shift+tab":
		a.goBackFromConfirm()
		m.focusActiveField()
		return m, nil
	case "enter":
		if a.confirmSelected == 0 {
			m.submitAsk()
		} else {
			a.goBackFromConfirm()
			m.focusActiveField()
		}
		return m, nil
	}

	return m, nil
}

func (m *Model) submitAsk() {
	a := m.askState
	if a.fields[a.active].editing && m.components.askTextInputReady {
		a.fields[a.active].text = m.components.askTextInput.Value()
	}
	agent.ResolveAsk(a.request.ID, a.collectAnswers())
	m.closeAsk()
}

