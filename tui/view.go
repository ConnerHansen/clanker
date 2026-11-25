package tui

import (
	"clankerv2/agent"
	"clankerv2/data"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var separator = " • "

// pendingToolMaxRows caps how many in-flight tools are listed before the rest
// collapse into a "…+N more" footer line.
const pendingToolMaxRows = 5

func (m Model) View() tea.View {
	// Dynamic input styling based on mode
	inputVal := m.components.input.Value()
	isCommand := strings.HasPrefix(inputVal, "/")
	styles := m.components.input.Styles()
	if isCommand {
		styles.Focused.Base = styles.Focused.Base.Foreground(lipgloss.Color("#FFD700"))
		styles.Focused.CursorLine = styles.Focused.CursorLine.Foreground(lipgloss.Color("#FFD700"))
		styles.Focused.Placeholder = styles.Focused.Placeholder.Foreground(lipgloss.Color("#FFD700"))
	} else {
		styles.Focused.Base = styles.Focused.Base.Foreground(lipgloss.Color("#fff"))
		styles.Focused.CursorLine = styles.Focused.CursorLine.Foreground(lipgloss.Color("#fff"))
		styles.Focused.Placeholder = styles.Focused.Placeholder.Foreground(lipgloss.Color("#777"))
	}
	m.components.input.SetStyles(styles)

	var bottomPanel string
	panelHeight := 0
	switch m.listMode {
	case listModePermission:
		if m.permissionPrompt != nil {
			bottomPanel = m.renderPermissionPrompt()
			// Measured, not fixed: long header lines wrap at terminal width
			// and push the block past its old 10-row assumption, clipping the
			// Allow/Deny rows off the bottom of the screen. Shrinking the
			// viewport by the true height keeps them on screen.
			panelHeight = lipgloss.Height(bottomPanel)
		} else {
			bottomPanel = borderNormal.Render(m.components.input.View())
			panelHeight = m.components.input.Height() + 2
		}

	case listModeSelect:
		bottomPanel = m.components.inputList.View()
		panelHeight = m.components.inputList.Height()
	case listModeAutocomplete:
		bottomPanel = lipgloss.JoinVertical(lipgloss.Left,
			m.components.inputList.View(),
			borderNormal.Render(m.components.input.View()),
		)
		panelHeight = m.components.inputList.Height() + m.components.input.Height() + 2
	case listModeAsk:
		// The ask form fully replaces the normal input block (the textarea is
		// blurred and unrendered for the lifetime of the form). Height is
		// measured from the rendered panel so multi-question forms shrink the
		// viewport automatically.
		bottomPanel = m.renderAskForm()
		panelHeight = lipgloss.Height(bottomPanel)
	default:
		bottomPanel = borderNormal.Render(m.components.input.View())
		panelHeight = m.components.input.Height() + 2
	}

	// Pending tool calls render in their own section between the viewport and
	// the input block. Empty string keeps the layout identical to before when
	// no tools are running.
	toolPanel := m.renderPendingTools()
	toolPanelHeight := 0
	if toolPanel != "" {
		toolPanelHeight = lipgloss.Height(toolPanel)
	}

	// Subagent status line (main view only): top border + one row of
	// colorized entries, hidden when no subagents are active or queued.
	subagentBar := ""
	if !m.subagentView {
		subagentBar = m.renderSubagentBar()
	}
	subagentBarHeight := 0
	if subagentBar != "" {
		subagentBarHeight = lipgloss.Height(subagentBar)
	}

	verticalMargin := 1
	rows := make([]string, 0)
	calculatedHeight := max(m.components.height-verticalMargin-panelHeight-toolPanelHeight-subagentBarHeight, 0)
	// The grid, the log view, and the main/alt viewports each own the viewport
	// region exclusively; any overlay (select, permission, ask) suppresses the
	// grid and log in favor of the activeViewport.
	gridActive := m.subagentView && m.listMode == listModeNone && len(m.activeSubagentFeeds()) > 0
	logActive := !gridActive && m.logView && m.listMode == listModeNone
	if gridActive {
		cells, more := subagentGridLayout(m.components.width, calculatedHeight, len(m.activeSubagentFeeds()))
		m.layoutSubagentGrid(cells, more)
		rows = append(rows, m.renderSubagentGrid(calculatedHeight))
	} else if logActive {
		m.applyLayoutViewport(calculatedHeight)
		rows = append(rows, m.components.logViewport.View())
	} else if m.components.activeViewport != nil {
		m.applyLayoutViewport(calculatedHeight)
		rows = append(rows, m.components.activeViewport.View())
	}

	if toolPanel != "" {
		rows = append(rows, toolPanel)
	}

	if subagentBar != "" {
		rows = append(rows, subagentBar)
	}

	rows = append(rows, bottomPanel, m.renderStatus())

	f := lipgloss.JoinVertical(lipgloss.Top, rows...)

	// Cursor placement. Each surface's element computes its own caret from
	// real content; we only add the offset to where it renders. Select and
	// permission overlays hide the cursor entirely.
	var cursor *tea.Cursor
	switch m.listMode {
	case listModeAsk:
		if f := &m.askState.fields[m.askState.active]; f.editing && m.components.askTextInputReady {
			if c := m.components.askTextInput.Cursor(); c != nil {
				// Content starts 2 rows below the panel top (top border row +
				// top padding); the input sits at content line inputContentLine.
				// The X offset is data-driven (inputColOffset): box left padding
				// (2) plus whatever prefix precedes the field — a 2-col indent for
				// bare free-response, or the arrow prefix for the inline row.
				c.Y += calculatedHeight + toolPanelHeight + subagentBarHeight + 2 + m.askState.inputContentLine
				c.X += m.askState.inputColOffset
				cursor = c
			}
		}
	case listModeSelect, listModePermission:
		// Overlays: no cursor (matches the old hide-by-nil behavior).
	default:
		if c := m.components.input.Cursor(); c != nil {
			c.Y += calculatedHeight + 1 + toolPanelHeight + subagentBarHeight
			if m.listMode == listModeAutocomplete {
				c.Y += m.components.inputList.Height()
			}
			c.X += borderNormal.GetPaddingLeft()
			cursor = c
		}
	}

	showCursor := cursor != nil

	view := tea.View{
		AltScreen:            true,
		KeyboardEnhancements: tea.KeyboardEnhancements{ReportEventTypes: true},
		Content:              f,
		MouseMode:            tea.MouseModeCellMotion,
	}
	if showCursor {
		view.Cursor = cursor
	}

	return view
}

func (m Model) renderStatus() string {
	stateString := string(m.state.state)
	var style lipgloss.Style
	switch m.state.state {
	case stateIdle:
		style = styleStatusIdle
	case stateWaiting:
		style = styleStatusWaiting
	case stateThinking:
		style = styleStatusThinking
	case stateActive:
		style = styleStatusActive
	case statePosting, stateIngest:
		style = styleStatusPosting
	case stateParsing:
		style = styleStatusParsing
	case stateExecuting:
		style = styleStatusExecuting
	case stateError:
		style = styleStatusError
	default:
		style = styleStatusActive
	}

	modelString := "no model"
	if m.state.selectedModel != "" {
		modelString = m.state.selectedModel
	}
	timeString := time.Since(m.state.startTime).Truncate(time.Second).String() + " "

	usageString := ""
	if m.state.lastUsage != nil {
		tokens := m.state.lastUsage.TotalTokens
		usageString = fmt.Sprintf("%d tokens", tokens)
	}

	// Context window active for the current session (resolves to the default
	// when unset) plus the effective reasoning level, so /context and
	// /reasoning changes are visible at a glance: [256k | xhigh].
	contextString := " [" + data.FormatContextSize(agent.GetContextSize())
	if lvl := agent.GetReasoningLevelForModel(m.state.selectedModel); lvl != "" {
		contextString += " | " + lvl
	}
	contextString += "]"

	statusParts := []string{stateString, modelString + contextString}
	if usageString != "" {
		statusParts = append(statusParts, usageString)
	}
	// Background summary sweep in flight (visible in both UIs via the
	// agent package; the TUI's tick loop re-renders this each frame).
	if agent.IsSummarizing() {
		statusParts = append(statusParts, "summarizing...")
	}
	// User messages queued for injection while a run is in flight (drained
	// at the next cycle boundary). Absent from the bar when nothing waits.
	if n := agent.MainRuntimePendingUserMessages(); n > 0 {
		statusParts = append(statusParts, fmt.Sprintf("%d pending input%s", n, plural(n)))
	}
	// Environment (working directory + git branch), fetched once at model
	// init — both are stable for the process's lifetime. The branch item is
	// hidden when the git lookup failed (not a repo, git missing).
	if m.envDir != "" {
		statusParts = append(statusParts, "🖿 "+m.envDir)
	}
	if m.envBranch != "" {
		statusParts = append(statusParts, "⎇  "+m.envBranch)
	}
	statusParts = append(statusParts, timeString)

	return style.Width(m.components.width).Render(strings.Join(statusParts, separator))
}

// plural returns the empty string for 1 and "s" otherwise (for statusbar
// counters like "1 pending input" / "3 pending inputs").
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// renderPendingTools renders the in-flight tool calls as a compact list:
// header + one line per call (name, params, elapsed seconds). Returns "" when
// there is nothing pending.
func (m Model) renderPendingTools() string {
	n := len(m.pendingToolCalls)
	if n == 0 {
		return ""
	}

	shown := m.pendingToolCalls
	more := 0
	if n > pendingToolMaxRows {
		shown = shown[:pendingToolMaxRows]
		more = n - pendingToolMaxRows
	}

	lines := []string{styleToolSuccessTitle.Render(fmt.Sprintf(" Tool (%d running)", n))}
	now := time.Now()
	for _, tc := range shown {
		line := "   " + stylePendingTool.Render("└ "+tc.Name)
		line += styleToolMeta.Render(fmt.Sprintf("%s%ds", separator, int(now.Sub(tc.StartedAt).Seconds())))
		if args := formatJSONPreview(tc.Arguments); args != "" {
			line += styleToolArgs.Render(" (" + args + ")")
		}

		lines = append(lines, line)
	}
	if more > 0 {
		lines = append(lines, styleToolMeta.Render(fmt.Sprintf("  …+%d more", more)))
	}

	return styleToolsBorder.Width(m.components.width).Render(strings.Join(lines, "\n"))
}

func (m *Model) renderPermissionPrompt() string {
	perm := m.permissionPrompt

	allowStyle := styleAskYes
	disallowStyle := styleAskNo

	if m.permissionSelectedIndex != 0 {
		allowStyle = allowStyle.Foreground(lipgloss.Color("#555"))
	}
	if m.permissionSelectedIndex != 1 {
		disallowStyle = disallowStyle.Foreground(lipgloss.Color("#555"))
	}

	allowPrefix := "  "
	if m.permissionSelectedIndex == 0 {
		allowPrefix = selectedPrefix
		allowStyle = allowStyle.Background(styleAskSelected.GetBackground())
	}
	disallowPrefix := "  "
	if m.permissionSelectedIndex == 1 {
		disallowPrefix = selectedPrefix
		disallowStyle = disallowStyle.Background(styleAskSelected.GetBackground())
	}

	// Level drives the prompt's coloration and icon: info (blue) for new
	// files, warning (gold) for overwrites/updates, danger (red) for deletes.
	color := colorInfo
	icon := "(i)"
	switch perm.Level {
	case "warning":
		color = colorWarning
		icon = "[!]"
	case "danger":
		color = colorDanger
		icon = fmt.Sprintf("[%s]", xPrefix)
	}

	// The header is the descriptor the CALLER passed (e.g. "Delete file: x.go")
	// — the tool layer decides what the operation means; we only color it.
	hdr := perm.Prompt
	if hdr == "" {
		hdr = fmt.Sprintf("Tool %s permission request", perm.Tool)
	}

	question := "Allow this tool to modify the file?"
	switch perm.Action {
	case "create":
		question = "Allow this tool to create the file?"
	case "delete":
		question = "Allow this tool to delete the file?"
	case "execute":
		question = "Allow this tool to execute the command?"
	}

	// Stamped requests carry the asking subagent's identity; show it so the
	// user knows which background run raised the prompt.
	agentLine := ""
	if perm.AgentID != "" {
		agentName := perm.AgentName
		if agentName == "" {
			agentName = perm.AgentID
		}
		agentLine = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ccc")).
			Render(fmt.Sprintf("Agent: %s (%s)", agentName, perm.AgentID))
	}

	rows := []string{
		lipgloss.NewStyle().
			Foreground(color).
			Bold(true).
			Render(fmt.Sprintf("%s %s", icon, hdr)),
		lipgloss.NewStyle().
			Foreground(lipgloss.Color("#fff")).
			Render(fmt.Sprintf("Tool: %s", perm.Tool)),
	}
	if agentLine != "" {
		rows = append(rows, agentLine)
	}
	rows = append(rows,
		lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ccc")).
			Render(fmt.Sprintf("Path: %s", perm.Path)),
		lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ccc")).
			Render(question),
		allowStyle.Width(m.components.width-4).Render(fmt.Sprintf(" %s Allow [Y]", allowPrefix)),
		disallowStyle.Width(m.components.width-4).Render(fmt.Sprintf(" %s Deny [N]", disallowPrefix)),
	)

	content := lipgloss.JoinVertical(lipgloss.Left, rows...)

	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false).
		BorderForeground(color).
		Width(m.components.width).
		Padding(1, 2).
		Render(content)
}
