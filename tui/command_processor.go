package tui

import (
	"clankerv2/agent"
	"clankerv2/data"
	"fmt"
	"io"
	"log"
	"regexp"
	"sort"
	"strings"

	"charm.land/bubbles/v2/list"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var (
	itemSelected = lipgloss.NewStyle().Foreground(lipgloss.White).Background(lipgloss.Color("#333"))
	defaultItem  = lipgloss.NewStyle().Foreground(lipgloss.Color("#ccc"))
)

type itemDelegate struct{}

func (d itemDelegate) Height() int                             { return 1 }
func (d itemDelegate) Spacing() int                            { return 0 }
func (d itemDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }
func (d itemDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	i, ok := listItem.(modelItem)
	if !ok {
		return
	}

	if index == m.Index() {
		fmt.Fprint(w, itemSelected.Render("> "+i.Title()))
	} else {
		fmt.Fprint(w, defaultItem.Render("  "+i.Title()))
	}
}

// staticCommandItems returns the static slash-command item list WITHOUT
// consulting commandRegistry — used by buildAutocompleteItemsForConfig so the
// reload handler (itself in commandRegistry) cannot recurse through it.
func staticCommandItems() []CommandDefinition {
	return []CommandDefinition{
		{Key: "/context"}, {Key: "/reasoning"}, {Key: "/model"}, {Key: "/session"},
		{Key: "/rerun"}, {Key: "/config"}, {Key: "/stealth"}, {Key: "/subagents"},
		{Key: "/log"}, {Key: "/discover"}, {Key: "/compact"}, {Key: "/reload-skills"},
		{Key: "/exit"},
	}
}

// buildAutocompleteItems assembles the slash-command list shown while typing:
// every static registry entry plus one item per loaded skill.
func buildAutocompleteItems(cfg *data.AppConfig) []list.Item {
	return buildAutocompleteItemsForConfig(cfg)
}

// buildAutocompleteItemsForConfig builds the autocomplete item list for cfg
// from staticCommandItems + skill names — never consulting commandRegistry
// directly (the registry contains handlers that call this function).
func buildAutocompleteItemsForConfig(cfg *data.AppConfig) []list.Item {
	items := make([]list.Item, 0)
	for _, item := range staticCommandItems() {
		items = append(items, modelItem{name: item.Key, data: item.Key})
	}
	if cfg != nil {
		for _, s := range cfg.Skills {
			items = append(items, modelItem{name: "/" + s.Name, data: s.Name})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].(modelItem).name < items[j].(modelItem).name
	})
	return items
}

// filterCommandItems returns the autocomplete items whose name is a prefix of
// the first whitespace-delimited token of the typed command. Matching on just
// the first token (not the whole line) keeps a command listed and highlighted
// while its arguments are being typed — e.g. "/jira-get_ticket SYC-1234" still
// selects /jira-get_ticket so Enter can run it with the params. An empty or
// bare "/" query matches every item.
func filterCommandItems(val string, items []list.Item) []list.Item {
	token := strings.TrimPrefix(val, "/")
	if i := strings.IndexAny(token, " \t"); i >= 0 {
		token = token[:i]
	}
	var filtered []list.Item
	for _, it := range items {
		name := strings.TrimPrefix(it.(modelItem).Title(), "/")
		if token == "" || strings.HasPrefix(name, token) {
			filtered = append(filtered, it)
		}
	}
	return filtered
}

// handleCommand routes a submitted slash command: the static registry is
// tried first; any unmatched /name [args...] that names a loaded skill runs it
// (see runSkillCommand). Unknown commands fall through unchanged.
func (m *Model) handleCommand(cmd string) (tea.Model, tea.Cmd) {
	cmd = strings.TrimSpace(cmd)

	for _, def := range commandRegistry {
		match := false
		if def.IsRegex {
			re, err := regexp.Compile(def.Pattern)
			if err == nil && re.MatchString(cmd) {
				match = true
			}
		} else {
			if cmd == def.Pattern {
				match = true
			}
		}

		if match {
			return def.Handler(m)
		}
	}

	// Dynamic skill commands: /<skill-name> [args...] where the name is a loaded
	// skill's frontmatter name. Args are everything after the first space.
	name, args := splitSkillCommand(cmd)
	if name != "" {
		return m.runSkillCommand(name, args)
	}

	// Unknown command: just return without action
	return m, nil
}

