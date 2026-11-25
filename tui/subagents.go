package tui

import (
	"clankerv2/agent/tools"
	"clankerv2/data"
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// subagentLinger is how long a terminal entry stays in the status bar after it
// ends, so completions/ failures are still visible before fading out.
const subagentLinger = 5 * time.Second

// Terminal status values mirror agent.SubagentStatus (kept as literals so the
// tui package does not depend on the agent's internal registry).
const (
	subStatusQueued    = "queued"
	subStatusCompleted = "completed"
	subStatusFailed    = "failed"
	subStatusKilled    = "killed"
	subStatusTimedOut  = "timed_out"
)

func isSubagentTerminal(status string) bool {
	switch status {
	case subStatusCompleted, subStatusFailed, subStatusKilled, subStatusTimedOut:
		return true
	}
	return false
}

// subagentFeed is the TUI-side record of one subagent's live output. Blocks are
// pre-styled display strings; bufText/bufRole accumulate a streamed chunk run
// until the role changes or a tool block interrupts, at which point flush()
// renders it with the feed's per-agent colors.
type subagentFeed struct {
	id            string
	name          string
	color         string // hex accent assigned at spawn; rendered via lipgloss.Color
	thinkingColor string
	status        string
	state         AgentState // refined run state from stamped StateMsg events
	startedAt     time.Time
	endedAt       time.Time

	blocks    []string
	bufText   string
	bufRole   string // "response" | "thinking" | "console"
	pending   []pendingToolCall
	updatedAt time.Time
	pushedAt  time.Time
}

// label is the short status word shown in the bar and on feed titles.
func (f *subagentFeed) label() string {
	switch f.status {
	case subStatusQueued:
		return "queued"
	case subStatusCompleted:
		return "done"
	case subStatusFailed:
		return "failed"
	case subStatusKilled:
		return "killed"
	case subStatusTimedOut:
		return "timeout"
	}
	switch f.state {
	case stateThinking:
		return "thinking"
	case stateExecuting:
		return "executing"
	case stateParsing:
		return "parsing"
	case stateWaiting:
		return "waiting"
	default:
		return "running"
	}
}

// elapsed renders the run duration ("" while queued).
func (f *subagentFeed) elapsed() string {
	if f.status == subStatusQueued || f.startedAt.IsZero() {
		return ""
	}
	end := time.Now()
	if !f.endedAt.IsZero() {
		end = f.endedAt
	}
	return end.Sub(f.startedAt).Truncate(time.Second).String()
}

// appendText adds a streamed chunk, merging with the current buffer when the
// role is unchanged so each text run becomes one block.
func (f *subagentFeed) appendText(role, chunk string) {
	if role == f.bufRole {
		f.bufText += chunk
	} else {
		f.flush()
		f.bufRole = role
		f.bufText = chunk
	}
	f.updatedAt = time.Now()
}

// flush renders the buffered text run as a styled block.
func (f *subagentFeed) flush() {
	if f.bufText == "" {
		f.bufRole = ""
		return
	}
	f.blocks = append(f.blocks, f.styledBuf())
	f.bufText, f.bufRole = "", ""
}

// addPending records an announced tool call for the feed's tail listing.
func (f *subagentFeed) addPending(tc pendingToolCall) {
	f.pending = append(f.pending, tc)
	f.updatedAt = time.Now()
}

// removePending drops a finished tool call from the feed's tail listing.
func (f *subagentFeed) removePending(id string) {
	for i, tc := range f.pending {
		if tc.ID == id {
			f.pending = append(f.pending[:i], f.pending[i+1:]...)
			return
		}
	}
}

// content renders the feed's display text: committed blocks, the in-flight
// buffer (rendered without committing), and pending tool lines, tail-windowed
// to two viewport screens like the main feed.
func (f *subagentFeed) content(h int) string {
	lines := make([]string, 0, len(f.blocks)+len(f.pending)+1)
	lines = append(lines, f.blocks...)
	if f.bufText != "" {
		lines = append(lines, f.styledBuf())
	}
	now := time.Now()
	for _, tc := range f.pending {
		line := stylePendingTool.Render("└ " + tc.Name)
		line += styleToolMeta.Render(fmt.Sprintf("%s%ds", separator, int(now.Sub(tc.StartedAt).Seconds())))
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return styleSubagentMeta.Render("(waiting for activity…)")
	}
	joined := strings.Join(lines, "\n")
	return tailWindow(strings.Split(joined, "\n"), h)
}

// styledBuf renders the in-flight buffer with its role's style without clearing
// it; used by content() for a live view and by flush() to commit.
func (f *subagentFeed) styledBuf() string {
	switch f.bufRole {
	case "thinking":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(f.thinkingColor)).Italic(true).Render(f.bufText)
	case "console":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(f.color)).Render("· " + f.bufText)
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(f.color)).Render(f.bufText)
	}
}

// feedAddTool appends a completed tool-call block to a feed (same block shape
// as the main history uses).
func (m *Model) feedAddTool(f *subagentFeed, ok bool, name string, args map[string]any, paramsPreview, preview string) {
	f.flush()
	paramLine := paramsPreview
	if paramLine == "" {
		paramLine = tools.SummarizeParams(name, args)
	}
	f.blocks = append(f.blocks, m.toolCallBlock(ok, name, args, paramLine, truncateDisplay(preview, 120)))
	f.updatedAt = time.Now()
}

