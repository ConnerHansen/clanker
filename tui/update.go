package tui

import (
	"clankerv2/data"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case data.SubagentEvent:
		return m.handleAgentMessage(msg)
	case data.PermissionRequest:
		return m.handleAgentMessage(msg)
	case data.AskUserRequest:
		return m.handleAgentMessage(msg)
	case data.ToolStart:
		return m.handleAgentMessage(msg)
	case data.StateMsg:
		return m.handleAgentMessage(msg)
	case data.ToolCallSummary:
		return m.handleAgentMessage(msg)
	case data.Response:
		return m.handleAgentMessage(msg)
	case data.Usage:
		return m.handleAgentMessage(msg)

	case HeaderMsg:
		header := styleSystemHeaderText.Render("Clanker")
		author := styleSystemHeaderVersionText.Render("  ├ authored by Conner Hansen (connerhansen@gmail.com)")
		version := styleSystemHeaderVersionText.Render("  └ v" + msg.Version)
		headerBlock := lipgloss.NewStyle().
			PaddingLeft(2).
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(lipgloss.Color("#50748e")).
			MarginLeft(2).
			Render(strings.Join([]string{header, author, version}, "\n"))
		clankerBlock := `             ██▀▀▀    ██▀▀▀        
             ██▄▄▄    ██▄▄▄        
     ██▀▀██████████████████        
     ██▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃█     
     ▀▀▀▀▀▀▀▀▀██▀▀▀██▀▀▀▀▀▀▀▀▀     
              ██   ██             
         ▆▆▆▆▆██   ██▆▆▆▆▆         `

		m.state.history = append(m.state.history, ChatMessage{
			Role:    "system",
			Content: lipgloss.JoinHorizontal(lipgloss.Center, styleSystemHeaderText.Render(clankerBlock), headerBlock),
		})
		m.renderHistory()

	case tickMsg:
		return m.handleTick()
	case tea.WindowSizeMsg:
		return m.handleResize(msg)
	case tea.MouseWheelMsg:
		return m.handleMouseWheel(msg)
	case tea.KeyMsg:
		return m.handleKeyEvent(msg)

	case tea.PasteMsg:
		switch m.listMode {
		case listModeNone:
			// Normal prompt input owns the paste.
			var updateCmd tea.Cmd
			m.components.input, updateCmd = m.components.input.Update(msg)
			return m, updateCmd
		case listModeAsk:
			// Paste into an active free-form answer field (single-line only).
			model, cmd := m.processAsk(msg)
			return model, cmd
		}
	}

	return m, nil
}

func (m Model) handleAgentMessage(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case data.StateMsg:
		// Stamped events belong to a subagent feed; only main-agent state
		// updates the status bar.
		if v.AgentID != "" {
			if f := m.feedByID(v.AgentID); f != nil {
				f.state = AgentState(v.State)
			}
			return m, listenForResponse(m.ResponseChan)
		}
		m.state.state = AgentState(v.State)

	case data.PermissionRequest:
		// Stamped (subagent) requests flow through the same overlay as the main
		// agent's; the prompt labels which subagent is asking (see
		// renderPermissionPrompt).
		m.permissionPrompt = &v

		// Only requests WITH a content payload open the alt viewport (diff /
		// assembled command). A bare label-only request keeps the main viewport
		// active — no full-screen swap for nothing to show.
		if v.Viewport != nil && v.Viewport.Content != "" {
			m.components.activeViewport = &m.components.permissionViewport

			content := lipgloss.JoinVertical(lipgloss.Top,
				styleDiffHeader.Render(permissionViewportTitle(&v)),
				styleResponse.Render(v.Viewport.Content),
			)

			m.components.permissionViewport.SetContent(content)
		}

		m.listMode = listModePermission
		m.permissionSelectedIndex = 0
		m.state.state = stateWaiting

	case data.AskUserRequest:
		m.handleAskRequest(v)

	case data.ToolStart:
		if v.AgentID != "" {
			if f := m.feedByID(v.AgentID); f != nil {
				f.addPending(pendingToolCall{ID: v.ID, Name: v.Name, Arguments: v.Arguments, StartedAt: time.Now()})
			}
			return m, listenForResponse(m.ResponseChan)
		}
		m.pendingToolCalls = append(m.pendingToolCalls, pendingToolCall{
			ID:        v.ID,
			Name:      v.Name,
			Arguments: v.Arguments,
			StartedAt: time.Now(),
		})

	case data.ToolCallSummary:
		if v.AgentID != "" {
			if f := m.feedByID(v.AgentID); f != nil {
				f.removePending(v.ID)
				m.feedAddTool(f, v.Success, v.Name, v.Arguments, v.ParamsPreview, v.ResultPreview)
			}
			return m, listenForResponse(m.ResponseChan)
		}
		m.processIncomingToolCall(v)

	case data.SubagentEvent:
		m.upsertSubagentFeed(v)

	case data.Response:
		// Stamped (subagent) stream content goes to that agent's feed; the main
		// history only ever receives events from the interactive run.
		if v.AgentID != "" {
			if f := m.feedByID(v.AgentID); f != nil {
				switch v.Type {
				case "response":
					f.appendText("response", v.Message)
				case "debug":
					f.appendText("thinking", v.Message)
				case "console":
					f.appendText("console", v.Message)
				}
			}
			return m, listenForResponse(m.ResponseChan)
		}

		// A context compaction registered: the seed summary arrives on its own
		// event (Type "compaction", the framed user message that re-seeds the
		// session). Append it as a plain user bubble — visually identical to how
		// the persisted seed row renders on reload. Handled before the generic
		// switch so it neither merges into adjacent history entries nor disturbs
		// pending-tool bookkeeping mid-run.
		if v.Type == "compaction" {
			m.state.history = append(m.state.history, ChatMessage{Role: "user", Content: v.Message})
			m.renderHistory()
			return m, listenForResponse(m.ResponseChan)
		}

		role := "user"
		switch v.Type {
		case "debug":
			role = "thinking"
		case "console":
			role = "console"
		case "response":
			role = "response"
		default:
			if len(m.pendingToolCalls) > 0 {
				m.clearPendingTools()
			}
		}

		if v.Message != "" {
			if len(m.state.history) > 0 && m.state.history[len(m.state.history)-1].Role == role {
				if role == "console" {
					m.state.history[len(m.state.history)-1].Content += "\n" + v.Message
				} else {
					m.state.history[len(m.state.history)-1].Content += v.Message
				}
			} else {
				m.state.history = append(m.state.history, ChatMessage{Role: role, Content: v.Message})
			}
		}

		m.renderHistory()
	case data.Usage:
		m.handleUsageMessage(v)
	}

	return m, listenForResponse(m.ResponseChan)
}

