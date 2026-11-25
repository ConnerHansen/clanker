package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// askInputWidth is the SetWidth for the shared free-form input given the
// terminal width and the kind of row it will render in. textinput.View() always
// emits SetWidth+1 columns (it reserves a cursor slot), and the widest row it
// renders in is the inline free-form row: prefix + the view, where the prefix
// already carries the 2-col margin (4 cols for a multi box+gap; for a
// single-choice arrow the committed " ✓ " slot totals 7 - the governing case,
// since the uncommitted row's 1-col marker slot is narrower). The ask box's
// inner width is W-4, so size the input so that line is exactly W-4; anything
// wider makes the terminal wrap.
func askInputWidth(termW int, isMulti bool) int {
	if isMulti {
		return termW - 9 // prefix(4) + (SetWidth+1) = W-4
	}
	return termW - 12 // prefix(7) + (SetWidth+1) = W-4
}

// askRow builds one real-option row, preserving the fixed column layout:
// 2-col margin, then an arrow-slot + • marker for single-choice or a □/■
// box for multi-select, then the label. When isCursor is true every run carries
// the highlight background (prefix, marker/box, spacer, label) so the active
// row reads as one solid bar with no plain gaps; the caller pads it to innerW.
func askRow(isMulti, picked, isCursor bool, label string) string {
	margin := "  "
	if isCursor {
		margin = styleAskSelected.Render("  ")
	}

	var line string
	toggled := fmt.Sprintf(" %s ", checkPrefix)
	if !isMulti {
		arrow := "  "
		marker := styleAskOption.Render(" ")
		lab := styleAskOption.Render(label)
		if picked {
			marker = styleAskChecked.Render(toggled)
			lab = styleAskChecked.Render(label)
		}
		if isCursor {
			// Paint the whole row with the fill (arrow, marker, label).
			arrow = styleAskSelOption.Render(selectedPrefix)
			lab = styleAskSelOption.Render(label)
			if picked {
				marker = styleAskSelChecked.Render(toggled)
				lab = styleAskSelChecked.Render(label)
			} else {
				marker = styleAskSelOption.Render(" ")
			}
		}
		line = margin + arrow + marker + lab
	} else {
		box := multiUnselectedPrefix
		gap := " "
		lab := styleAskOption.Render(label)
		if picked {
			box = styleAskChecked.Render(multiSelectedPrefix)
			lab = styleAskChecked.Render(label)
		}
		if isCursor {
			// Paint the whole row with the fill (box, spacer, label).
			gap = styleAskSelected.Render(" ")
			lab = styleAskSelOption.Render(label)
			if picked {
				box = styleAskSelChecked.Render(multiSelectedPrefix)
				lab = styleAskChecked.Render(label)
			} else {
				box = styleAskSelected.Render(multiUnselectedPrefix)
			}
		}
		line = margin + box + gap + lab
	}
	return line
}