// --- Model wiring ---

func (m *Model) feedByID(id string) *subagentFeed {
	for _, f := range m.subagentFeeds {
		if f.id == id {
			return f
		}
	}
	return nil
}

// upsertSubagentFeed registers or updates a feed from a lifecycle event.
func (m *Model) upsertSubagentFeed(ev data.SubagentEvent) {
	f := m.feedByID(ev.ID)
	if f == nil {
		f = &subagentFeed{id: ev.ID, status: subStatusQueued}
		m.subagentFeeds = append(m.subagentFeeds, f)
	}
	f.name = ev.Name
	if ev.Color != "" {
		f.color = ev.Color
	} else {
		f.color = "#9AB"
	}
	if ev.ThinkingColor != "" {
		f.thinkingColor = ev.ThinkingColor
	} else {
		f.thinkingColor = "#555"
	}
	f.status = ev.Status
	if !ev.StartedAt.IsZero() {
		f.startedAt = ev.StartedAt
	}
	if isSubagentTerminal(ev.Status) {
		if !ev.EndedAt.IsZero() {
			f.endedAt = ev.EndedAt
		} else {
			f.endedAt = time.Now()
		}
	}
}

// activeSubagentFeeds returns non-terminal feeds in spawn order (grid scope).
func (m *Model) activeSubagentFeeds() []*subagentFeed {
	var out []*subagentFeed
	for _, f := range m.subagentFeeds {
		if !isSubagentTerminal(f.status) {
			out = append(out, f)
		}
	}
	return out
}

// statusbarFeeds are the entries shown in the bar: active ones plus terminal
// ones still inside their linger window.
func (m *Model) statusbarFeeds() []*subagentFeed {
	now := time.Now()
	var out []*subagentFeed
	for _, f := range m.subagentFeeds {
		if !isSubagentTerminal(f.status) || now.Sub(f.endedAt) < subagentLinger {
			out = append(out, f)
		}
	}
	return out
}

// pruneSubagentFeeds drops entries past their linger window and auto-closes the
// live grid when nothing active remains.
func (m *Model) pruneSubagentFeeds() {
	now := time.Now()
	kept := m.subagentFeeds[:0]
	for _, f := range m.subagentFeeds {
		if !isSubagentTerminal(f.status) || now.Sub(f.endedAt) < subagentLinger {
			kept = append(kept, f)
		}
	}
	m.subagentFeeds = kept
	if m.subagentView && len(m.activeSubagentFeeds()) == 0 {
		m.subagentView = false
	}
}

// --- grid layout ---

// subCellLayout is one grid cell's geometry; index picks the feed to place.
type subCellLayout struct {
	index int
	x, y  int
	w, h  int
}

// subagentGridLayout divides a w×h region among up to four feeds:
// 1 -> full, 2 -> left/right halves, 3 -> two half-width on top + one full
// width below, 4+ -> 2x2 grid with the overflow counted for the footer.
func subagentGridLayout(w, h, n int) ([]subCellLayout, int) {
	show := min(n, 4)
	more := n - show
	regionH := h
	if more > 0 {
		regionH = h - 1
	}
	var cells []subCellLayout
	switch {
	case show == 1:
		cells = []subCellLayout{{index: 0, w: w, h: regionH}}
	case show == 2:
		half := w / 2
		cells = []subCellLayout{{index: 0, w: half, h: regionH}, {index: 1, x: half, w: w - half, h: regionH}}
	case show == 3:
		topH, half := regionH/2, w/2
		cells = []subCellLayout{
			{index: 0, w: half, h: topH},
			{index: 1, x: half, w: w - half, h: topH},
			{index: 2, y: topH, w: w, h: regionH - topH},
		}
	default:
		topH, leftW := regionH/2, w/2
		cells = []subCellLayout{
			{index: 0, w: leftW, h: topH},
			{index: 1, x: leftW, w: w - leftW, h: topH},
			{index: 2, y: topH, w: leftW, h: regionH - topH},
			{index: 3, x: leftW, y: topH, w: w - leftW, h: regionH - topH},
		}
	}
	for i := range cells {
		if cells[i].w < 4 {
			cells[i].w = 4
		}
		if cells[i].h < 5 {
			cells[i].h = 5
		}
	}
	return cells, more
}

// layoutSubagentGrid sizes the viewport pool and pushes dirty feed content at
// the same cadence the main feed uses. Called once per rendered frame while the
// grid is on screen.
func (m *Model) layoutSubagentGrid(cells []subCellLayout, more int) {
	m.subagentGridCells = cells
	m.subagentGridMore = more
	active := m.activeSubagentFeeds()
	for _, c := range cells {
		if c.index >= len(active) {
			continue
		}
		w, h := c.w-2, c.h-3 // border sides + title row
		if w < 1 {
			w = 1
		}
		if h < 1 {
			h = 1
		}
		vp := &m.components.subViewports[c.index]
		vp.SetWidth(w)
		vp.SetHeight(h)
		f := active[c.index]
		if f.updatedAt.After(f.pushedAt) || f.pushedAt.IsZero() {
			vp.SetContent(f.content(h))
			vp.GotoBottom()
			f.pushedAt = time.Now()
		}
	}
}