// permissionViewportTitle resolves the alt-viewport header: a tool-provided
// title wins; otherwise it derives from the content kind (diff -> "Diff
// Contents", script -> "Command").
func permissionViewportTitle(req *data.PermissionRequest) string {
	if req.Viewport == nil {
		return ""
	}
	if req.Viewport.Title != "" {
		return req.Viewport.Title
	}
	switch req.Viewport.Kind {
	case "script":
		return "Command"
	default:
		return "Diff Contents"
	}
}

func (m Model) handleUsageMessage(usage data.Usage) {
	m.state.lastUsage = &usage
}

func (m Model) handleTick() (tea.Model, tea.Cmd) {
	// Reconcile the feed mode first so a transition happens immediately when
	// the user scrolls back to the bottom (this push then re-anchors it with
	// GotoBottom), or leaves the bottom (syncFeedMode already loaded the full
	// history and remapped the offset on the way in).
	m.syncFeedMode()

	// Drop finished subagent feeds past their linger window; this also
	// auto-closes the live grid when nothing remains active.
	m.pruneSubagentFeeds()

	// Tick cadence follows what is actually on screen: an open overlay or a
	// back-scrolled surface gets the reduced rate; live tails run at full rate.
	scrolledBack := m.listMode != listModeNone
	if !scrolledBack {
		switch {
		case m.logView && !m.subagentView:
			scrolledBack = !m.components.logViewport.AtBottom()
		default: // main feed on screen (directly or behind the grid): unchanged semantics
			scrolledBack = m.state.feedScrolledBack
		}
	}

	timestamp := m.state.contentsUpdatedAt
	if timestamp.After(m.state.viewUpdatedAt) || m.state.viewUpdatedAt.IsZero() {
		// Live mode (at bottom, nothing over the viewport) serves the two-screen
		// window; otherwise we push the full history at reduced rate, leaving
		// the offset untouched so the user's position stays stable as the
		// document grows beneath it.
		content := m.state.viewContent
		if scrolledBack {
			content = m.state.renderedContent
		}
		m.components.mainViewport.SetContent(content)
		if !scrolledBack {
			m.components.mainViewport.GotoBottom()
		}
		m.state.viewUpdatedAt = timestamp
	}

	if m.logView && m.listMode == listModeNone && !m.subagentView {
		m.refreshLogViewport()
	}

	return m, tea.Tick(feedTickInterval(scrolledBack), func(t time.Time) tea.Msg { return tickMsg{} })
}

