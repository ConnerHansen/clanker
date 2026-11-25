package tui

import (
	"clankerv2/agent"
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
)

// TestLogCommandToggle verifies /log takes over the viewport region (active
// viewport -> log), suspends a running subagent grid, and that exiting
// restores both.
func TestLogCommandToggle(t *testing.T) {
	model := NewTestModel(10, []string{"a", "b", "c"})

	model.subagentView = true
	_, _ = model.runLogCommand()
	if !model.logView {
		t.Fatal("expected logView on after /log")
	}
	if model.subagentView {
		t.Error("expected subagent grid suspended while in log view")
	}
	if model.components.activeViewport != &model.components.logViewport {
		t.Error("expected active viewport to be the log viewport")
	}

	model.exitLogView()
	if model.logView {
		t.Fatal("expected logView off after exit")
	}
	if !model.subagentView {
		t.Error("expected subagent grid restored on exit")
	}
	if model.components.activeViewport != &model.components.mainViewport {
		t.Error("expected active viewport back at main")
	}
}

// TestRestoreOverlaySourcePicksLog verifies that closing a permission from the
// log view returns input routing to the log viewport with the view still on.
func TestRestoreOverlaySourcePicksLog(t *testing.T) {
	model := NewTestModel(10, []string{"a"})
	model.logView = true
	model.components.activeViewport = &model.components.logViewport

	presentDiff(&model)
	if model.components.activeViewport != &model.components.permissionViewport {
		t.Fatal("expected active viewport to be the permission viewport")
	}

	approveDiff(&model)
	if model.components.activeViewport != &model.components.logViewport {
		t.Error("expected active viewport back at the log viewport after resolve")
	}
	if !model.logView {
		t.Error("expected log view still active after permission resolve")
	}
}

// TestEscClosesLogBeforeGrid verifies ESC precedence: log view first, grid
// second, before a cancel of the main run.
func TestEscClosesLogBeforeGrid(t *testing.T) {
	model := NewTestModel(10, []string{"a"})
	model.logView = true
	model.subagentView = false
	model.components.activeViewport = &model.components.logViewport

	pressKey(&model, tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.logView {
		t.Fatal("expected log view closed by esc")
	}
	if model.components.activeViewport != &model.components.mainViewport {
		t.Error("expected active viewport back at main after esc")
	}
}

// TestSubagentsCommandFromLogWithNoRowsKeepsLog verifies that /subagents typed
// in the log view with no subagents present does NOT leave the log view (the
// early no-rows return must not strand the user out of it).
func TestSubagentsCommandFromLogWithNoRowsKeepsLog(t *testing.T) {
	if len(agent.ListSubagents("")) != 0 {
		t.Skip("test assumes an empty subagent registry")
	}

	model := NewTestModel(10, []string{"a"})
	// runSubagentsCommand's no-rows path sends a console line: give it a
	// drain channel (NewTestModel leaves ResponseChan nil, which would block).
	model.ResponseChan = make(chan any, 8)
	go func() {
		for range model.ResponseChan {
		}
	}()
	_, _ = model.runLogCommand()
	if !model.logView {
		t.Fatal("expected log view on before /subagents")
	}

	_, _ = model.runSubagentsCommand()
	if !model.logView {
		t.Error("expected to stay in log view when no subagents exist")
	}
	if model.subagentView {
		t.Error("expected grid off when no subagents exist")
	}
}

// TestRestoreOverlaySourceDropsPendingForGrid verifies that a stale main-scroll
// one-shot is dropped when the grid resumes behind an overlay: grid frames
// skip applyLayoutViewport, so the pending flag could never be consumed there.
func TestRestoreOverlaySourceDropsPendingForGrid(t *testing.T) {
	model := NewTestModel(10, []string{"a"})
	model.applyFrame(10)
	model.state.mainResumePending = true

	model.subagentView = true
	model.restoreOverlaySource()
	if model.components.activeViewport != &model.components.mainViewport {
		t.Fatal("expected main active viewport")
	}
	if model.state.mainResumePending {
		t.Error("expected stale one-shot resume dropped while the grid is on screen")
	}
}

// TestPermissionFromMainStillRestores verifies the pre-existing main->diff
// capture/restore path: scrolled-back main opens a permission, resolves, and
// resumes at its captured offset via the one-shot.
func TestPermissionFromMainStillRestores(t *testing.T) {
	lines := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("l%02d", i))
	}
	model := NewTestModel(10, lines)
	model.applyFrame(10)

	off := scrollUpInMain(&model, 5)
	if off == 0 {
		t.Fatal("expected a non-zero full-history offset after scrolling up")
	}
	if !model.state.feedScrolledBack {
		t.Fatal("expected scrolled-back feed mode after scrolling up")
	}

	presentDiff(&model)
	model.applyFrame(10) // first diff frame: captures main's scroll offset
	approveDiff(&model)

	if !model.state.mainResumePending {
		t.Fatal("expected one-shot resume armed after returning from the diff overlay")
	}
	model.applyFrame(10) // first main frame back: consumes the one-shot
	if got := model.components.mainViewport.YOffset(); got != off {
		t.Errorf("expected offset %d after restore, got %d", off, got)
	}
}

