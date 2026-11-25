package tui

import (
	"strings"
	"testing"
)

func newTestFeed() *subagentFeed {
	return &subagentFeed{
		id:            "test",
		color:         "#FFF",
		thinkingColor: "#999",
	}
}

// TestContentDoesNotCommitBuffer verifies that calling content() during a
// streaming run does not flush the in-flight buffer to blocks, so subsequent
// appendText calls with the same role merge into one block.
func TestContentDoesNotCommitBuffer(t *testing.T) {
	f := newTestFeed()
	f.appendText("response", "hel")

	// Simulate a render frame: content() is called without committing.
	f.content(10)

	// Next token should merge, not start a new block.
	f.appendText("response", "lo")

	// Now flush (e.g., role change or tool call would trigger this).
	f.flush()

	if len(f.blocks) != 1 {
		t.Fatalf("expected 1 block after streaming + flush, got %d", len(f.blocks))
	}
	if !strings.Contains(f.blocks[0], "hello") {
		t.Errorf("expected merged text 'hello' in block, got %q", f.blocks[0])
	}
}

// TestStreamingProducesSingleBlock simulates the live-view scenario: many tokens
// arrive with interleaved content() calls (one per frame). All same-role tokens
// must end up as a single block.
func TestStreamingProducesSingleBlock(t *testing.T) {
	f := newTestFeed()
	tokens := []string{"The ", "quick ", "brown ", "fox "}
	for _, tok := range tokens {
		f.appendText("response", tok)
		// Each frame calls content(); this must not split the run.
		f.content(10)
	}
	f.flush()

	if len(f.blocks) != 1 {
		t.Fatalf("expected 1 block for a continuous same-role stream, got %d", len(f.blocks))
	}
	if !strings.Contains(f.blocks[0], "The quick brown fox") {
		t.Errorf("expected full text in single block, got %q", f.blocks[0])
	}
}

// TestRoleChangeProducesSeparateBlocks verifies that switching roles commits the
// prior run and starts a new one.
func TestRoleChangeProducesSeparateBlocks(t *testing.T) {
	f := newTestFeed()
	f.appendText("response", "answer text")
	f.appendText("thinking", "internal reasoning")

	// Force a final flush to commit the last in-flight buffer.
	f.flush()

	if len(f.blocks) != 2 {
		t.Fatalf("expected 2 blocks after role change, got %d", len(f.blocks))
	}
	if !strings.Contains(f.blocks[0], "answer text") {
		t.Errorf("block 0 should contain response text, got %q", f.blocks[0])
	}
	if !strings.Contains(f.blocks[1], "internal reasoning") {
		t.Errorf("block 1 should contain thinking text, got %q", f.blocks[1])
	}
}

// TestContentJoinsWithSingleNewline verifies that content() joins blocks with a
// single newline, not a double newline.
func TestContentJoinsWithSingleNewline(t *testing.T) {
	f := newTestFeed()
	f.appendText("response", "first")
	f.appendText("thinking", "second")

	// Commit both blocks by flushing after the role change already committed
	// the first one; flush commits the second.
	f.flush()

	out := f.content(10)
	lines := strings.Split(out, "\n")

	// Two blocks joined by single newline = 2 lines (assuming no wrapping at h=10).
	// If there were a double-newline join we'd see an empty line between them.
	for i, l := range lines {
		if l == "" {
			t.Errorf("found empty line at index %d; blocks should be joined by single \\n", i)
		}
	}

	if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Errorf("content missing expected text: %q", out)
	}
}

// TestContentShowsInFlightBuffer verifies that the in-flight (unflushed) buffer
// is visible in content() output even before a flush.
func TestContentShowsInFlightBuffer(t *testing.T) {
	f := newTestFeed()
	f.appendText("response", "partial text")

	// No flush — buffer is in-flight.
	out := f.content(10)
	if !strings.Contains(out, "partial text") {
		t.Errorf("content() should show in-flight buffer text, got %q", out)
	}

	// Buffer must still be there after the render.
	if f.bufText != "partial text" {
		t.Errorf("buffer should not be cleared by content(), got bufText=%q", f.bufText)
	}
}

// TestFlushClearsBuffer verifies flush() moves the buffer into blocks and resets
// state.
func TestFlushClearsBuffer(t *testing.T) {
	f := newTestFeed()
	f.appendText("response", "done")
	f.flush()

	if f.bufText != "" {
		t.Errorf("bufText should be empty after flush, got %q", f.bufText)
	}
	if f.bufRole != "" {
		t.Errorf("bufRole should be empty after flush, got %q", f.bufRole)
	}
	if len(f.blocks) != 1 {
		t.Fatalf("expected 1 block after flush, got %d", len(f.blocks))
	}
}