// renderAskForm renders the single active question (or the confirm summary).
// Only one question is displayed at a time; tab/shift+tab move between them,
// so even long forms never need scrolling.
func (m *Model) renderAskForm() string {
	if m.askState.phase == askPhaseConfirm {
		return m.renderAskConfirm()
	}

	a := m.askState
	num := fmt.Sprintf("Agent Information Request %d/%d", a.active+1, len(a.fields))
	f := &a.fields[a.active]

	lines := make([]string, 0, 9)
	lines = append(lines, styleAskHeader.Render(num))
	lines = append(lines, styleAskPrompt.Render(f.q.Prompt))
	if f.q.Context != "" {
		lines = append(lines, "  "+styleAskContext.Render(f.q.Context))
	}

	if f.q.Type == "free-response" && f.editing && m.components.askTextInputReady {
		// Bare free-response: a lone single-line text input; enter submits.
		a.inputContentLine = len(lines)
		a.inputColOffset = 2 + lipgloss.Width("  ") // box pad (2) + 2-col indent
		lines = append(lines, "  "+m.components.askTextInput.View())
	} else {
		// Inner width of the box (no side borders; left/right padding of 2)
		// so the active-row highlight spans exactly the content area.
		innerW := m.components.width - 4

		positions, hiddenRows := f.windowedPositions()
		// When a single-choice field has committed free-form text it supersedes
		// any picked option: options render unchecked and the free-form row
		// carries the check (mirrors collectAnswers).
		freeWins := !f.isMulti && f.freeCommitted && f.hasFreeText()
		pickedFor := func(i int) bool {
			if freeWins {
				return false
			}
			return f.picked[i]
		}
		for _, pos := range positions {
			rowVal := f.rowAt(pos)
			isCursor := pos == f.cursor

			if rowVal == nextIndex {
				// A blank separator line precedes the "Next (Tab)" row.
				lines = append(lines, "")
			}

			var content string
			switch rowVal {
			case freeFormIndex:
				if isCursor && f.editing && m.components.askTextInputReady {
					// Inline free-form entry: the row's own prefix + the real input.
					// Multi-select keeps the □/■ box (like every other row) so the
					// checked state stays visible while typing; single-choice keeps
					// the arrow plus a 1-col marker slot, widened to " ✓ " only when
					// committed text wins - so uncommitted input starts where the
					// static row's label does.
					prefix := styleAskSelected.Render("  ")
					if f.isMulti {
						box := multiUnselectedPrefix
						if f.picked[freeFormIndex] {
							box = styleAskChecked.Render(multiSelectedPrefix)
						}
						prefix += box + styleAskSelected.Render(" ")
					} else {
						prefix += styleAskSelOption.Render(selectedPrefix)
						if f.freeCommitted && f.hasFreeText() {
							// Committed text wins: the 3-col " ✓ " slot matches the
							// static row, input starts one column right of option labels.
							prefix += styleAskSelChecked.Render(fmt.Sprintf(" %s ", checkPrefix))
						} else {
							// Uncommitted: 1-col marker slot so the placeholder/typed text
							// sits exactly where the static row's label does.
							prefix += styleAskSelected.Render(" ")
						}
					}
					a.inputContentLine = len(lines)
					a.inputColOffset = 2 + lipgloss.Width(prefix) // box pad (2) + prefix
					lines = append(lines, prefix+m.components.askTextInput.View())
					continue
				}
				// Free-form row. Multi-select renders it like any other option
				// (□/■ box — selected only when the row is toggled on); a
				// single-choice row shows an arrow, and a check bumping the label
				// over a column when committed text supersedes the options.
				if f.isMulti {
					content = askRow(true, f.picked[freeFormIndex], isCursor, f.freeRowLabel(f.picked[freeFormIndex], isCursor))
				} else if freeWins {
					margin := "  "
					arrow, marker, lab := "  ", " ", f.freeRowLabel(true, false)
					if isCursor {
						margin = styleAskSelected.Render("  ")
						arrow = styleAskSelOption.Render(selectedPrefix)
						marker = styleAskSelChecked.Render(fmt.Sprintf(" %s ", checkPrefix))
						lab = f.freeRowLabel(true, true)
					} else {
						marker = styleAskChecked.Render(fmt.Sprintf(" %s ", checkPrefix))
					}
					content = margin + arrow + marker + lab
				} else {
					margin := "  "
					arrow, marker, lab := "  ", " ", f.freeRowLabel(false, false)
					if isCursor {
						margin = styleAskSelected.Render("  ")
						arrow = styleAskSelOption.Render(selectedPrefix)
						marker = styleAskSelOption.Render("  ")
						lab = f.freeRowLabel(false, true)
					}
					content = margin + arrow + marker + lab
				}
			case nextIndex:
				if isCursor {
					content = styleAskSelected.Render("  ") + styleAskSelOption.Render(selectedPrefix+"Next (Tab)")
				} else {
					content = "  " + styleAskOption.Render(selectedPrefix+"Next (Tab)")
				}
			default:
				content = askRow(f.isMulti, pickedFor(rowVal), isCursor, f.q.Options[rowVal].Label)
			}

			if isCursor {
				// Extend the highlight bar to the full inner width with an
				// explicit background-painted spacer (deterministic, no gaps).
				if vw := lipgloss.Width(content); vw < innerW {
					content += styleAskSelected.Render(strings.Repeat(" ", innerW-vw))
				}
			}
			lines = append(lines, content)
		}

		if hiddenRows > 0 {
			lines = append(lines, "  "+styleAskHint.Render(fmt.Sprintf("…+%d more (↑↓ to scroll)", hiddenRows)))
		}
	}

	hint := m.askHintFor(*f)
	if hint != "" {
		lines = append(lines, "  "+styleAskHint.Render(hint))
	}

	return styleAskBox.Width(m.components.width).Render(strings.Join(lines, "\n"))
}