// splitSkillCommand splits a raw "/name [args...]" line into (name, args).
// It returns ("", "") when there is no name token at all.
func splitSkillCommand(cmd string) (string, string) {
	if !strings.HasPrefix(cmd, "/") || len(cmd) < 2 {
		return "", ""
	}
	raw := strings.TrimPrefix(cmd, "/")
	name, args, _ := strings.Cut(raw, " ")
	if name == "" {
		return "", ""
	}
	return name, strings.TrimSpace(args)
}

// runSkillCommand runs a loaded skill on the currently selected model. The
// user's extra text after the command name is forwarded as the skill's args.
// subagent:true skills launch via agent.RunSkillTask (background subagent +
// main-agent collection turn); everything else runs INLINE — the full SKILL.md
// workflow is injected as an agent turn in this conversation.
func (m *Model) runSkillCommand(name, args string) (tea.Model, tea.Cmd) {
	cfg := agent.GetAppConfig()
	var skill *data.Skill
	if cfg != nil {
		skill = cfg.GetSkill(name)
	}
	if skill == nil {
		log.Printf("unknown command or skill: /%s", name)
		return m, nil
	}

	// Live re-read: SKILL.md on disk is authoritative at invocation time.
	live, err := data.LoadSkillFromDisk(skill)
	if err != nil {
		m.ResponseChan <- data.Response{Type: "console", Message: fmt.Sprintf("/%s: failed to read SKILL.md: %v", skill.Name, err)}
		return m, nil
	}
	skill = live

	m.components.input.Reset()
	m.listMode = listModeNone
	model := m.state.selectedModel

	if skill.RunsAsSubagent() {
		m.ResponseChan <- data.Response{Type: "console", Message: fmt.Sprintf("/%s: launching skill subagent on %s (esc cancels the main-agent collection step)", skill.Name, model)}
		cancelFn := agent.RunSkillTask(m.ResponseChan, skill, args, model)
		m.cancelFn = &cancelFn
		return m, nil
	}

	// Inline: announce via a console line and inject the workflow as an agent
	// turn. The long SKILL.md body is NOT echoed as a user message so it does
	// not flood the feed.
	m.ResponseChan <- data.Response{Type: "console", Message: fmt.Sprintf("/%s: running inline on %s (esc cancels)", skill.Name, model)}
	cancelFn := agent.PromptWithContext(nil, m.ResponseChan, skill.SkillInlinePrompt(args), model)
	m.cancelFn = &cancelFn
	return m, nil
}

func (m *Model) runRerunCommand() (tea.Model, tea.Cmd) {
	m.components.input.Reset()
	m.listMode = listModeNone

	cancelFn := agent.PromptWithContext(nil, m.ResponseChan, "", m.state.selectedModel)
	m.cancelFn = &cancelFn
	return m, nil
}

func (m *Model) runSessionCommand() (tea.Model, tea.Cmd) {
	sessions, err := data.ListSessions()

	if err != nil || len(sessions) == 0 {
		log.Println("No sessions available or failed to fetch.")
		return m, nil
	}

	items := make([]list.Item, len(sessions)+1)
	items[0] = modelItem{"New Session", nil}
	for i, session := range sessions {
		display := fmt.Sprintf("%d %s %s", session.ID, session.InsertedAt.Format("2006-01-02"), strings.ReplaceAll(session.Name, "\n", " "))
		items[i+1] = modelItem{display, session}
	}

	m.setList("Select Session", items)
	m.components.listCallback = func(i modelItem) {
		if i.data == nil {
			agent.SetSession(nil, nil)
			m.SetSession(nil, nil)
		} else {
			session := i.data.(data.Session)
			messages, _ := data.LoadSession(&session)
			// TODO: don't duplicate this bullshiz
			agent.SetSession(&session, messages)
			m.SetSession(&session, messages)

			if len(messages) > 0 {
				lastModel := messages[len(messages)-1].Model
				cfg := agent.GetAppConfig()

				for _, model := range cfg.Models {
					if model.ID == lastModel {
						m.state.selectedModel = model.ID
					}
				}
			}
		}
	}

	return m, nil
}

