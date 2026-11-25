package tui

import (
	"clankerv2/agent"
	"clankerv2/data"
	"context"
	"log"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type HeaderMsg struct {
	Version string
}

func headerCommand(v string) tea.Cmd {
	return func() tea.Msg { return HeaderMsg{Version: v} }
}

type modelItem struct {
	name string
	data any
}

func (i modelItem) Title() string       { return i.name }
func (i modelItem) Description() string { return "desc" }
func (i modelItem) FilterValue() string { return i.name }

type tickMsg struct{}

type ChatMessage struct {
	Role    string
	Content string
	// ToolOK records success for Role "tool" messages emitted by the live agent
	// loop. It is nil for session rows loaded from the DB, where the stored
	// content is the full envelope (re-parsed in renderHistory).
	ToolOK *bool
	// ToolArgs carries the tool call's JSON parameters for display; live tool
	// messages populate it via the agent summary.
	ToolArgs map[string]any
	// ToolPreview carries a bounded preview of the tool result for display.
	// For live calls this is already the tool's self-summary (ResultPreview).
	ToolPreview string
	// ToolSummary carries the tool row's one-line self-summary for DB-loaded
	// sessions: messages.summary when present, otherwise backfilled from the
	// stored envelope by agent.StoredToolRowSummary. Empty only for
	// unparseable legacy rows, which fall back to a clipped raw result.
	ToolSummary string
}

// pendingToolCall tracks a tool invocation that has been announced on the
// response channel ("tool_start") but has not yet produced its summary. These
// render in the dedicated panel between the viewport and the input block.
type pendingToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
	StartedAt time.Time
}

type InputMode int

type tuiPermissionResponse struct {
	ID      string
	Allowed bool
}

const (
	modePrompt  InputMode = iota
	modeCommand           // Starts with '/'
	modeShell             // Future: shell commands
)

type AgentState string

// Run states, driven exclusively by explicit data.StateMsg events from the
// agent (plus idle on user cancel). The status bar renders whatever the last
// emitted state was — no inference from message types.
const (
	stateIdle      AgentState = "idle"
	statePosting   AgentState = "posting"
	stateIngest    AgentState = "ingest"
	stateError     AgentState = "error"
	stateThinking  AgentState = "thinking"
	stateActive    AgentState = "active"
	stateParsing   AgentState = "parsing"
	stateExecuting AgentState = "executing"
	stateWaiting   AgentState = "waiting"
)

type ListMode int

const (
	listModeNone         ListMode = iota
	listModeSelect                // Traditional select mode
	listModeAutocomplete          // Claude-style autocomplete
	listModePermission            // Permission prompt overlay
	listModeAsk                   // system-ask form: bottom panel swaps the input
)

type State struct {
	contentsUpdatedAt time.Time
	viewUpdatedAt     time.Time
	renderedContent   string
	// renderedLines is the offscreen full-history buffer split into display
	// lines (the same content as renderedContent). Live-mode viewport updates
	// are served from a window taken off its tail, so SetContent stays cheap
	// no matter how long the session gets.
	renderedLines []string
	// viewContent is the string currently loaded in the main viewport: either
	// the full history (scrolled back) or the two-screen live window.
	viewContent string
	// feedScrolledBack tracks whether the live feed is in reduced-rate mode
	// because the user has scrolled away from the bottom.
	feedScrolledBack bool
	// mainOffsetAtDiff / feedScrolledBackAtDiff capture the main viewport's
	// position on the first frame of the diff (permission) overlay so View can
	// resume exactly there when returning to main (see applyLayoutViewport in
	// view.go).
	mainOffsetAtDiff       int
	feedScrolledBackAtDiff bool
	// mainResumePending arms the one-shot scroll resume on return from the
	// diff overlay (consumed by restoreMainScroll so later plain frames leave
	// the user's subsequent scrolling alone).
	mainResumePending bool
	// logWrap incrementally wraps the in-memory global log into display lines
	// for /log; see logWrapState in viewport.go.
	logWrap logWrapState

	history       []ChatMessage
	selectedModel string
	state         AgentState
	startTime     time.Time
	lastUsage     *data.Usage // Tracks usage of the most recent turn across prompts
}

