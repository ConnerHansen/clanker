package tui

import (
	tea "charm.land/bubbletea/v2"
)

type CommandHandler func(m *Model) (tea.Model, tea.Cmd)

type CommandDefinition struct {
	Key      string
	Pattern  string
	IsRegex  bool
	Handler  CommandHandler
	HelpText string
}

var commandRegistry = []CommandDefinition{
	{Key: "/context", Pattern: "/context", Handler: (*Model).runContextCommand, HelpText: "Select session context size"},
	{Key: "/reasoning", Pattern: "/reasoning", Handler: (*Model).runReasoningCommand, HelpText: "Select reasoning effort for the current model"},
	{Key: "/model", Pattern: "/model", Handler: (*Model).runModelCommand, HelpText: "List and select models"},
	{Key: "/session", Pattern: "/session", Handler: (*Model).runSessionCommand, HelpText: "List and select sessions"},
	{Key: "/rerun", Pattern: "/rerun", Handler: (*Model).runRerunCommand, HelpText: "Rerun the last prompt"},
	{Key: "/config", Pattern: `^/config(\s+.*)?$`, IsRegex: true, Handler: (*Model).runConfigCommand, HelpText: "Configure settings"},
	{Key: "/stealth", Pattern: "/stealth", Handler: (*Model).runStealthCommand, HelpText: "Toggle stealth mode (no DB writes)"},
	{Key: "/subagents", Pattern: "/subagents", Handler: (*Model).runSubagentsCommand, HelpText: "List subagents (status, activity, output)"},
	{Key: "/log", Pattern: "/log", Handler: (*Model).runLogCommand, HelpText: "Toggle the live log view (esc closes)"},
	{Key: "/discover", Pattern: "/discover", Handler: (*Model).runDiscoverCommand, HelpText: "Map the project structure into .clanker/project.md via subagents"},
	{Key: "/compact", Pattern: "/compact", Handler: (*Model).runCompactCommand, HelpText: "Compact session context into a summary (manual compaction)"},
	{Key: "/reload-skills", Pattern: "/reload-skills", Handler: (*Model).runReloadSkillsCommand, HelpText: "Reload skills from disk (adds/removes/updates)"},
	{Key: "/exit", Pattern: "/exit", Handler: (*Model).runExitCommand, HelpText: "Gracefully exit the application"},
}
