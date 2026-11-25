package tui

import (
	"clankerv2/agent"
	"clankerv2/data"
	"log"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// processInput handles keys in the default (no overlay) mode. Returning a nil
// model passes the key through to the textarea for normal editing.
func (m *Model) processInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// Log view, then subagent grid each own ESC before a final press
		// cancels a running main run.
		if m.logView {
			m.exitLogView()
			return m, nil
		}
		if m.subagentView {
			m.subagentView = false
			return m, nil
		}
		if m.cancelFn != nil {
			(*m.cancelFn)()
			m.state.state = stateIdle
			m.cancelFn = nil
		}
		// A cancelled run discards its queued interjections when the agent's
		// goroutine ends; dropping them here too keeps the statusbar honest
		// immediately (and any stragglers are covered by that deferred discard).
		agent.MainRuntime().DiscardQueuedMessages()
		// A cancelled run may leave tool entries with no incoming summary.
		m.clearPendingTools()
		return m, nil

	case "ctrl+c":
		return m, tea.Quit

	case "enter":
		if m.state.state == stateIdle {
			userText := m.components.input.Value()
			if strings.HasPrefix(userText, "/") {
				return m.handleCommand(userText)
			}
		}

	case "pgup":
		m.components.activeViewport.PageUp()
		if m.components.activeViewport == &m.components.mainViewport {
			m.syncFeedMode()
		}
		return m, nil

	case "pgdown":
		m.components.activeViewport.PageDown()
		if m.components.activeViewport == &m.components.mainViewport {
			m.syncFeedMode()
		}
		return m, nil

	case "ctrl+j":
		userText := m.components.input.Value()
		// A pending system-ask form owns the interaction; never start a second
		// agent run from a stale or half-typed line underneath it.
		if m.listMode == listModeAsk || m.listMode == listModePermission {
			return m, nil
		}
		if strings.TrimSpace(userText) == "" {
			return m, nil
		}

		// While a run is in flight the text is QUEUED on the main runtime: the
		// turn loop picks it up at the next cycle boundary (interjection). Idle
		// starts a normal run; commands only make sense then.
		if m.state.state != stateIdle {
			if agent.EnqueueMainUserMessage(userText) {
				m.components.input.Reset()
			}
			return m, nil
		}

		if strings.HasPrefix(userText, "/") {
			return m.handleCommand(userText)
		}

		m.clearPendingTools()
		m.ResponseChan <- data.Response{Type: "user", Message: userText}
		log.Println(userText)
		m.components.input.Reset()

		cancelFn := agent.PromptWithContext(nil, m.ResponseChan, userText, m.state.selectedModel)
		m.cancelFn = &cancelFn
		return m, nil
	}

	return nil, nil
}

// runLogCommand toggles the /log viewer over the main viewport; running it
// again (or ESC) returns to the previous view — the subagent grid when it was
// active, otherwise main.
func (m *Model) runLogCommand() (tea.Model, tea.Cmd) {
	m.components.input.Reset()
	m.listMode = listModeNone

	if m.logView {
		m.exitLogView()
		return m, nil
	}
	// The grid is suspended while the log view owns the screen; logSavedSubagent
	// restores it on exit.
	m.logSavedSubagent = m.subagentView
	if m.subagentView {
		m.subagentView = false
	}
	m.logView = true
	// Make the log viewport active so wheel/pgup route to it, and seed it
	// immediately (follow starts at the tail) so the toggle does not flash empty.
	m.components.activeViewport = &m.components.logViewport
	m.components.logViewport.GotoBottom()
	m.refreshLogViewport()
	return m, nil
}

// exitLogView leaves the log view, restoring any subagent grid that was
// suspended on entry.
func (m *Model) exitLogView() {
	m.logView = false
	m.subagentView = m.logSavedSubagent
	m.logSavedSubagent = false
	if m.components.activeViewport == &m.components.logViewport {
		m.components.activeViewport = &m.components.mainViewport
	}
	m.syncFeedMode()
}

