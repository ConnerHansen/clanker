package tui

import (
	"strings"
	"testing"
)

func linesFor(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "line" + string(rune('a'+(i%26))) + "000" // distinct, equal width
	}
	return out
}

func TestTailWindow(t *testing.T) {
	t.Run("empty returns empty", func(t *testing.T) {
		if got := tailWindow(nil, 10); got != "" {
			t.Fatalf("expected empty string, got %q", got)
		}
	})

	t.Run("zero height falls back to full content", func(t *testing.T) {
		lines := linesFor(50)
		if got, want := tailWindow(lines, 0), strings.Join(lines, "\n"); got != want {
			t.Fatalf("expected full content for zero height; len(got)=%d len(want)=%d", len(got), len(want))
		}
	})

	t.Run("content fitting in two screens is unchanged", func(t *testing.T) {
		lines := linesFor(20) // window = 2*10 = 20
		if got, want := tailWindow(lines, 10), strings.Join(lines, "\n"); got != want {
			t.Fatalf("expected full content (fits exactly); len(got)=%d len(want)=%d", len(got), len(want))
		}

		short := linesFor(5)
		if got, want := tailWindow(short, 10), strings.Join(short, "\n"); got != want {
			t.Fatalf("expected full content (fits); got %q", got)
		}
	})

	t.Run("longer content is truncated to the last two screens", func(t *testing.T) {
		lines := linesFor(100)
		gotLines := strings.Split(tailWindow(lines, 10), "\n")
		if len(gotLines) != 20 {
			t.Fatalf("expected 20 lines (2 screens of 10), got %d", len(gotLines))
		}
		for i, want := range lines[80:] {
			if gotLines[i] != want {
				t.Fatalf("line %d: expected %q, got %q", i, want, gotLines[i])
			}
		}
	})

	t.Run("window tracks viewport height", func(t *testing.T) {
		lines := linesFor(100)
		gotLines := strings.Split(tailWindow(lines, 25), "\n")
		if len(gotLines) != 50 {
			t.Fatalf("expected 50 lines (2 screens of 25), got %d", len(gotLines))
		}
	})
}

func TestFeedTickInterval(t *testing.T) {
	if got, want := feedTickInterval(false), liveTickInterval; got != want {
		t.Fatalf("live interval: expected %v, got %v", want, got)
	}
	if got, want := feedTickInterval(true), scrolledBackTickInterval; got != want {
		t.Fatalf("scrolled-back interval: expected %v, got %v", want, got)
	}
}

func TestScrollToFullOffset(t *testing.T) {
	t.Run("full content that fits in the viewport clamps to top", func(t *testing.T) {
		if got := scrollToFullOffset(20, 8, 10, 10); got != 0 {
			t.Fatalf("expected 0 (full fits), got %d", got)
		}
	})

	t.Run("at the bottom of both is a no-op", func(t *testing.T) {
		// windowed: 30 lines, height 10 -> max offset 20; full: 100 lines -> max 90.
		if got := scrollToFullOffset(30, 100, 10, 20); got != 90 {
			t.Fatalf("expected bottom (90), got %d", got)
		}
	})

	t.Run("backoff from the tail is preserved", func(t *testing.T) {
		// User sits 5 lines above the window's bottom; full history keeps them
		// 5 lines above its own bottom.
		if got := scrollToFullOffset(30, 100, 10, 15); got != 85 {
			t.Fatalf("expected 85 (90 - 5), got %d", got)
		}
	})

	t.Run("large backoff is preserved when the full doc has slack", func(t *testing.T) {
		// yOffset 2 in a 30-line load = 18 lines of backoff; the 100-line full
		// doc has plenty of room, so the same distance from the bottom holds.
		if got := scrollToFullOffset(30, 100, 10, 2); got != 72 {
			t.Fatalf("expected 72 (90 - 18), got %d", got)
		}
	})

	t.Run("never negative", func(t *testing.T) {
		if got := scrollToFullOffset(5, 50, 10, 4); got < 0 {
			t.Fatalf("expected >= 0, got %d", got)
		}
	})
}