func (m *Model) runModelCommand() (tea.Model, tea.Cmd) {
	cfg := agent.GetAppConfig()
	models := cfg.Models

	if len(models) == 0 {
		log.Println("No models available or failed to fetch.")
		return m, nil
	}

	items := make([]list.Item, len(models))
	for i, mod := range models {
		display := fmt.Sprintf("%s (%s)", mod.Name, mod.Executor)
		items[i] = modelItem{display, mod}
	}

	m.setList("Select Model", items)
	m.components.listCallback = func(i modelItem) {
		if i.data != nil {
			if mod, ok := i.data.(data.ModelConfig); ok {
				m.state.selectedModel = mod.ID
			}
		}
	}

	return m, nil
}

// runContextCommand presents the supported context window sizes as a
// selectable list (same pattern as /model) for the current session.
func (m *Model) runContextCommand() (tea.Model, tea.Cmd) {
	current := agent.GetContextSize()
	items := make([]list.Item, 0, len(data.ContextSizeOptions))
	for _, size := range data.ContextSizeOptions {
		label := data.FormatContextSize(size)
		if size == data.DefaultContextSize {
			label += " (default)"
		}

		prefix := "  "
		if size == current {
			prefix = "• " // Marks the active selection inside the list
		}
		items = append(items, modelItem{prefix + label, size})
	}

	m.setList("Select Context Size", items)
	m.components.listCallback = func(i modelItem) {
		size, ok := i.data.(int)
		if !ok {
			return
		}

		persisted, err := agent.SetSessionContextSize(size)
		if err != nil {
			m.ResponseChan <- data.Response{Type: "console", Message: fmt.Sprintf("Failed to set context size: %v", err)}
			return
		}

		msg := fmt.Sprintf("Context size set to %s", data.FormatContextSize(size))
		if !persisted {
			msg += " (in-memory only: no active session)"
		}
		m.ResponseChan <- data.Response{Type: "console", Message: msg}
	}

	return m, nil
}

// runReasoningCommand presents the current model's selectable reasoning levels
// (none, then each configured option) as a list, same pattern as /context.
// When the model has no enabled reasoning block it just reports that the level
// cannot be configured.
func (m *Model) runReasoningCommand() (tea.Model, tea.Cmd) {
	cfg := agent.GetAppConfig()
	model := m.state.selectedModel
	mc := cfg.GetModelByID(model)

	if mc == nil || !mc.ReasoningIsConfigurable() {
		m.ResponseChan <- data.Response{Type: "console", Message: "Reasoning effort cannot be configured for this model"}
		return m, nil
	}

	current := agent.GetReasoningLevelForModel(model)
	levels := mc.ReasoningSelectableLevels()
	items := make([]list.Item, 0, len(levels))
	for _, lvl := range levels {
		prefix := "  "
		if lvl == current {
			prefix = "\u2022 " // Marks the active selection inside the list
		}
		items = append(items, modelItem{prefix + lvl, lvl})
	}

	m.setList("Select Reasoning Effort", items)
	m.components.listCallback = func(i modelItem) {
		lvl, ok := i.data.(string)
		if !ok {
			return
		}
		persisted, err := agent.SetReasoningEffort(model, lvl)
		if err != nil {
			m.ResponseChan <- data.Response{Type: "console", Message: fmt.Sprintf("Failed to set reasoning effort: %v", err)}
			return
		}
		msg := "Reasoning effort set to " + lvl
		if !persisted {
			msg += " (in-memory only: no active session)"
		}
		m.ResponseChan <- data.Response{Type: "console", Message: msg}
	}

	return m, nil
}

func (m *Model) runConfigCommand() (tea.Model, tea.Cmd) {
	// TODO: Implement config command
	return m, nil
}

func (m *Model) runExitCommand() (tea.Model, tea.Cmd) {
	return m, tea.Quit
}

func (m *Model) runStealthCommand() (tea.Model, tea.Cmd) {
	m.components.input.Reset()
	m.listMode = listModeNone

	isStealth := agent.GetStealth()
	agent.SetStealth(!isStealth)

	m.ResponseChan <- data.Response{Type: "console", Message: fmt.Sprintf("Stealth mode set to %t", !isStealth)}

	return m, nil
}