// askMaxVisibleRows caps how many option rows a single question renders before
// the rest collapse into an "…+N more" footer. The cursor still moves through
// the full list; the window simply follows it.
const askMaxVisibleRows = 6

// windowedPositions returns the contiguous slice of cursor positions to render
// (centered on the active cursor) plus a count of hidden rows. Positions map to
// row values via rowAt; sentinel rows are included in the count so they scroll.
func (f *askQuestionState) windowedPositions() (positions []int, hidden int) {
	total := len(f.navRows())
	if total == 0 {
		return nil, 0
	}
	all := make([]int, total)
	for i := range all {
		all[i] = i
	}
	if total <= askMaxVisibleRows {
		return all, 0
	}

	start := f.cursor - askMaxVisibleRows/2
	if start < 0 {
		start = 0
	}
	end := start + askMaxVisibleRows
	if end > total {
		end = total
	}
	if end-start < askMaxVisibleRows && end == total {
		// Anchor to the bottom when the cursor sits near the end of the list.
		start = end - askMaxVisibleRows
	}
	return all[start:end], total - (end - start)
}

// askHintFor builds the footer hint line for the active question.
func (m *Model) askHintFor(f askQuestionState) string {
	isLast := m.askState.active == len(m.askState.fields)-1
	var parts []string

	switch {
	case f.editing && f.q.Type == "free-response":
		parts = append(parts, "type your answer")
	case f.editing:
		if f.isMulti {
			parts = append(parts, "↑↓: back to options • enter: toggle • tab: advance")
		} else {
			parts = append(parts, "↑↓: back to options • enter: use typed answer")
		}
	case f.q.Type == "multi-select":
		parts = append(parts, "↑↓: move • enter: toggle")
	default: // select / confirm
		parts = append(parts, "↑↓: move • enter: select")
	}

	if !isLast {
		parts = append(parts, "tab: next • shift+tab: back")
	} else if !f.editing {
		if f.q.Type == "multi-select" {
			// Enter toggles; tab reaches review.
			parts = append(parts, "tab: review answers")
		} else {
			parts = append(parts, "enter: review answers (tab ignored)")
		}
	}

	return strings.Join(parts, " • ")
}

// renderAskConfirm shows the assembled answers plus the yes/no prompt.
func (m *Model) renderAskConfirm() string {
	a := m.askState

	lines := []string{
		styleAskHeader.Render(fmt.Sprintf("Agent Information Request - Confirm %d Answers", len(a.fields))),
		"Ready to submit responses?",
		"",
	}

	for i, f := range a.fields {
		label := fmt.Sprintf("%d. %s", i+1, truncateDisplay(f.q.Prompt, 60))
		val := styleAskAnswer.Render("→ " + f.currentAnswer())
		lines = append(lines, label+"   "+val)
	}

	lines = append(lines, "")

	yesStyle := styleAskYes
	noStyle := styleAskNo
	yesPrefix, noPrefix := "  ", "  "
	if a.confirmSelected == 0 {
		yesPrefix = selectedPrefix
		yesStyle = yesStyle.Background(styleAskSelected.GetBackground())
		noStyle = noStyle.Foreground(lipgloss.Color("#555"))
	} else {
		noPrefix = selectedPrefix
		noStyle = noStyle.Background(styleAskSelected.GetBackground())
		yesStyle = yesStyle.Foreground(lipgloss.Color("#555"))
	}

	lines = append(lines,
		yesStyle.Width(m.components.width-4).Render(fmt.Sprintf(" %s Yes — submit [Y]", yesPrefix)),
		noStyle.Width(m.components.width-4).Render(fmt.Sprintf(" %s No — go back and edit [N]", noPrefix)),
		"",
		styleAskHint.Render("y/enter: submit • n/shift+tab: go back • esc: cancel"),
	)

	return styleAskBox.Width(m.components.width).Render(strings.Join(lines, "\n"))
}