func (m Model) handleResize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.components.width = msg.Width
	m.components.height = msg.Height
	verticalMargin := 2

	if !m.ready {
		m.components.mainViewport = viewport.New(
			viewport.WithWidth(msg.Width),
			viewport.WithHeight(msg.Height-verticalMargin),
		)
		m.components.permissionViewport = viewport.New(
			viewport.WithWidth(msg.Width),
			viewport.WithHeight(msg.Height-verticalMargin),
		)
		m.components.logViewport = viewport.New(
			viewport.WithWidth(msg.Width),
			viewport.WithHeight(msg.Height-verticalMargin),
		)
		m.components.mainViewport.MouseWheelEnabled = true
		m.components.mainViewport.MouseWheelDelta = 6
		m.components.permissionViewport.MouseWheelEnabled = true
		m.components.permissionViewport.MouseWheelDelta = 6
		m.components.logViewport.MouseWheelEnabled = true
		m.components.logViewport.MouseWheelDelta = 6
		m.components.activeViewport = &m.components.mainViewport

		// Fixed pool backing the /subagents live grid; sized per-frame by
		// layoutSubagentGrid while the grid is on screen.
		for i := range m.components.subViewports {
			m.components.subViewports[i] = viewport.New(
				viewport.WithWidth(msg.Width),
				viewport.WithHeight(msg.Height-verticalMargin),
			)
		}

		// Free-form input element for system-ask text questions: single-line,
		// owns its own value and caret, enter submits (see tui/ask.go).
		at := textinput.New()
		at.Prompt = ""
		at.SetVirtualCursor(false)
		m.components.askTextInput = at
		m.components.askTextInputReady = true

		m.ready = true
	} else {
		m.components.mainViewport.SetWidth(msg.Width)
		m.components.mainViewport.SetHeight(msg.Height - verticalMargin)
		m.components.permissionViewport.SetWidth(msg.Width)
		m.components.permissionViewport.SetHeight(msg.Height - verticalMargin)
		m.components.logViewport.SetWidth(msg.Width)
		m.components.logViewport.SetHeight(msg.Height - verticalMargin)
	}

	styleUser = styleUser.Width(msg.Width)
	styleThinking = styleThinking.Width(msg.Width)
	styleResponse = styleResponse.Width(msg.Width)
	styleToolSuccessTitle = styleToolSuccessTitle.Width(msg.Width)
	styleToolSuccess = styleToolSuccess.Width(msg.Width)
	styleToolFailureTitle = styleToolFailureTitle.Width(msg.Width)
	styleToolFailure = styleToolFailure.Width(msg.Width)
	styleConsole = styleConsole.Width(msg.Width)
	styleDiffHeader = styleDiffHeader.Width(msg.Width)

	m.components.input.SetWidth(msg.Width - 2)
	if m.components.askTextInputReady {
		isMulti := false
		if m.askState != nil && len(m.askState.fields) > 0 {
			isMulti = m.askState.fields[m.askState.active].isMulti
		}
		m.components.askTextInput.SetWidth(askInputWidth(msg.Width, isMulti))
	}

	m.renderHistory()
	return m, nil
}

func (m Model) handleMouseWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	updatedVp, cmd := m.components.activeViewport.Update(msg)
	switch m.components.activeViewport {
	case &m.components.mainViewport:
		m.components.mainViewport = updatedVp
		// Scroll input changes the feed mode immediately (1hz when scrolled
		// back, 10hz at the bottom) instead of waiting for the next tick.
		m.syncFeedMode()
	case &m.components.logViewport:
		// Scrolling off the bottom freezes follow; returning re-arms it on the
		// next log tick (see refreshLogViewport).
		m.components.logViewport = updatedVp
	case &m.components.permissionViewport:
		m.components.permissionViewport = updatedVp
	}
	return m, cmd
}

func (m Model) handleKeyEvent(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.listMode {
	case listModeAutocomplete:
		model, cmd := m.processAutocomplete(msg)
		return model, cmd
	case listModeSelect:
		model, cmd := m.processList(msg)
		if model != nil {
			return model, cmd
		}
		m.components.inputList, cmd = m.components.inputList.Update(msg)
		return m, cmd
	case listModePermission:
		model, cmd := m.processPermission(msg)
		return model, cmd
	case listModeAsk:
		model, cmd := m.processAsk(msg)
		return model, cmd
	default:
		// Process normal input first
		model, cmd := m.processInput(msg)
		if model != nil {
			return model, cmd
		}
		m.components.input, cmd = m.components.input.Update(msg)

		// Trigger autocomplete if input now starts with "/"
		if m.listMode == listModeNone && strings.HasPrefix(m.components.input.Value(), "/") {
			m.listMode = listModeAutocomplete
			listHeight := len(m.autocompleteItems)
			if listHeight > 10 {
				listHeight = 10
			} else if listHeight < 4 {
				listHeight = 4
			}
			m.components.inputList = list.New(m.autocompleteItems, itemDelegate{}, m.components.width-2, listHeight)
			m.components.inputList.SetShowTitle(false)
			m.components.inputList.SetShowStatusBar(false)
			m.components.inputList.SetShowHelp(false)
			m.components.inputList.SetShowPagination(false)
			m.components.inputList.Select(0)
		}
		return m, cmd
	}
}