type Components struct {
	mainViewport       viewport.Model
	permissionViewport viewport.Model
	// logViewport backs the /log full-screen view: it shows (and streams) the
	// in-memory global log via agent.SessionLog().Contents().
	logViewport    viewport.Model
	activeViewport *viewport.Model
	// lastActiveWasMain mirrors activeViewport one frame behind; applyLayout
	// uses it to detect the first frame of each main<->diff switch.
	lastActiveWasMain bool
	input             textarea.Model
	// askTextInput is the free-form input element for system-ask forms.
	// Single-line: it owns the value and caret; enter submits (see tui/ask.go).
	// askTextInputReady is true once the first WindowSizeMsg has created it.
	askTextInput      textinput.Model
	askTextInputReady bool
	inputList         list.Model
	listCallback      func(modelItem)
	width             int

	height int
	// subViewports is a fixed pool backing the /subagents live grid (up to four
	// cells at once); layoutSubagentGrid sizes and fills them.
	subViewports [4]viewport.Model
}

// inactiveViewports are the non-active full-screen viewports, used to keep
// their geometry current while they are off screen (see applyLayoutViewport).
func (c *Components) inactiveViewports() []*viewport.Model {
	var out []*viewport.Model
	for _, vp := range []*viewport.Model{&c.mainViewport, &c.permissionViewport, &c.logViewport} {
		if vp != c.activeViewport {
			out = append(out, vp)
		}
	}
	return out
}

type Model struct {
	ResponseChan chan any

	program          *tea.Program
	cancelFn         *context.CancelFunc
	permissionPrompt *data.PermissionRequest

	listMode              ListMode
	autocompleteNavActive bool
	autocompleteItems     []list.Item
	askState              *AskState

	// envDir/envBranch are the working directory and git branch shown at the
	// right of the status line (before the time). Fetched once at init — both
	// are stable for the process lifetime. Empty strings hide their item
	// (git lookup failed / not a repository).
	envDir    string
	envBranch string

	state                   *State
	components              *Components
	pendingToolCalls        []pendingToolCall
	ready                   bool
	permissionSelectedIndex int
	version                 string

	// subagentView toggles the /subagents live grid over the main viewport.
	subagentView bool
	// logView toggles the /log full-screen log viewer over the main viewport;
	// entering it saves subagentView so /log-off restores the previous view.
	logView           bool
	logSavedSubagent  bool
	subagentFeeds     []*subagentFeed
	subagentGridCells []subCellLayout
	subagentGridMore  int
}

func NewModel(version string) Model {
	ti := textarea.New()
	// While a run is in flight ctrl+j does not start a second run — it queues
	// the text for injection at the next cycle boundary (see processInput).
	ti.Placeholder = "ctrl+c to quit, ctrl+j to submit (queues while running)"
	ti.DynamicHeight = true
	ti.ShowLineNumbers = false
	ti.MinHeight = 1
	ti.MaxHeight = 20
	ti.MaxContentHeight = 1000
	ti.Prompt = ""

	styles := ti.Styles()
	styles.Focused.Base = styles.Focused.Base.Background(lipgloss.NoColor{})
	styles.Focused.CursorLine = styles.Focused.CursorLine.Background(lipgloss.NoColor{})
	ti.SetStyles(styles)
	ti.SetVirtualCursor(false)
	ti.Focus()

	autocompleteItems := buildAutocompleteItems(agent.GetAppConfig())

	// Resolve the initial active model: prefer system.defaultModel when it
	// exists in config; otherwise fall back to the first available model
	// (mirrors the Wails frontend startup behavior).
	selectedModel := ""
	if cfg := agent.GetAppConfig(); cfg != nil {
		if def := cfg.GetDefaultModel(); def != "" && cfg.GetModelByID(def) != nil {
			selectedModel = def
		} else if len(cfg.Models) > 0 {
			selectedModel = cfg.Models[0].ID
		}
	}

	// Buffered so the agent goroutine never deadlocks on a send while the TUI
	// is busy (e.g. blocked inside system-ask). The single FIFO listener keeps
	// ordering intact regardless of the buffer size. Sizing: a /discover run
	// emits one SubagentEvent per chunk before the first drain, so the buffer
	// must comfortably exceed the number of chunks a typical project produces.
	responseChan := make(chan any, 256)

	// Skill loading happens at config load — before this channel exists — so its
	// diagnostics (malformed files, collisions) are recorded on the config and
	// flushed here as console lines: they render immediately at start-up.
	if cfg := agent.GetAppConfig(); cfg != nil {
		for _, line := range cfg.SkillDiagnostics {
			responseChan <- data.Response{Type: "console", Message: line}
		}
	}

	// Subagent lifecycle events, streamed content, tool activity, and blocking
	// requests forwarded from subagents land on ResponseChan. Stamped events
	// (AgentID set) are routed to per-subagent feeds by Update; permission/ask
	// requests flow through the same handlers as the main agent's, so a user can
	// still approve writes or answer questions raised by a running subagent. The
	// send is non-blocking (drop + log) so a stalled UI can never wedge a
	// background run's relay goroutine.
	agent.SetUISink(func(ev any) {
		select {
		case responseChan <- ev:
		default:
			log.Println("[subagent] TUI response channel full; dropped event")
		}
	})

	return Model{
		ready:             false,
		ResponseChan:      responseChan,
		listMode:          listModeNone,
		autocompleteItems: autocompleteItems,
		pendingToolCalls:  make([]pendingToolCall, 0),
		envDir:            data.WorkingDirectory(),
		envBranch:         data.GitBranch(),
		components: &Components{
			input: ti,
		},
		state: &State{
			history:           []ChatMessage{},
			state:             stateIdle,
			selectedModel:     selectedModel,
			contentsUpdatedAt: time.Time{},
			viewUpdatedAt:     time.Time{},
			startTime:         time.Now(),
		},
		version: version,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		// textarea.Blink,
		tea.Tick(feedTickInterval(false), func(t time.Time) tea.Msg { return tickMsg{} }),
		headerCommand(m.version),
		listenForResponse(m.ResponseChan),
	)
}

