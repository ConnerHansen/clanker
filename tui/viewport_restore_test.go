package tui

import (
	"strings"
	"testing"

	"clankerv2/data"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

// NewTestModel builds a model with a main viewport of the given height whose
// full history is `lines` display lines, live-mode windowed (the two-screen
// tail in the viewport), mirroring a long session. The active viewport points
// at main; lastActiveWasMain starts false as after construction.
func NewTestModel(height int, lines []string) Model {
	input := textarea.New()
	input.Focus() // processPermission's approval path re-focuses the input
	model := Model{
		state: &State{
			history: []ChatMessage{}, // no history mutation in these tests
		},
		components: &Components{
			input: input,
		},
	}
	full := strings.Join(lines, "\n")
	model.state.renderedContent = full
	model.state.renderedLines = lines
	model.components.mainViewport = viewport.New(
		viewport.WithWidth(80),
		viewport.WithHeight(height),
	)
	model.components.mainViewport.SetContent(tailWindow(lines, height))
	model.components.activeViewport = &model.components.mainViewport
	return model
}

// testModel is NewTestModel plus one rendered main frame at the same height
// (no resize): it arms lastActiveWasMain so the next switch to the diff is
// detected, exactly like the first real frame.
func testModel(t *testing.T, height int, lines []string) Model {
	t.Helper()
	model := NewTestModel(height, lines)
	model.applyLayoutViewport(height) // first main frame (height unchanged -> no resize)
	return model
}

// applyFrame renders a frame at the given viewport-row budget (the same call
// view.go makes with its calculated height).
func (m *Model) applyFrame(calculatedHeight int) {
	m.applyLayoutViewport(calculatedHeight)
}

// scrollUpInMain mimics a scroll-up input: move the viewport and reconcile the
// feed mode, which loads the full history and remaps the offset into
// full-history coordinates. Returns the resulting absolute offset.
func scrollUpInMain(model *Model, windowOffset int) int {
	model.components.mainViewport.SetYOffset(windowOffset)
	model.syncFeedMode()
	return model.components.mainViewport.YOffset()
}

// adopt swaps *model with the result of a Model.Update call, mirroring
// Bubble Tea's `m = m.Update(msg)` loop (some handlers return the model by
// value, others by pointer).
func adopt(model *Model, updated tea.Model) {
	switch u := updated.(type) {
	case Model:
		*model = u
	case *Model:
		*model = *u
	default:
		panic("unexpected model type from Update")
	}
}

// presentDiff drives a real permission request through Model.Update — the same
// path the TUI takes when a tool asks for write approval (the alt viewport
// becomes active).
func presentDiff(model *Model) {
	updated, _ := model.Update(data.PermissionRequest{
		ID:   "perm-test",
		Tool: "system-modify",
		Prompt: "Update file: x.go",
		Viewport: &data.PermissionViewport{
			Kind:    "diff",
			Content: "diff content",
		},
	})
	adopt(model, updated)
}

// approveDiff drives the real approval key ("y") through Model.Update, which
// resolves the prompt and swaps back to the main viewport.
func approveDiff(model *Model) {
	updated, _ := model.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	adopt(model, updated)
}

// growHistory simulates new content arriving between frames (renderHistory + a
// 1hz reduced-rate tick push): the full buffer grows at the tail and is pushed
// into main WITHOUT touching main's offset.
func growHistory(model *Model, lines []string) {
	full := strings.Join(lines, "\n")
	model.state.renderedContent = full
	model.state.renderedLines = lines
	model.components.mainViewport.SetContent(full)
}

// TestDiffReturnRestoresScroll covers the display bug where returning from the
// diff (permission) viewport left the main viewport at the height/position of
// the buffered content instead of where the user's view had been.
func TestDiffReturnRestoresScroll(t *testing.T) {
	lines := linesFor(200) // live window = 40 lines; full history scrolls
	model := testModel(t, 20, lines)

	t.Run("resumes the exact offset captured when the diff opened", func(t *testing.T) {
		wantOffset := scrollUpInMain(&model, 5)
		if !model.state.feedScrolledBack {
			t.Fatal("expected scrolled-back feed mode after scrolling up")
		}

		presentDiff(&model)
		if model.components.activeViewport != &model.components.permissionViewport {
			t.Fatal("permission request should have switched to the diff viewport")
		}
		model.applyFrame(20) // a frame of the diff overlay

		if got := model.state.mainOffsetAtDiff; got != wantOffset {
			t.Fatalf("capture on diff entry: expected %d, got %d", wantOffset, got)
		}

		// The user scrolls inside the DIFF viewport (main's stored position must
		// not move with it) and new history arrives: the tick pushes full content
		// into main without touching its offset.
		model.components.permissionViewport.SetYOffset(10)
		growHistory(&model, linesFor(260))

		approveDiff(&model)
		if model.components.activeViewport != &model.components.mainViewport {
			t.Fatal("approval should have switched back to the main viewport")
		}
		model.applyFrame(20) // first frame back on main

		vp := model.components.mainViewport
		if got := vp.YOffset(); got != wantOffset {
			t.Fatalf("resume on diff exit: expected offset %d, got %d", wantOffset, got)
		}
		// The viewport must hold the full history again — not the short live
		// window (the "height of the buffered content" symptom).
		if got := vp.TotalLineCount(); got != 260 {
			t.Fatalf("expected full history (260) in main after resume, got %d lines", got)
		}
		if !model.state.feedScrolledBack {
			t.Fatal("expected scrolled-back feed mode after resuming mid-history")
		}
	})

	t.Run("resumes even when main's height changed while the diff was open", func(t *testing.T) {
		lines := linesFor(200)
		model := testModel(t, 20, lines)
		wantOffset := scrollUpInMain(&model, 15)

		presentDiff(&model)
		model.applyFrame(20) // diff frame at the normal layout (20 rows)

		// Close the diff while a pending-tools panel (or grown input) has shaved
		// the main viewport's row budget from 20 to 17.
		approveDiff(&model)
		model.applyFrame(17)

		if h := model.components.mainViewport.Height(); h != 17 {
			t.Fatalf("main height after resume: expected 17, got %d", h)
		}
		if got := model.components.mainViewport.YOffset(); got != wantOffset {
			t.Fatalf("resume offset: expected %d, got %d", wantOffset, got)
		}
	})

	t.Run("resumes at the bottom when main was following the tail", func(t *testing.T) {
		lines := linesFor(200)
		model := testModel(t, 20, lines)

		model.components.mainViewport.GotoBottom() // live-following (at bottom)
		model.syncFeedMode()                       // mode flag: at bottom

		presentDiff(&model)
		model.applyFrame(20)
		growHistory(&model, linesFor(260))

		approveDiff(&model)
		model.applyFrame(20)

		vp := model.components.mainViewport
		if !vp.AtBottom() {
			t.Fatalf("expected to be at bottom after resume (offset %d, max %d)", vp.YOffset(), 260-vp.Height())
		}
	})

	t.Run("plain main frame keeps the offset", func(t *testing.T) {
		lines := linesFor(200)
		model := testModel(t, 20, lines)

		wantOffset := scrollUpInMain(&model, 7)

		model.applyFrame(20) // a plain frame must not fight the scroll
		if got := model.components.mainViewport.YOffset(); got != wantOffset {
			t.Fatalf("plain main frame moved the offset: expected %d, got %d", wantOffset, got)
		}
	})
}

// TestNoViewportPermissionKeepsMain locks the display contract for
// label-only permission requests (no content payload): the alt viewport must
// NOT be swapped in — there is nothing to show — while the prompt itself stays
// active in the bottom panel.
func TestNoViewportPermissionKeepsMain(t *testing.T) {
	lines := linesFor(20)
	model := testModel(t, 20, lines)

	updated, _ := model.Update(data.PermissionRequest{
		ID:     "perm-nocontent",
		Tool:   "system.x",
		Prompt: "Bare permission request",
	})
	adopt(&model, updated)

	if model.components.activeViewport != &model.components.mainViewport {
		t.Fatal("permission request without viewport content must keep the main viewport active")
	}
	if model.listMode != listModePermission {
		t.Fatalf("prompt mode should still be permission, got %v", model.listMode)
	}

	// The prompt is resolvable and returns to a clean state.
	approveDiff(&model)
	if model.listMode != listModeNone {
		t.Fatalf("approval should clear the prompt, got %v", model.listMode)
	}
}

// TestPermissionViewportTitle covers header resolution: tool-provided title
// wins, kind derives the fallback.
func TestPermissionViewportTitle(t *testing.T) {
	cases := []struct {
		name     string
		viewport *data.PermissionViewport
		want     string
	}{
		{"nil viewport", nil, ""},
		{"title wins", &data.PermissionViewport{Kind: "script", Title: "bash-cmd command", Content: "x"}, "bash-cmd command"},
		{"diff fallback", &data.PermissionViewport{Kind: "diff", Content: "x"}, "Diff Contents"},
		{"script fallback", &data.PermissionViewport{Kind: "script", Content: "x"}, "Command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := data.PermissionRequest{Viewport: tc.viewport}
			if got := permissionViewportTitle(&req); got != tc.want {
				t.Errorf("title = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSyncFeedModeStaleWindowRecovery covers the mode desync that made the
// viewport "appear at the height of the buffered content": history can grow
// while an overlay is open (leaving the feed in scrolled-back mode), and a
// later windowed push's SetContent snap pins the offset to the short buffer's
// tail — leaving mode=scrollback + AtBottom()==true on just 40 lines. Before
// the fix this made syncFeedMode a no-op (AtBottom lied true), so every 1hz
// tick kept re-serving the short buffer and pinning the user to its tail. The
// fix loads the full history in that state, landing the user back at the
// document's live tail in live mode with a stable scroll.
func TestSyncFeedModeStaleWindowRecovery(t *testing.T) {
	lines := linesFor(260) // live window = 40 lines
	model := testModel(t, 20, lines)
	vp := &model.components.mainViewport

	// Construct the desynced stale-window state (what a sequence of
	// overlay-growth + windowed pushes leaves behind): mode says scrolled-back
	// while the viewport holds the 40-line live window snapped to its tail.
	vp.SetYOffset(5)
	model.syncFeedMode()                 // load full history (260 lines), remap offset
	vp.GotoBottom()                      // sit at the tail of the full document
	vp.SetContent(tailWindow(lines, 20)) // stale short push: snap pins offset to 20
	model.state.feedScrolledBack = true  // mode stayed scrolled-back underneath it

	if !vp.AtBottom() || !model.state.feedScrolledBack {
		t.Fatalf("test setup: expected mode=scrollback + AtBottom on a short buffer (offset %d, total %d)", vp.YOffset(), vp.TotalLineCount())
	}

	model.syncFeedMode()

	if got := vp.TotalLineCount(); got != 260 {
		t.Fatalf("expected full history (260) loaded, got %d lines", got)
	}
	if !vp.AtBottom() {
		t.Fatalf("expected back at the document tail after recovery (offset %d, max %d)", vp.YOffset(), 260-vp.Height())
	}
	if model.state.feedScrolledBack {
		t.Fatal("expected live mode after landing at the document tail")
	}

	// Steady windowed pushes are stable from here: following the tail means the
	// cheap window re-anchors its bottom (no position drift, no stuck scroll).
	vp.SetContent(tailWindow(lines, 20))
	model.syncFeedMode()
	if !vp.AtBottom() {
		t.Fatalf("offset %d lost its bottom after a window push", vp.YOffset())
	}
	if model.state.feedScrolledBack {
		t.Fatal("window push should keep live mode while following the tail")
	}
}