// restoreOverlaySource picks the active viewport for when an overlay
// (permission/ask) closes: logView and subagentView persist untouched while
// overlays are open (suppressed only via listMode in View).
func (m *Model) restoreOverlaySource() {
	if m.logView {
		m.components.activeViewport = &m.components.logViewport
	} else {
		m.components.activeViewport = &m.components.mainViewport
		// Resuming the grid: a captured main scroll position is moot while it
		// stays hidden, and applyLayoutViewport is skipped for grid frames —
		// drop the stale one-shot so it cannot fire on a later return to main.
		if m.subagentView {
			m.state.mainResumePending = false
		}
	}
}
func (m *Model) processPermission(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up":
		if m.permissionSelectedIndex == 1 {
			m.permissionSelectedIndex = 0
		}
		return m, nil
	case "down":
		if m.permissionSelectedIndex == 0 {
			m.permissionSelectedIndex = 1
		}
		return m, nil
	case "enter":
		allowed := m.permissionSelectedIndex == 0
		agent.ResolvePermission(m.permissionPrompt.ID, allowed)
		m.permissionPrompt = nil
		m.listMode = listModeNone
		m.restoreOverlaySource()
		m.components.input.Focus()
		return m, nil
	case "y", "ctrl+y":
		agent.ResolvePermission(m.permissionPrompt.ID, true)
		m.permissionPrompt = nil
		m.listMode = listModeNone
		m.restoreOverlaySource()
		m.components.input.Focus()
		return m, nil
	case "n", "esc":
		agent.ResolvePermission(m.permissionPrompt.ID, false)
		m.permissionPrompt = nil
		m.listMode = listModeNone
		m.restoreOverlaySource()
		m.components.input.Focus()
		return m, nil
	}
	return m, nil
}

// processAutocomplete handles keys while the slash-command autocomplete list is
// visible.
func (m Model) processAutocomplete(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	val := m.components.input.Value()

	switch msg.String() {
	case "up", "down":
		m.autocompleteNavActive = true
		m.components.inputList, cmd = m.components.inputList.Update(msg)
		return m, cmd

	case "enter", "ctrl+j":
		if m.autocompleteNavActive || len(m.components.inputList.Items()) > 0 {
			// Run the SELECTED command, carrying any args already typed beyond
			// its name. Because the filter matches on the first token only (see
			// filterCommandItems), "/jira-get_ticket SYC-1234" still highlights
			// /jira-get_ticket while params are entered, so this branch is
			// reached and the params are preserved here.
			item := m.components.inputList.SelectedItem().(modelItem)
			typed := strings.TrimSpace(m.components.input.Value())
			argPart := ""
			if len(typed) > len(item.name) && strings.HasPrefix(typed, item.name) {
				argPart = strings.TrimSpace(typed[len(item.name):])
			}
			m.components.input.SetValue(item.name)
			if argPart != "" {
				return m.handleCommand(item.name + " " + argPart)
			}
			return m.handleCommand(item.name)
		}
		// Fall through to normal submit if no navigation occurred
		return m.processInput(msg)

	case "tab":
		// Tab expands the selected command's name into the input (cursor at
		// end) without running it, so params can be appended and Enter'd.
		if len(m.components.inputList.Items()) > 0 {
			item := m.components.inputList.SelectedItem().(modelItem)
			m.components.input.SetValue(item.name)
			m.autocompleteNavActive = false
		}
		return m, nil

	case "esc":
		m.listMode = listModeNone
		m.autocompleteNavActive = false
		return m, nil

	default:
		// Update textarea first to capture new character
		m.components.input, cmd = m.components.input.Update(msg)
		val = m.components.input.Value()

		if !strings.HasPrefix(val, "/") {
			// Left command mode
			m.listMode = listModeNone
			m.autocompleteNavActive = false
		} else {
			// Filter autocomplete list (matches on the first token only so a
			// command stays highlighted while its params are being typed).
			filtered := filterCommandItems(val, m.autocompleteItems)
			m.components.inputList.SetItems(filtered)
			if len(filtered) != 0 {
				m.components.inputList.SetHeight(min(len(filtered)+1, 10))
				m.components.inputList.Select(0)
			} else {
				m.components.inputList.SetHeight(2)
			}
			m.autocompleteNavActive = false // Reset nav state on new keystroke
		}
		return m, cmd
	}
}

// processList handles keys in the legacy select-mode list; it returns a nil
// model for unhandled keys so the caller falls back to the list widget.
func (m Model) processList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.components.listCallback(m.components.inputList.SelectedItem().(modelItem))
		m.listMode = listModeNone
		m.components.input.Focus()
		return m, nil
	case "esc":
		m.listMode = listModeNone
		m.components.input.Focus()
		return m, nil
	}
	return nil, nil
}