// logSample builds a buffer with short lines and one very long single-token
// line (no spaces to wrap at) so both break paths are exercised.
func logSample() string {
	return "2026/11/20 00:00:00 short first\n" +
		"2026-11-20 00:00:01 a moderately long line that will wrap across several display rows when the viewport is narrow enough to force it\n" +
		"2026-11-20 00:00:02 " + strings.Repeat("x", 400) + "\n" +
		"2026/11/20 00:00:03 last line\n"
}

// TestLogWrapChunkedEqualsWhole verifies the incremental wrap: feeding the log
// in chunks (a split mid-line) produces exactly the same display lines as one
// whole feed into a fresh state.
func TestLogWrapChunkedEqualsWhole(t *testing.T) {
	content := logSample()
	cut := len(content) - 37 // lands inside the last line, before its newline

	var whole logWrapState
	whole.consume(40, content)

	var chunked logWrapState
	chunked.consume(40, content[:cut])
	if got := chunked.consume(40, content); !linesEqual(got, whole.lines) {
		t.Fatalf("chunked wrap differs from whole wrap:\ngot  %d lines\nwant %d lines", len(chunked.lines), len(whole.lines))
	}
}

// TestLogWrapLinesWithinWidthNoLoss verifies every display line fits the width
// and no text is lost (character sequence ignoring whitespace survives).
func TestLogWrapLinesWithinWidthNoLoss(t *testing.T) {
	var s logWrapState
	lines := s.consume(40, logSample())
	for i, line := range lines {
		if w := lg.Width(line); w > 40 {
			t.Fatalf("line %d is %d wide (max 40): %q", i, w, line)
		}
	}
	want := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\t' {
			return -1
		}
		return r
	}, logSample())
	got := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\t' {
			return -1
		}
		return r
	}, strings.Join(lines, ""))
	if got != want {
		t.Fatalf("wrapped text lost or altered characters\ngot  %q\nwant %q", got, want)
	}
}

// TestLogWrapHoldsPartialLine verifies a record without its trailing newline
// produces no lines until completed; completing it emits the full line.
func TestLogWrapHoldsPartialLine(t *testing.T) {
	var s logWrapState
	partial := "2026/11/20 00:00:09 in"
	if lines := s.consume(40, partial); len(lines) != 0 {
		t.Fatalf("expected no display lines for a partial record, got %d: %v", len(lines), lines)
	}
	lines := s.consume(40, "2026/11/20 00:00:09 in the middle of a record\n")
	got := strings.Join(lines, " ")
	want := "in the middle of a record"
	normGot, normWant := squashSpace(got), want
	if len(lines) == 0 || !strings.Contains(normGot, normWant) {
		t.Fatalf("expected the completed record rendered (wrapped to width), got %q", got)
	}
}

// TestLogWrapResizeReraps verifies a wrap-width change discards the cache and
// re-wraps the whole buffer to the new width.
func TestLogWrapResizeReraps(t *testing.T) {
	content := logSample()
	var s logWrapState
	s.consume(40, content)
	wide := len(s.lines)
	lines := s.consume(10, content)
	if wide == 0 {
		t.Fatal("expected lines at width 40")
	}
	for i, line := range lines {
		if w := lg.Width(line); w > 10 {
			t.Fatalf("line %d is %d wide after resize to 10: %q", i, w, line)
		}
	}
	var fresh logWrapState
	fresh.consume(10, content)
	if !linesEqual(lines, fresh.lines) {
		t.Fatalf("re-wrapped lines differ from a fresh wrap at width 10")
	}
}