func listenForResponse(ch chan any) tea.Cmd {
	return func() tea.Msg {
		msg := <-ch // Block until something arrives on the channel
		return msg  // This becomes the 'msg' passed into Update()
	}
}

func (m *Model) SetSession(session *data.Session, messages []data.Message) {
	m.state.lastUsage = nil // Clear previous session's token counts on load
	// The FULL history renders: rows before the latest compaction seed were
	// compacted away from the MODEL's context only — they stay in the session
	// DB and remain display-only here (the runtime payload still cuts at the
	// seed; see data.CompactionCutIndex / agent SetSession).
	history := make([]ChatMessage, 0, len(messages))
	for _, message := range messages {
		var entry ChatMessage
		switch message.Source {
		case "user":
			log.Println("Loading", message.Contents)
			entry = ChatMessage{Role: "user", Content: message.Contents}
		case "assistant":
			if message.Contents != "" {
				log.Println("Loading", message.Contents)
			}
			entry = ChatMessage{Role: "response", Content: message.Contents}
		case "tool":
			log.Println("Loading tool message:", truncateDisplay(message.Contents, 120))
			// Stored rows keep the full envelope; renderHistory re-parses it
			// (name + params + success). ToolSummary carries the one-line
			// self-summary — messages.summary or a runtime backfill from the
			// stored result — so loaded sessions render like live calls.
			entry = ChatMessage{Role: "tool", Content: message.Contents, ToolOK: nil,
				ToolSummary: agent.StoredToolRowSummary(message)}
		}

		history = append(history, entry)
		// m.processIncomingToolCall(message.Contents)
	}

	m.state.history = history
	m.pendingToolCalls = m.pendingToolCalls[:0]
	m.renderHistory()
}

func (m *Model) clearPendingTools() {
	m.pendingToolCalls = m.pendingToolCalls[:0]
}

func (m *Model) processIncomingToolCall(summary data.ToolCallSummary) {
	m.removePendingTool(summary.ID)

	success := summary.Success
	m.state.history = append(m.state.history, ChatMessage{
		Role:        "tool",
		Content:     summary.Name,
		ToolOK:      &success,
		ToolArgs:    summary.Arguments,
		ToolPreview: summary.ResultPreview,
	})
	m.renderHistory()
}

func (m *Model) removePendingTool(id string) {
	for i, tc := range m.pendingToolCalls {
		if tc.ID == id {
			m.pendingToolCalls = append(m.pendingToolCalls[:i], m.pendingToolCalls[i+1:]...)
			return
		}
	}
}

func (m *Model) BindProgram(program *tea.Program) {
	m.program = program
}