// gridPiece is one cell line waiting to be placed on a shared row.
type gridPiece struct {
	x    int
	line string
}

// renderSubagentGrid composes the bordered cells laid out by
// layoutSubagentGrid into region-height rows, plus the overflow footer.
func (m *Model) renderSubagentGrid(height int) string {
	cells := m.subagentGridCells
	if len(cells) == 0 {
		return ""
	}
	active := m.activeSubagentFeeds()

	regionH := height
	if m.subagentGridMore > 0 {
		regionH = height - 1
	}
	rowPieces := make([][]gridPiece, regionH)
	for _, c := range cells {
		if c.index >= len(active) {
			continue
		}
		f := active[c.index]
		title := f.id
		if f.name != "" && f.name != f.id {
			title = fmt.Sprintf("%s · %s", f.id, f.name)
		}
		titleLine := lipgloss.NewStyle().Foreground(lipgloss.Color(f.color)).Bold(true).Render(capRunes(title, c.w-4))
		body := strings.Join([]string{titleLine, m.components.subViewports[c.index].View()}, "\n")
		cellStr := lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), true).
			BorderForeground(lipgloss.Color(f.color)).
			Width(c.w).Height(c.h).
			Render(body)
		for i, line := range strings.Split(cellStr, "\n") {
			if c.y+i >= regionH {
				break
			}
			rowPieces[c.y+i] = append(rowPieces[c.y+i], gridPiece{x: c.x, line: line})
		}
	}

	rows := make([]string, height)
	for y, pieces := range rowPieces {
		if len(pieces) == 0 {
			continue
		}
		sort.Slice(pieces, func(i, j int) bool { return pieces[i].x < pieces[j].x })
		var s string
		cur := 0
		for _, p := range pieces {
			if p.x > cur {
				s += strings.Repeat(" ", p.x-cur)
				cur = p.x
			}
			s += p.line
			cur = p.x + lipgloss.Width(p.line)
		}
		rows[y] = s
	}
	if m.subagentGridMore > 0 {
		rows[regionH] = styleSubagentMeta.Render(fmt.Sprintf(" +%d more subagents", m.subagentGridMore))
	}
	return strings.Join(rows, "\n")
}

// --- status bar ---

func subagentBarStyle(f *subagentFeed) lipgloss.Style {
	switch f.status {
	case subStatusQueued:
		return styleSubagentQueued
	case subStatusCompleted:
		return styleSubagentDone
	case subStatusFailed, subStatusKilled, subStatusTimedOut:
		return styleStatusError
	}
	switch f.state {
	case stateThinking:
		return styleStatusThinking
	case stateExecuting:
		return styleStatusExecuting
	case stateParsing:
		return styleStatusParsing
	case stateWaiting:
		return styleStatusWaiting
	default:
		return styleStatusActive
	}
}

// subagentSeparator joins entries in the subagent status bar (no spaces);
// distinct from the dot separator shared with the main status line.
const subagentSeparator = "|"

// renderSubagentBar builds the subagent status line (top border + one content
// row) for the main view. Entries are right-to-left truncated into "+N more"
// when they stop fitting; on overflow, full entries are evicted until the pill
// itself fits so the row never wraps. Returns "" when there is nothing to show,
// which hides the bar entirely.
func (m *Model) renderSubagentBar() string {
	feeds := m.statusbarFeeds()
	if len(feeds) == 0 || m.components.width < 16 {
		return ""
	}
	usable := m.components.width - 2

	var parts []string
	for _, f := range feeds {
		label := fmt.Sprintf("%s %s", f.id, f.label())
		if e := f.elapsed(); e != "" {
			label += " " + e
		}
		parts = append(parts, subagentBarStyle(f).Render(label))
	}

	var shown []string
	for _, p := range parts {
		if len(shown) == 0 {
			shown = append(shown, p)
			continue
		}
		candidate := make([]string, 0, len(shown)+1)
		candidate = append(candidate, shown...)
		candidate = append(candidate, p)
		if lipgloss.Width(strings.Join(candidate, subagentSeparator)) <= usable {
			shown = candidate
			continue
		}
		// First overflow: collapse this entry and the remainder into a pill,
		// evicting full entries until the row fits.
		for hidden := len(parts) - len(shown); ; hidden++ {
			row := make([]string, 0, len(shown)+1)
			row = append(row, shown...)
			row = append(row, styleSubagentMeta.Render(fmt.Sprintf("+%d more", hidden)))
			if lipgloss.Width(strings.Join(row, subagentSeparator)) <= usable || len(shown) == 0 {
				shown = row
				break
			}
			shown = shown[:len(shown)-1]
		}
		break
	}

	line := lipgloss.NewStyle().Width(m.components.width).Render(strings.Join(shown, subagentSeparator))
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(userBorder).
		Width(m.components.width).
		Render(line)
}
