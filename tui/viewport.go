package tui

import (
	"clankerv2/agent"
	"clankerv2/agent/tools"
	"strings"
	"time"
)

// Feed pacing: at the bottom the viewport serves a windowed content buffer
// (last two screens) so SetContent stays cheap and updates flow rapidly;
// while scrolled back it serves the full history at a reduced rate.
const (
	liveTickInterval         = 100 * time.Millisecond // 10hz, at bottom
	scrolledBackTickInterval = 1 * time.Second        // 1hz, scrolled back
)

// renderHistory re-renders the full offscreen history buffer from the model's
// chat messages and refreshes the live content window. It is the single point
// where message content becomes viewport lines (see State.renderedLines).
func (m *Model) renderHistory() {
	// var sb strings.Builder
	messages := make([]string, 0)
	for _, msg := range m.state.history {
		// Get out of here you blank bastard
		if msg.Content == "" {
			continue
		}

		content := strings.TrimSpace(msg.Content)

		var styled string
		switch msg.Role {
		case "user":
			styled = styleUser.Render(content)
		case "thinking":
			styled = styleThinking.Render(content)
		case "response":
			styled = styleResponse.Render(content)
		case "console":
			styled = styleConsole.Render(content)
		case "system":
			styled = styleSystemHeader.Width(m.components.width).Render(content)
		case "tool":
			if msg.ToolOK != nil {
				// Live tool call: fields come from the agent summary; content IS
				// the tool name (set from summary.Name).
				paramLine := tools.SummarizeParams(content, msg.ToolArgs)
				styled = m.toolCallBlock(*msg.ToolOK, content, msg.ToolArgs, paramLine, truncateDisplay(msg.ToolPreview, 120))
			} else if parsed, ok := parseStoredToolRow(content); ok {
				// Session row loaded from the DB: same block as live calls.
				// Prefer the tool's self-summary (stored or backfilled) over a
				// clipped raw result so loaded and live rendering match.
				preview := msg.ToolSummary
				if preview == "" {
					preview = parsed.Result
				}
				paramLine := tools.SummarizeParams(parsed.Name, parsed.Params)
				styled = m.toolCallBlock(parsed.Success, parsed.Name, msg.ToolArgs, paramLine, truncateDisplay(preview, 120))
			} else {
				// Unrecognized legacy content: bounded raw display.
				styled = styleToolMeta.Width(m.components.width).Render(truncateDisplay(content, 120))
			}
		}

		messages = append(messages, styled)
	}

	m.state.renderedContent = strings.Join(messages, "\n\n")
	// m.state.renderedContent = lipgloss.JoinVertical(lipgloss.Top, messages...)
	m.state.contentsUpdatedAt = time.Now()

	// Keep the offscreen line buffer and the live content window in sync with
	// the full-history render (the viewport itself is refreshed lazily on tick).
	if len(m.state.renderedContent) == 0 {
		m.state.renderedLines = nil
	} else {
		m.state.renderedLines = strings.Split(m.state.renderedContent, "\n")
	}
	m.refreshViewportContent()
}

// tailWindow returns the last two screens worth of lines as a display string.
// When the whole buffer fits in the window (or the viewport height is unknown
// or zero), the full content is returned unchanged. This keeps SetContent
// cheap for long sessions: only the tail is ever handed to the viewport in
// live mode.
func tailWindow(lines []string, height int) string {
	if len(lines) == 0 || height <= 0 {
		return strings.Join(lines, "\n")
	}
	window := height * 2
	if len(lines) <= window {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-window:], "\n")
}

// scrollToFullOffset maps a viewport offset taken against the live window
// (curTotal lines) onto an equivalent offset in the full history (fullTotal
// lines), preserving the user's backoff-from-tail distance when the content
// switches from windowed to full. At the bottom of both it is a no-op.
func scrollToFullOffset(curTotal, fullTotal, height, yOffset int) int {
	if fullTotal <= height {
		return 0
	}
	distFromBottom := (curTotal - height) - yOffset
	if distFromBottom < 0 {
		distFromBottom = 0
	}
	offset := (fullTotal - height) - distFromBottom
	if offset < 0 {
		offset = 0
	}
	if maxFull := fullTotal - height; offset > maxFull {
		offset = maxFull
	}
	return offset
}

// logWrapState incrementally wraps the append-only in-memory global log into
// display lines for the /log view: each tick wraps only newly appended bytes,
// so a long record costs nothing until its newline arrives. The cache resets on
// a wrap-width change (terminal resize) or a shrunken source (torn-state guard).
type logWrapState struct {
	width    int // viewport width used for lines (0 = not yet wrapped)
	consumed int // leading bytes of the current buffer already in lines
	pending  string
	lines    []string
}

func (s *logWrapState) reset() {
	s.width = 0
	s.consumed = 0
	s.pending = ""
	s.lines = nil
}