// TestLogViewportCommandWraps verifies the end-to-end /log path: long records
// from the global session log are wrapped to the component width in both the
// follow and scrolled-back refresh paths.
func TestLogViewportCommandWraps(t *testing.T) {
	model := NewTestModel(5, []string{"a"})
	model.components.width = 40
	model.components.logViewport = viewport.New(viewport.WithWidth(40), viewport.WithHeight(5))

	// Seed the global in-memory log (disable stderr mirroring so test output
	// stays clean; the buffer is process-lifetime, other tests ignore its content).
	agent.SessionLog().SetStderr(false)
	if _, err := agent.SessionLog().Write([]byte(logSample())); err != nil {
		t.Fatalf("seed session log: %v", err)
	}

	_, _ = model.runLogCommand()
	lines := model.state.logWrap.lines
	if len(lines) < 6 {
		t.Fatalf("expected the long records to wrap into more physical rows than raw lines, got %d", len(lines))
	}
	for i, line := range lines {
		if w := lg.Width(line); w > 40 {
			t.Fatalf("display line %d is %d wide (max 40): %q", i, w, line)
		}
	}

	// Scrolled-back path: full wrapped content with a frozen offset.
	model.components.logViewport.GotoTop()
	if model.components.logViewport.AtBottom() {
		t.Fatal("expected the viewport scrolled off its bottom")
	}
	model.refreshLogViewport()
	if got := model.components.logViewport.TotalLineCount(); got != len(lines) {
		t.Fatalf("scrolled-back path should load all %d wrapped lines, viewport has %d", len(lines), got)
	}

	// Follow path: tail-windowed content re-anchored to the bottom.
	model.components.logViewport.GotoBottom()
	model.refreshLogViewport()
	if !model.components.logViewport.AtBottom() {
		t.Fatal("expected follow refresh to re-anchor at the bottom")
	}
}

// TestLogViewPageUpRemapsOffset reproduces the windowed->full switch on scroll:
// following the log serves a two-screen tail window; paging up switches the
// refresh to the full wrapped history, and the user's backoff-from-tail must be
// remapped into full coordinates (one page up lands one page above the tail,
// not at the top of the whole buffer — the old behavior carried the windowed
// offset 0 over verbatim).
func TestLogViewPageUpRemapsOffset(t *testing.T) {
	model := NewTestModel(10, []string{"a"})
	model.components.width = 40
	model.components.logViewport.SetWidth(40)
	model.components.logViewport.SetHeight(10)

	agent.SessionLog().SetStderr(false)
	var b strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "line-%02d\n", i)
	}
	if _, err := agent.SessionLog().Write([]byte(b.String())); err != nil {
		t.Fatalf("seed session log: %v", err)
	}

	model.logView = true
	model.components.activeViewport = &model.components.logViewport
	model.refreshLogViewport()
	if !model.components.logViewport.AtBottom() {
		t.Fatal("expected the log view to start following at the bottom")
	}
	window := model.components.logViewport.TotalLineCount() // two-screen tail window
	if window <= 10 {
		t.Fatalf("expected a windowed buffer taller than one screen, got %d", window)
	}

	model.components.activeViewport.PageUp() // one page up inside the window
	winOff := model.components.logViewport.YOffset()
	distFromBottom := (window - 10) - winOff
	if distFromBottom <= 0 {
		t.Fatalf("expected a non-zero backoff from the tail after one page up, got %d", distFromBottom)
	}

	model.refreshLogViewport() // tick: switches to full content, must remap
	full := model.components.logViewport.TotalLineCount()
	if full <= window {
		t.Fatalf("expected full history (%d) taller than the window (%d), got %d", window, window, full)
	}
	want := (full - 10) - distFromBottom
	if got := model.components.logViewport.YOffset(); got != want {
		t.Fatalf("page-up offset not remapped: expected yoff %d (one page above the tail in full history), got %d", want, got)
	}
}

// squashSpace collapses consecutive spaces so wrapped-line comparisons can
// ignore wrap padding and join artifacts.
func squashSpace(s string) string {
	var b strings.Builder
	run := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			run = true
			continue
		}
		if run {
			b.WriteByte(' ')
			run = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

func linesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
