package tui

import (
	"strconv"
	"strings"
	"testing"

	clankerv2data "clankerv2/data"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// TestAskFormRenderNoWrapHeader verifies render invariants for the system-ask
// form across terminal widths: every rendered line fits within the terminal
// (no wrapping — this is what caught the free-form input overflow), the
// "Agent Information Request x/y" progress header is present, and exactly one
// option row is painted with the active-highlight background (zero in bare
// text-entry mode).
func TestAskFormRenderNoWrapHeader(t *testing.T) {
	for _, termW := range []int{100, 80, 60, 40} {
		m := NewModel("dev")
		m.handleResize(tea.WindowSizeMsg{Width: termW, Height: 30})

		req := clankerv2data.AskUserRequest{ID: "ask-1", Questions: []clankerv2data.AskUserQuestion{
			{
				ID:     "q1",
				Type:   "select",
				Prompt: "Which virtual machine does Elixir run on?",
				Options: []clankerv2data.AskOption{
					{Label: "BEAM VM", Value: "beam"},
					{Label: "JVM", Value: "jvm"},
				},
			},
			{
				ID:     "q2",
				Type:   "multi-select",
				Prompt: "Features?",
				Options: []clankerv2data.AskOption{
					{Label: "Pattern matching", Value: "pm"},
					{Label: "Mix build tool", Value: "mix"},
					{Label: "GOTO statements", Value: "goto"},
				},
			},
			{
				ID:          "q3",
				Type:        "free-response",
				Prompt:      "Describe one thing you'd want to build with Elixir, and why.",
				Placeholder: "e.g., A real-time chat server using Phoenix Channels...",
			},
		}}
		m.handleAskRequest(req)

		// The free-form input is sized so its inline row (margin + arrow
		// prefix + SetWidth+1 view columns) is exactly the box's inner width.
		if got := m.components.askTextInput.Width(); got != askInputWidth(termW, false) {
			t.Fatalf("termW=%d: input width %d, want %d", termW, got, askInputWidth(termW, false))
		}

		for qidx := range m.askState.fields {
			m.askState.active = qidx
			f := &m.askState.fields[m.askState.active]
			if f.editing && m.components.askTextInputReady {
				m.setAskTextEditing(f, true)
			}
			panel := m.renderAskForm()
			plines := strings.Split(panel, "\n")

			// The top border and one padding row precede the content, so the
			// progress header is a few lines in; search the whole panel.
			wantHeader := "Agent Information Request " + strconv.Itoa(qidx+1) + "/3"
			found := false
			for _, l := range plines {
				if strings.Contains(l, wantHeader) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("termW=%d q%d: missing header %q in panel", termW, qidx+1, wantHeader)
			}

			for i, line := range plines {
				if w := lipgloss.Width(line); w > termW {
					t.Fatalf("termW=%d q%d: line %d wraps (width %d)", termW, qidx+1, i, w)
				}
			}

			// Exactly the active option row is painted with the highlight
			// background. In bare text-entry mode (free-response) there is no
			// option row, so no line carries a background at all.
			bgLines := 0
			for _, l := range plines {
				if strings.Contains(l, "\x1b[4") {
					bgLines++
				}
			}
			expectedBG := 0
			if !(f.editing && !f.hasFree) {
				expectedBG = 1 // one highlighted (cursor) option row
			}
			if bgLines != expectedBG {
				t.Fatalf("termW=%d q%d: %d highlighted row(s), want %d", termW, qidx+1, bgLines, expectedBG)
			}
		}

		m.askState.enterConfirm()
		conf := m.renderAskConfirm()
		// The confirm header is wider than a 40-col box, so at narrow widths
		// lipgloss wraps it into consecutive lines; both fragments must appear.
		if !strings.Contains(conf, "Agent Information Request - Confirm") ||
			!strings.Contains(conf, "3 Answers") {
			t.Fatalf("termW=%d: confirm phase missing header in:\n%s", termW, conf)
		}
		for i, line := range strings.Split(conf, "\n") {
			if w := lipgloss.Width(line); w > termW {
				t.Fatalf("termW=%d confirm: line %d wraps (width %d)", termW, i, w)
			}
		}

		m.closeAsk()
	}
}