// runSubagentsCommand toggles the live subagent grid (one bordered viewport per
// non-terminal feed) over the main viewport; running it again (or ESC) returns
// to the previous view. Entering from the log view leaves it for good.
func (m *Model) runSubagentsCommand() (tea.Model, tea.Cmd) {
	m.components.input.Reset()
	m.listMode = listModeNone

	rows := agent.ListSubagents("") // all subagents (TUI has one caller: main)
	if len(rows) == 0 {
		m.ResponseChan <- data.Response{Type: "console", Message: "No subagents. The main agent can spawn them with system-spawn_subagent."}
		return m, nil
	}

	// Entering the grid from the log view leaves it for good (the grid becomes
	// the previous view).
	if !m.subagentView && m.logView {
		m.exitLogView()
	}

	if m.subagentView {
		m.subagentView = false
		return m, nil
	}
	m.subagentView = true
	return m, nil
}

// runDiscoverCommand kicks off a /discover run: the agent package scans the
// project tree, fans out read-only summarizer subagents per chunk, and hands
// the MAIN agent a task to collect the summaries and merge them into the marked
// section of .clanker/project.md. It streams through ResponseChan like any turn
// (same pattern as /rerun), so esc cancels the main-agent part mid-flight.
func (m *Model) runDiscoverCommand() (tea.Model, tea.Cmd) {
	m.components.input.Reset()
	m.listMode = listModeNone
	model := m.state.selectedModel

	m.ResponseChan <- data.Response{Type: "console", Message: fmt.Sprintf("/discover: scanning project and launching chunk subagents on %s (esc cancels the merge step)", model)}

	cancelFn := agent.RunDiscoverTask(m.ResponseChan, model)
	m.cancelFn = &cancelFn
	return m, nil
}

// runCompactCommand kicks off a manual context compaction on the main
// runtime (same flow as the model's system-compact tool): the agent seeds the
// compaction prompt, the model writes its summary via
// system-register_compaction, and the run stops so flow returns to the user.
// It streams through ResponseChan like any turn, so esc cancels mid-flight.
func (m *Model) runCompactCommand() (tea.Model, tea.Cmd) {
	m.components.input.Reset()
	m.listMode = listModeNone
	model := m.state.selectedModel

	m.ResponseChan <- data.Response{Type: "console", Message: "/compact: compacting session context into a summary (esc cancels)"}

	cancelFn := agent.RunManualCompaction(m.ResponseChan, model)
	m.cancelFn = &cancelFn
	return m, nil
}


// runReloadSkillsCommand re-scans both config layers, swaps the loaded skill
// set (including the system-skills tool schema), refreshes the main
// runtime's tool set in place, and rebuilds the autocomplete list.
func (m *Model) runReloadSkillsCommand() (tea.Model, tea.Cmd) {
	m.components.input.Reset()
	m.listMode = listModeNone

	res := agent.ReloadSkills()
	if res == nil {
		m.ResponseChan <- data.Response{Type: "console", Message: "/reload-skills: no config loaded"}
		return m, nil
	}

	cfg := agent.GetAppConfig()
	m.autocompleteItems = buildAutocompleteItemsForConfig(cfg)

	msg := fmt.Sprintf("/reload-skills: %d skills loaded", res.Total)
	if len(res.Added) > 0 {
		msg += fmt.Sprintf(" (+%s)", strings.Join(res.Added, ", "))
	}
	if len(res.Removed) > 0 {
		msg += fmt.Sprintf(" (-%s)", strings.Join(res.Removed, ", "))
	}
	if len(res.Updated) > 0 {
		msg += fmt.Sprintf(" (~%s)", strings.Join(res.Updated, ", "))
	}
	m.ResponseChan <- data.Response{Type: "console", Message: msg}

	if cfg != nil {
		for _, line := range cfg.SkillDiagnostics {
			m.ResponseChan <- data.Response{Type: "console", Message: line}
		}
	}
	return m, nil
}

func (m *Model) setList(title string, items []list.Item) {
	itemCount := len(items)
	maxHeight := 15
	padding := 4
	showPagination := true

	if itemCount+padding < maxHeight {
		padding -= 1
		showPagination = false
	}
	listHeight := min(itemCount+padding, maxHeight)

	m.components.inputList = list.New(items, itemDelegate{}, m.components.width-2, listHeight)
	m.components.inputList.Title = title
	m.components.inputList.SetShowTitle(true)
	m.components.inputList.SetShowStatusBar(false)
	m.components.inputList.SetShowHelp(false)
	m.components.inputList.SetShowPagination(showPagination)

	m.listMode = listModeSelect
	m.components.input.Reset()
}