// consume wraps newly appended log content into display lines and returns the
// full wrapped buffer. The source must be append-only (agent.SessionLog's
// in-memory buffer is); a width change or shorter source triggers a one-time
// full re-wrap. A width of zero (viewport not sized yet) skips caching and
// returns the raw split.
func (s *logWrapState) consume(width int, content string) []string {
	if width <= 0 {
		s.reset()
		return strings.Split(content, "\n")
	}
	if s.width != width || len(content) < s.consumed+len(s.pending) {
		s.reset()
	}
	fresh := content[s.consumed+len(s.pending):]
	completed, rest := splitCompletedLines(s.pending + fresh)
	if completed != "" {
		s.lines = append(s.lines, wrapLogLines(width, completed)...)
	}
	s.width = width
	s.consumed = len(content) - len(rest)
	s.pending = rest
	return s.lines
}

// splitCompletedLines splits a buffer into the part ending at its last newline
// (completed) and the trailing partial line (rest, no newline).
func splitCompletedLines(buf string) (completed, rest string) {
	if i := strings.LastIndexByte(buf, '\n'); i >= 0 {
		return buf[:i+1], buf[i+1:]
	}
	return "", buf
}

// wrapLogLines renders each complete log line through the width-capped style so
// long records wrap instead of running off the viewport edge. Each rendered
// entry is flattened on newlines so the result holds one true display line per
// element (tailWindow and viewport offsets count physical rows).
func wrapLogLines(width int, completed string) []string {
	raw := strings.Split(completed, "\n")
	var out []string
	for i, line := range raw {
		if i == len(raw)-1 {
			break // trailing "" after the final newline
		}
		out = append(out, strings.Split(styleLogLine.Width(width).Render(line), "\n")...)
	}
	return out
}

// refreshLogViewport pushes a fresh snapshot of the in-memory global log into
// the log viewport: tail-windowed + bottom-anchored while following, full content with a frozen offset when scrolled back. The windowed->full switch on scroll remaps the user's backoff-from-tail distance into full-history coordinates (see scrollToFullOffset) so one page up lands one page above the tail instead of at the top of the buffer.
func (m *Model) refreshLogViewport() {
	vp := &m.components.logViewport
	lines := m.state.logWrap.consume(m.components.width, agent.SessionLog().Contents())
	if !vp.AtBottom() {
		content := strings.Join(lines, "\n")
		if vp.TotalLineCount() < len(lines) {
			// Full history is taller than the windowed buffer currently loaded:
			// load it and remap the offset into full coordinates.
			yOffset := vp.YOffset()
			curTotal := vp.TotalLineCount()
			vp.SetContent(content)
			fullTotal := vp.TotalLineCount()
			if fullTotal > vp.Height() {
				vp.SetYOffset(scrollToFullOffset(curTotal, fullTotal, vp.Height(), yOffset))
			}
			return
		}
		vp.SetContent(content)
		return
	}
	vp.SetContent(tailWindow(lines, vp.Height()))
	vp.GotoBottom()
}

// feedTickInterval is the live-feed cadence: rapid (10hz) while following the
// bottom, reduced (1hz) while the user has scrolled back.
func feedTickInterval(scrolledBack bool) time.Duration {
	if scrolledBack {
		return scrolledBackTickInterval
	}
	return liveTickInterval
}

// refreshViewportContent recomputes the live content window from the
// offscreen line buffer using the main viewport's current height.
func (m *Model) refreshViewportContent() {
	m.state.viewContent = tailWindow(m.state.renderedLines, m.components.mainViewport.Height())
}

// syncFeedMode reconciles the feed mode with where the main viewport is
// scrolled. Called on scroll input and before each tick so the transition is
// immediate rather than waiting for a tick to notice.
//
//   - At the bottom: switch back to rapid mode; the windowed buffer is pushed
//     on the next dirty tick, which re-anchors with GotoBottom (the viewport's
//     own SetContent already auto-snaps once it holds full content).
//
//   - Scrolled up: switch to reduced mode and immediately load the full
//     history so the 1hz updates keep growing the document underneath a stable
//     scroll position. The user's offset is remapped so the same lines stay on
//     screen (backoff-from-tail distance is preserved).
//
// The bottom state is re-measured *after* loading the full history: an offset
// that is out of range against a short loaded buffer (history grew while a diff
// overlay was open, then a windowed push auto-snapped it) makes AtBottom() read
// true on stale geometry. Acting on that would skip the load entirely —
// leaving 1hz ticks to keep re-serving the short buffer and pinning the user at
// its tail (the viewport "height of the buffered content" symptom) instead of
// resuming in full history.
func (m *Model) syncFeedMode() {
	vp := &m.components.mainViewport
	if m.state.feedScrolledBack && vp.AtBottom() && vp.TotalLineCount() < len(m.state.renderedLines) && len(m.state.renderedContent) > 0 {
		// Scrolled-back mode claims we are off the bottom, but the viewport is
		// at the tail of a stale short buffer: load the full history. The user
		// was following that buffer's tail — i.e. the document's live tail — so
		// resume at the true tail in live mode instead of remapping to a stale
		// mid-document spot.
		vp.SetContent(m.state.renderedContent)
		if vp.TotalLineCount() > vp.Height() {
			vp.GotoBottom()
		}
		m.state.feedScrolledBack = !vp.AtBottom()
		return
	}
	if !vp.AtBottom() && vp.TotalLineCount() < len(m.state.renderedLines) && len(m.state.renderedContent) > 0 {
		if m.state.feedScrolledBack {
			vp.SetContent(m.state.renderedContent)
		} else {
			// Scrolling up while live and the full history is longer than what is
			// loaded: load it first, then decide the mode from the new geometry.
			yOffset := vp.YOffset()
			curTotal := vp.TotalLineCount() // measured against the old (windowed) content
			vp.SetContent(m.state.renderedContent)
			fullTotal := vp.TotalLineCount()
			m.state.feedScrolledBack = !vp.AtBottom()
			if m.state.feedScrolledBack && fullTotal > vp.Height() {
				vp.SetYOffset(scrollToFullOffset(curTotal, fullTotal, vp.Height(), yOffset))
			}
		}
		return
	}
	m.state.feedScrolledBack = !vp.AtBottom()
}

// applyLayoutViewport fits all full-screen viewports to this frame's layout.
// The non-active ones must keep their height current too, or they re-anchor
// against stale geometry on the next switch.
func (m *Model) applyLayoutViewport(calculatedHeight int) {
	if m.components.activeViewport == nil {
		return
	}
	prevWasMain := m.components.lastActiveWasMain // state of the PREVIOUS frame
	m.components.lastActiveWasMain = m.components.activeViewport == &m.components.mainViewport
	if calculatedHeight != m.components.activeViewport.Height() {
		for _, other := range m.components.inactiveViewports() {
			atBottom := other.AtBottom()
			other.SetHeight(calculatedHeight)
			if atBottom {
				other.GotoBottom()
			}
		}
		atBottom := m.components.activeViewport.AtBottom()
		m.components.activeViewport.SetHeight(calculatedHeight)
		if atBottom {
			m.components.activeViewport.GotoBottom()
		}
	}

	switch {
	case m.components.activeViewport == &m.components.mainViewport:
		if m.state.mainResumePending {
			// First frame back from the diff overlay: resume where main left
			// off (one-shot, so later frames don't fight user scrolling).
			m.restoreMainScroll()
		}
	case prevWasMain:
		// First frame of the diff overlay (previous frame was main):
		// remember where main left off — but only when we are actually coming
		// BACK to main. When the overlay opened over the log view, restore goes
		// to the log viewport (see restoreOverlaySource), so capturing main's
		// scroll here would leave a stale one-shot resume pending.
		if !m.logView {
			m.captureMainForDiff()
		}
	}
}

// captureMainForDiff stores the main viewport's scroll position on the first
// frame after switching to the diff (permission) viewport, so View can resume
// exactly there when we come back.
func (m *Model) captureMainForDiff() {
	m.state.mainOffsetAtDiff = m.components.mainViewport.YOffset()
	m.state.feedScrolledBackAtDiff = m.state.feedScrolledBack
	m.state.mainResumePending = true
}

// restoreMainScroll resumes the main viewport exactly where it left off after
// returning from the diff (permission) viewport. It is a one-shot: the pending
// flag is consumed so plain main frames afterwards leave the user's subsequent
// scrolling alone.
//
// While the overlay was open, the ticks kept pushing (full) content into main
// without touching its offset and history only grows at the tail, so the
// stored absolute offset pins the same lines on screen; SetYOffset clamps to
// maxYOffset internally. A user who was following the live tail before the
// diff gets re-anchored to the new tail (the feed's normal bottom-anchoring)
// rather than a stale mid-history position.
func (m *Model) restoreMainScroll() {
	if !m.state.mainResumePending {
		return
	}
	m.state.mainResumePending = false
	vp := &m.components.mainViewport
	if m.state.mainOffsetAtDiff <= 0 {
		// Nothing captured (main was empty when the diff opened): hand back to
		// the feed's normal bottom-anchoring.
		m.syncFeedMode()
		return
	}
	if !m.state.feedScrolledBackAtDiff {
		// Was at the bottom before the diff: resume at the (possibly grown)
		// tail, matching the live-follow behavior.
		vp.GotoBottom()
		m.state.feedScrolledBack = false
		return
	}
	vp.SetYOffset(m.state.mainOffsetAtDiff)
	m.state.feedScrolledBack = !vp.AtBottom()
}
