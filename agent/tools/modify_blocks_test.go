package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rep builds a replace-shape change: find the exact `old` block once, swap it
// for `new`.
func rep(file, old, new string) Change {
	return Change{File: file, Type: "replace", Old: old, New: new}
}

// anch builds an anchor-shape change: grab everything from the start block to
// the end block INCLUSIVE and replace it with `new`. Empty start/end = file
// edges.
func anch(file, start, end, new string) Change {
	return Change{File: file, Type: "anchor", Start: start, End: end, New: new}
}

// setupModifyDir creates a fresh temp dir, chdirs into it (so relative paths in
// the tools resolve like production), and writes each named file with its
// content. The cleanup restores the working directory.
func setupModifyDir(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(oldWd) })
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// ==================== Shape 1: replace (find-and-replace) ====================

// TestReplaceSingleLineMidFile is the canonical short swap.
func TestReplaceSingleLineMidFile(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "Hello World\nThis is a test.\nGoodbye World"})

	got, failures, _ := SimulateApplyChanges([]Change{
		rep("f.txt", "This is a test.", "This is a TEST."),
	})
	assertSingleSuccess(t, got, failures, "f.txt", "Hello World\nThis is a TEST.\nGoodbye World")
}

// TestReplaceMultiLineSwapsSeveral replaces a contiguous multi-line block.
func TestReplaceMultiLineSwapsSeveral(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb\nc\nd"})

	got, failures, _ := SimulateApplyChanges([]Change{
		rep("f.txt", "b\nc", "bc-combined"),
	})
	assertSingleSuccess(t, got, failures, "f.txt", "a\nbc-combined\nd")
}

// TestReplaceExpandsOneLineIntoSeveral; interior blank lines in the replacement
// are preserved.
func TestReplaceExpandsOneLineIntoSeveral(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb\nc"})

	got, failures, _ := SimulateApplyChanges([]Change{
		rep("f.txt", "b", "x1\n\nx2"),
	})
	assertSingleSuccess(t, got, failures, "f.txt", "a\nx1\n\nx2\nc")
}

// TestReplaceDeletesWithEmptyNew: an empty `new` removes the located block.
func TestReplaceDeletesWithEmptyNew(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb\nc"})

	got, failures, _ := SimulateApplyChanges([]Change{
		rep("f.txt", "b", ""),
	})
	assertSingleSuccess(t, got, failures, "f.txt", "a\nc")
}

// TestReplaceNoOpRejectsFails rejects swapping a block for its own content.
func TestReplaceNoOpRejectsFails(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb\nc"})

	_, failures, _ := SimulateApplyChanges([]Change{
		rep("f.txt", "b\nc", "b\nc"),
	})
	assertSingleFailure(t, failures, "f.txt", "no-op")
}

// TestReplaceNotFoundFails: a block absent from the file fails loudly.
func TestReplaceNotFoundFails(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb\nc"})

	_, failures, _ := SimulateApplyChanges([]Change{
		rep("f.txt", "does-not-exist", "x"),
	})
	assertSingleFailure(t, failures, "f.txt", "not found")
}

// TestReplaceMultiMatchFailsWithCandidates rejects an ambiguous block and lists
// candidate ranges so the model can extend it with more context.
func TestReplaceMultiMatchFailsWithCandidates(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb\na\nb\na"})

	_, failures, _ := SimulateApplyChanges([]Change{
		rep("f.txt", "a", "X"),
	})
	if len(failures) != 1 {
		t.Fatalf("expected one failure, got %+v", failures)
	}
	errMsg := failures[0].Error
	for _, want := range []string{"matched 3 times", "lines 1-1", "extend it with more surrounding lines"} {
		if !strings.Contains(errMsg, want) {
			t.Errorf("multi-match error should contain %q, got %q", want, errMsg)
		}
	}
}

// TestReplaceAbsorbsTrailingNewlineAndBlankPadding: a stray trailing newline on
// `old` plus an added blank first line must not break the match.
func TestReplaceAbsorbsTrailingNewlineAndBlankPadding(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb\nc"})

	got, failures, _ := SimulateApplyChanges([]Change{
		rep("f.txt", "\nb\n", "B"),
	})
	assertSingleSuccess(t, got, failures, "f.txt", "a\nB\nc")
}

// TestReplaceLiteralBackslashNNotSplitOn proves the fake-newline guard: a line
// containing the two characters \n is ONE record and matches as such. The
// byte-level approach would split it into three records and fail to match.
func TestReplaceLiteralBackslashNNotSplitOn(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": `x := "a\nb"`})

	got, failures, _ := SimulateApplyChanges([]Change{
		rep("f.txt", `x := "a\nb"`, `y := "c\nd"`),
	})
	assertSingleSuccess(t, got, failures, "f.txt", `y := "c\nd"`)
}

// TestBackslashCountRoundTripAndVerbatimMatch pins the write/read/modify path
// for backslashes: content with 1, 2, and 4 LITERAL \ characters must be
// written byte-identically, and an `old` block copied verbatim from the file's
// own lines (any count) must match under strict mode. Regression guard for the
// suspected write/match escape asymmetry (ticket: publisher.ex incident).
func TestBackslashCountRoundTripAndVerbatimMatch(t *testing.T) {
	cases := []struct {
		name, line string
	}{
		{"one-literal", `def publish_event(%Payload{} = payload, server \ @server_name) do`},
		{"two-literal", `a = "\\"`},
		{"four-literal", `b = \\\\`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupModifyDir(t, map[string]string{tc.name + ".txt": tc.line})
			file := tc.name + ".txt"

			// 1. Write path: on-disk bytes must equal the source exactly.
			if err := os.WriteFile(file, []byte(tc.line), 0o644); err != nil {
				t.Fatal(err)
			}
			disk, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if string(disk) != tc.line {
				t.Fatalf("write round-trip failed: disk = %q (bytes % x), want %q", disk, disk, tc.line)
			}

			// 2. Modify-match equivalence: an `old` block taken VERBATIM from the
			// file's own lines must be found — strictly, never lenient.
			_, failures, _ := SimulateApplyChanges([]Change{
				rep(file, string(disk), "CHANGED"),
			})
			assertSingleSuccess(t, map[string]string{file: "CHANGED"}, failures, file, "CHANGED")

			spans, lenient := matchBlock(splitRecords(string(disk)), normalizeBlock(string(disk)))
			if len(spans) != 1 {
				t.Fatalf("verbatim read line should match exactly once, got %d spans", len(spans))
			}
			if lenient {
				t.Errorf("strict (tier 1) equality is required for a verbatim copy; it fell to the whitespace-insensitive tier")
			}
		})
	}
}

// TestHandleWriteReadModifyBackslashTransportRoundTrip drives the REAL handler
// path — JSON arguments decoded by HandleToolCall, bytes read from disk after
// system-write, then system-modify with an `old` copied verbatim from those
// bytes. This is the full transport loop the incident repro exercises; if any
// layer ever unescapes/doubles backslash sequences between decode and disk, this
// fails.
func TestHandleWriteReadModifyBackslashTransportRoundTrip(t *testing.T) {
	setupModifyDir(t, nil)
	path := "roundtrip.txt"
	cap := &permCapture{}

	want := []string{
		`one: server \ @server_name`, // 1 literal backslash (Elixir default arg)
		`two: \\`,                    // 2
		`four: \\\\`,                // 4
	}
	content := strings.Join(want, "\n")

	// system-write via the handler; on-disk bytes must equal the argument.
	wres := HandleToolCall(nil, nil, "system-write", map[string]any{"path": path, "content": content}, cap.fn(true), nil)
	if !wres.Success {
		t.Fatalf("write failed: %+v", wres.Result)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(disk) != content {
		t.Fatalf("on-disk bytes after handler write = %q (bytes % x), want %q", disk, disk, content)
	}

	// system-modify via the handler: every line, copied verbatim from the on-
	// disk bytes, must locate under strict matching.
	changes := make([]any, 0, len(want))
	for _, line := range want {
		changes = append(changes, map[string]any{
			"file": path, "type": "replace", "old": line, "new": line + "; // touched",
		})
	}
	mres := HandleToolCall(nil, nil, "system-modify", map[string]any{"changes": changes}, cap.fn(true), nil)
	if !mres.Success {
		t.Fatalf("verbatim-copy modify must succeed: %+v", mres.Result)
	}

	finalDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	gotLines := strings.Split(string(finalDisk), "\n")
	for i, line := range want {
		if gotLines[i] != line+"; // touched" {
			t.Errorf("line %d after modify = %q, want %q", i+1, gotLines[i], line+"; // touched")
		}
	}
}

// TestNearMissHintOnRepeatedCharDrift proves that when an `old` block misses ONLY
// due to a repeated-character count drift, the failure message quotes the ACTUAL
// on-disk line verbatim so the model can see the true character count. Backslash
// runs are built with strings.Repeat so the test itself cannot drift in escapes.
func TestNearMissHintOnRepeatedCharDrift(t *testing.T) {
	bs := "\\"
	diskLine := "def publish_event(%Payload{} = payload, server " + strings.Repeat(bs, 4) + " @server_name) do"
	setupModifyDir(t, map[string]string{"f.ex": diskLine})

	block := "def publish_event(%Payload{} = payload, server " + strings.Repeat(bs, 3) + " @server_name) do" // 3 literals -> drift
	_, failures, _ := SimulateApplyChanges([]Change{
		rep("f.ex", block, "X"),
	})
	assertSingleFailure(t, failures, "f.ex", "not found")
	errMsg := failures[0].Error
	if !strings.Contains(errMsg, "resembles the file at line 1: ") {
		t.Fatalf("expected a near-miss hint, got %q", errMsg)
	}
	// The hint must quote the on-disk text VERBATIM (4 backslashes), not the block's.
	if !strings.Contains(errMsg, "server "+strings.Repeat(bs, 4)+" @server_name") {
		t.Errorf("hint must carry the verbatim 4-backslash disk text, got %q", errMsg)
	}
}

// TestNearMissHintMultiLineBlock: a multi-line block that misses only due to a
// run drift in ONE line still earns a hint naming the whole span.
func TestNearMissHintMultiLineBlock(t *testing.T) {
	bs := "\\"
	disk := "line-a\n" + "server "+strings.Repeat(bs, 2)+" @x,\nline-c" // 2 literals on line 2
	block := "line-a\n" + "server "+strings.Repeat(bs, 1)+" @x,\nline-c" // 1 literal -> drift
	setupModifyDir(t, map[string]string{"f.ex": disk})

	_, failures, _ := SimulateApplyChanges([]Change{
		rep("f.ex", block, "X"),
	})
	assertSingleFailure(t, failures, "f.ex", "not found")
	if !strings.Contains(failures[0].Error, "resembles the file at line 1: ") {
		t.Fatalf("expected a multi-line near-miss hint, got %q", failures[0].Error)
	}
}

// TestNearMissHintAbsentForUnrelatedBlock: genuinely absent text gets a clean
// "not found" with NO misleading near-miss hint.
func TestNearMissHintAbsentForUnrelatedBlock(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "alpha\nbeta\ngamma"})

	_, failures, _ := SimulateApplyChanges([]Change{
		rep("f.txt", "zzz entirely unrelated", "X"),
	})
	assertSingleFailure(t, failures, "f.txt", "not found")
	if strings.Contains(failures[0].Error, "resembles") {
		t.Fatalf("unrelated block must not produce a near-miss hint: %q", failures[0].Error)
	}
}

// TestReplaceCaseSensitive: case differences are NOT absorbed by the lenient
// tier (only whitespace/indentation drift is).
func TestReplaceCaseSensitive(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "Hello World"})

	_, failures, _ := SimulateApplyChanges([]Change{
		rep("f.txt", "hello world", "x"),
	})
	assertSingleFailure(t, failures, "f.txt", "not found")
}

// ==================== Shape 2: anchor (grab A -> B inclusive) ====================

// TestAnchorInclusiveRegionReplacesBetweenAnchors is the canonical anchor edit:
// start and end mark KEPT boundaries that are part of the replaced region, so
// they must be echoed in `new` to survive.
func TestAnchorInclusiveRegionReplacesBetweenAnchors(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "Hello World\nThis is a test.\nGoodbye World"})

	got, failures, _ := SimulateApplyChanges([]Change{
		anch("f.txt", "Hello World", "Goodbye World", "Hello World\nLine 2 modified\nGoodbye World"),
	})
	assertSingleSuccess(t, got, failures, "f.txt", "Hello World\nLine 2 modified\nGoodbye World")
}

// TestAnchorMultiLineBlocksSpanningManyLines: anchors may be multi-line blocks;
// the region runs from the FIRST start line to the LAST end line.
func TestAnchorMultiLineBlocksSpanningManyLines(t *testing.T) {
	file := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine"
	setupModifyDir(t, map[string]string{"f.txt": file})

	got, failures, _ := SimulateApplyChanges([]Change{
		anch("f.txt", "two\nthree", "seven\neight", "TWO\nTHREE\nFOUR\nFIVE\nSIX\nSEVEN\nEIGHT"),
	})
	assertSingleSuccess(t, got, failures, "f.txt",
		"one\nTWO\nTHREE\nFOUR\nFIVE\nSIX\nSEVEN\nEIGHT\nnine")
}

// TestAnchorInsertBetweenKeptLines: inclusive semantics make insertion a normal
// case — the region covers everything between (and including) the boundaries,
// and `new` simply re-includes the lines that should survive. Forgetting to
// echo a kept line is visible in the diff preview, never silent corruption.
func TestAnchorInsertBetweenKeptLines(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb\nc"})

	got, failures, _ := SimulateApplyChanges([]Change{
		anch("f.txt", "a", "c", "a\nb\ninserted\nc"),
	})
	assertSingleSuccess(t, got, failures, "f.txt", "a\nb\ninserted\nc")
}

// TestAnchorBeginningEdit has no start block (region starts at line 1).
func TestAnchorBeginningEdit(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "Hello World\nThis is a test.\nGoodbye World"})

	got, failures, _ := SimulateApplyChanges([]Change{
		anch("f.txt", "", "This is a test.", "Hello Gopher\nThis is a test."),
	})
	assertSingleSuccess(t, got, failures, "f.txt", "Hello Gopher\nThis is a test.\nGoodbye World")
}

// TestAnchorEndingEdit has no end block (region runs to end of file).
func TestAnchorEndingEdit(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "Hello World\nThis is a test.\nGoodbye World"})

	got, failures, _ := SimulateApplyChanges([]Change{
		anch("f.txt", "This is a test.", "", "This is a test.\nSee you later"),
	})
	assertSingleSuccess(t, got, failures, "f.txt", "Hello World\nThis is a test.\nSee you later")
}

// TestAnchorWholeFileReplace uses no anchors at all.
func TestAnchorWholeFileReplace(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "old line 1\nold line 2"})

	got, failures, _ := SimulateApplyChanges([]Change{
		anch("f.txt", "", "", "brand new content"),
	})
	assertSingleSuccess(t, got, failures, "f.txt", "brand new content")
}

// TestAnchorSingleLineRegion: identical single-line start/end collapse to a
// one-line region.
func TestAnchorSingleLineRegion(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb\nc"})

	got, failures, _ := SimulateApplyChanges([]Change{
		anch("f.txt", "b", "b", "B"),
	})
	assertSingleSuccess(t, got, failures, "f.txt", "a\nB\nc")
}

// TestAnchorDeletionWithEmptyNew removes the inclusive region.
func TestAnchorDeletionWithEmptyNew(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb\nc\nd"})

	got, failures, _ := SimulateApplyChanges([]Change{
		anch("f.txt", "b", "c", ""),
	})
	assertSingleSuccess(t, got, failures, "f.txt", "a\nd")
}

// TestAnchorTrailingNewlinePreserved: a file ending in "\n" keeps it.
func TestAnchorTrailingNewlinePreserved(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb\nc\n"})

	got, failures, _ := SimulateApplyChanges([]Change{
		anch("f.txt", "a", "c", "a\nB\nc"),
	})
	assertSingleSuccess(t, got, failures, "f.txt", "a\nB\nc\n")
}

// TestAnchorCRLFPreserved normalizes CRLF for matching but restores it on write.
func TestAnchorCRLFPreserved(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "one\r\ntwo\r\nthree\r\n"})

	got, failures, _ := SimulateApplyChanges([]Change{
		anch("f.txt", "one", "three", "one\nTWO\nthree"),
	})
	assertSingleSuccess(t, got, failures, "f.txt", "one\r\nTWO\r\nthree\r\n")
}

// TestAnchorNewMayCarryTrailingNewline: one trailing newline on `new` is
// absorbed (the old hard error is gone).
func TestAnchorNewMayCarryTrailingNewline(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb\nc"})

	got, failures, _ := SimulateApplyChanges([]Change{
		anch("f.txt", "a", "c", "a\nB\nc\n"),
	})
	assertSingleSuccess(t, got, failures, "f.txt", "a\nB\nc")
}

// TestAnchorNoOpRejectsFails rejects a region reproduced unchanged.
func TestAnchorNoOpRejectsFails(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb\nc"})

	_, failures, _ := SimulateApplyChanges([]Change{
		anch("f.txt", "a", "c", "a\nb\nc"),
	})
	assertSingleFailure(t, failures, "f.txt", "no-op")
}

// TestAnchorInvertedFails: the start block matching at/after the end block's
// last line is rejected.
func TestAnchorInvertedFails(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb\nc"})

	_, failures, _ := SimulateApplyChanges([]Change{
		anch("f.txt", "c", "a", "x"),
	})
	assertSingleFailure(t, failures, "f.txt", "inverted")
}

// TestAnchorStartNotFoundFails names the missing block.
func TestAnchorStartNotFoundFails(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb\nc"})

	_, failures, _ := SimulateApplyChanges([]Change{
		anch("f.txt", "nope", "c", "x"),
	})
	assertSingleFailure(t, failures, "f.txt", "'start' anchor not found")
}

// TestAnchorMultiMatchStartFails: a start block that matches twice is ambiguous.
func TestAnchorMultiMatchStartFails(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb\na\nc"})

	_, failures, _ := SimulateApplyChanges([]Change{
		anch("f.txt", "a", "c", "x"),
	})
	assertSingleFailure(t, failures, "f.txt", "matched 2 times")
}

// TestAnchorLenientWhitespaceTier: tab/space indentation drift in the anchors is
// tolerated by the whitespace-insensitive tier, and the result notes it.
func TestAnchorLenientWhitespaceTier(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "\t\treturn x\n\t\treturn y\nplain"})

	simulated, failures, contexts := SimulateApplyChanges([]Change{
		// Model copied the block with spaces instead of tabs.
		anch("f.txt", "    return x", "plain", "        RETURN X\n        return y\nplain"),
	})
	if len(failures) != 0 {
		t.Fatalf("lenient tier should have matched, got %+v", failures)
	}
	want := "        RETURN X\n        return y\nplain"
	if simulated["f.txt"] != want {
		t.Errorf("simulated content = %q, want %q", simulated["f.txt"], want)
	}
	ctx := contexts["f.txt"]
	if !strings.Contains(ctx, "whitespace-insensitive") {
		t.Errorf("lenient match should be noted in the result context, got:\n%s", ctx)
	}
}

// TestAnchorLenientDoesNotMaskCaseDrift: the lenient tier normalizes whitespace
// only — a case difference is still "not found".
func TestAnchorLenientDoesNotMaskCaseDrift(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "\t\treturn x\nplain"})

	_, failures, _ := SimulateApplyChanges([]Change{
		anch("f.txt", "    RETURN x", "plain", "x"),
	})
	assertSingleFailure(t, failures, "f.txt", "'start' anchor not found")
}

// TestAnchorMultiLineLenientTier: a whole multi-line block with drifted
// indentation matches on tier 2.
func TestAnchorMultiLineLenientTier(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "\tif true {\n\t\tlog.Println(\"hi\")\n\t}\ndone"})

	got, failures, _ := SimulateApplyChanges([]Change{
		anch("f.txt", "  if true {\n    log.Println(\"hi\")\n  }", "done",
			"\tif true {\n\t\tlog.Println(\"hello\")\n\t}\ndone"),
	})
	assertSingleSuccess(t, got, failures, "f.txt", "\tif true {\n\t\tlog.Println(\"hello\")\n\t}\ndone")
}

// ==================== Cross-cutting: batching, isolation, file states ========

// TestTwoChangesOneFileNoRereadNeeded: both changes resolve against the original
// snapshot — the first (top) edit shifting lines never invalidates the second.
func TestTwoChangesOneFileNoRereadNeeded(t *testing.T) {
	file := "l1\nl2\nl3\nl4\nl5\nl6"
	setupModifyDir(t, map[string]string{"f.txt": file})

	got, failures, _ := SimulateApplyChanges([]Change{
		rep("f.txt", "l2", "L2-modified"),
		anch("f.txt", "l4", "l5", "L4\nL5-new"),
	})
	assertSingleSuccess(t, got, failures, "f.txt",
		"l1\nL2-modified\nl3\nL4\nL5-new\nl6")
}

// TestOverlappingChangesFail: two changes sharing an original line are rejected.
func TestOverlappingChangesFail(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb\nc\nd"})

	_, failures, _ := SimulateApplyChanges([]Change{
		rep("f.txt", "b\nc", "X"),
		anch("f.txt", "c", "d", "Y"),
	})
	assertSingleFailure(t, failures, "f.txt", "overlap")
}

// TestSameFileIsolatedFromOtherFiles: a broken change in one file does not abort
// the others.
func TestSameFileIsolatedFromOtherFiles(t *testing.T) {
	setupModifyDir(t, map[string]string{
		"good_a.txt": "alpha\nbeta",
		"good_b.txt": "one\ntwo",
		"bad.txt":    "keep",
	})

	simulated, failures, _ := SimulateApplyChanges([]Change{
		rep("good_a.txt", "alpha", "ALPHA"),
		rep("bad.txt", "does-not-exist", "x"),
		rep("good_b.txt", "two", "TWO"),
	})

	if len(simulated) != 2 {
		t.Fatalf("expected 2 files simulated despite one bad file, got %d: %v", len(simulated), simulated)
	}
	if simulated["good_a.txt"] != "ALPHA\nbeta" {
		t.Errorf("good_a: got %q", simulated["good_a.txt"])
	}
	if simulated["good_b.txt"] != "one\nTWO" {
		t.Errorf("good_b: got %q", simulated["good_b.txt"])
	}
	if len(failures) != 1 || failures[0].File != "bad.txt" || failures[0].Successful {
		t.Errorf("expected exactly one failure for bad.txt, got %+v", failures)
	}
}

// TestMissingFileFails: system-modify requires an existing file.
func TestMissingFileFails(t *testing.T) {
	setupModifyDir(t, map[string]string{})

	_, failures, _ := SimulateApplyChanges([]Change{
		rep("ghost.txt", "a", "b"),
	})
	assertSingleFailure(t, failures, "ghost.txt", "does not exist")
}

// TestEmptyFileFails rejects locating blocks in an empty file.
func TestEmptyFileFails(t *testing.T) {
	setupModifyDir(t, map[string]string{"empty.txt": ""})

	_, failures, _ := SimulateApplyChanges([]Change{
		rep("empty.txt", "", "x"),
	})
	assertSingleFailure(t, failures, "empty.txt", "nothing to locate blocks against")
}

// TestReplaceEmptyOldFails: a replace whose old normalizes to nothing is a
// shape failure, not a whole-file match.
func TestReplaceEmptyOldFails(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb"})

	_, failures, _ := SimulateApplyChanges([]Change{
		rep("f.txt", "\n\n", "x"),
	})
	assertSingleFailure(t, failures, "f.txt", "'old' is required")
}

// ==================== handler-level: real system-modify, fake permission =====

func modifyArgs(file string, ch map[string]any) map[string]any {
	args := map[string]any{"file": file}
	for k, v := range ch {
		args[k] = v
	}
	return map[string]any{"changes": []any{args}}
}

// TestHandleModifyApprovedWritesSimulated runs the ACTUAL system-modify handler:
// asserts the diff viewport is populated and that approval writes the simulated
// (not raw) content.
func TestHandleModifyApprovedWritesSimulated(t *testing.T) {
	path := "h.txt"
	setupModifyDir(t, map[string]string{path: "a\nb\nc"})

	cap := &permCapture{}
	res := HandleToolCall(nil, nil, "system-modify", modifyArgs(path, map[string]any{
		"type":  "anchor",
		"start": "a",
		"end":   "c",
		"new":   "a\nB\nc",
	}), cap.fn(true), nil)

	if !res.Success {
		t.Fatalf("approved modify should succeed: %+v", res.Result)
	}
	// The permission viewport must carry a diff of the intended change.
	if cap.viewport == nil || !strings.Contains(StripAnsi(cap.viewport.Content), "-b") || !strings.Contains(StripAnsi(cap.viewport.Content), "+B") {
		t.Errorf("viewport should preview -b/+B, got %v", cap.viewport)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a\nB\nc" {
		t.Fatalf("file after modify = %q, want %q", got, "a\nB\nc")
	}
}

// TestHandleModifyDeniedLeavesFileUntouched asserts a denied permission applies
// nothing to disk.
func TestHandleModifyDeniedLeavesFileUntouched(t *testing.T) {
	path := "h.txt"
	setupModifyDir(t, map[string]string{path: "a\nb\nc"})

	cap := &permCapture{}
	res := HandleToolCall(nil, nil, "system-modify", modifyArgs(path, map[string]any{
		"type": "replace", "old": "b", "new": "B",
	}), cap.fn(false), nil)

	if res.Success {
		t.Fatalf("denied modify must fail: %+v", res.Result)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "a\nb\nc" {
		t.Fatalf("denied modify altered the file: %q", got)
	}
}

// TestHandleModifyBadShapeRejectsEarly asserts malformed changes fail with a
// clear message BEFORE any permission prompt.
func TestHandleModifyBadShapeRejectsEarly(t *testing.T) {
	setupModifyDir(t, map[string]string{"h.txt": "a\nb\nc"})

	cap := &permCapture{}

	// Missing type.
	res := HandleToolCall(nil, nil, "system-modify", modifyArgs("h.txt", map[string]any{
		"old": "b", "new": "B",
	}), cap.fn(true), nil)
	if res.Success {
		t.Fatalf("missing type must fail: %+v", res.Result)
	}

	// Bad type.
	res = HandleToolCall(nil, nil, "system-modify", modifyArgs("h.txt", map[string]any{
		"type": "patch", "old": "b", "new": "B",
	}), cap.fn(true), nil)
	if res.Success {
		t.Fatalf("bad type must fail: %+v", res.Result)
	}

	// 'old' must be a string.
	res = HandleToolCall(nil, nil, "system-modify", modifyArgs("h.txt", map[string]any{
		"type": "replace", "old": 42, "new": "B",
	}), cap.fn(true), nil)
	if res.Success {
		t.Fatalf("non-string old must fail: %+v", res.Result)
	}

	// 'start' must be a string (or null).
	res = HandleToolCall(nil, nil, "system-modify", modifyArgs("h.txt", map[string]any{
		"type": "anchor", "start": 42, "end": "c", "new": "x",
	}), cap.fn(true), nil)
	if res.Success {
		t.Fatalf("non-string start must fail: %+v", res.Result)
	}

	// 'end' must be a string (or null).
	res = HandleToolCall(nil, nil, "system-modify", modifyArgs("h.txt", map[string]any{
		"type": "anchor", "start": "a", "end": 42, "new": "x",
	}), cap.fn(true), nil)
	if res.Success {
		t.Fatalf("non-string end must fail: %+v", res.Result)
	}

	// 'new' must be a string.
	res = HandleToolCall(nil, nil, "system-modify", modifyArgs("h.txt", map[string]any{
		"type": "replace", "old": "b", "new": 42,
	}), cap.fn(true), nil)
	if res.Success {
		t.Fatalf("non-string new must fail: %+v", res.Result)
	}

	// null start/end are legal (file edges): this one must PASS.
	res = HandleToolCall(nil, nil, "system-modify", modifyArgs("h.txt", map[string]any{
		"type":  "anchor",
		"start": nil,
		"end":   nil,
		"new":   "a\nB\nc",
	}), cap.fn(true), nil)
	if !res.Success {
		t.Fatalf("null start/end should mean file edges and succeed: %+v", res.Result)
	}

	// permCapture retains the LAST prompt it saw: after all the rejects, the
	// only call that may have reached a permission prompt is the valid one.
	if cap.prompt != "Modify file: h.txt" {
		t.Errorf("the valid change must be the last to reach a permission prompt, got %q", cap.prompt)
	}
}

// TestHandleModifyAtomicOnPartialFailure verifies that a request with one bad
// change writes NOTHING to disk: modify is atomic per request.
func TestHandleModifyAtomicOnPartialFailure(t *testing.T) {
	setupModifyDir(t, map[string]string{
		"good.txt": "alpha\nbeta",
		"bad.txt":  "keep",
	})

	cap := &permCapture{}
	res := HandleToolCall(nil, nil, "system-modify", map[string]any{
		"changes": []any{
			map[string]any{"file": "good.txt", "type": "replace", "old": "alpha", "new": "ALPHA"},
			map[string]any{"file": "bad.txt", "type": "replace", "old": "stale-text", "new": "x"},
		},
	}, cap.fn(true), nil)

	if res.Success {
		t.Fatalf("modify with a failing change must fail: %+v", res.Result)
	}
	errText, _ := res.Result["error"].(string)
	if !strings.Contains(errText, "no changes were applied") {
		t.Errorf("failure should state that nothing was applied, got %q", errText)
	}

	for _, f := range []struct{ path, content string }{
		{"good.txt", "alpha\nbeta"},
		{"bad.txt", "keep"},
	} {
		got, rerr := os.ReadFile(f.path)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if string(got) != f.content {
			t.Errorf("%s was modified despite atomic failure: %q", f.path, got)
		}
	}
}

// --- small assertion helpers for the table above ---

func assertSingleSuccess(t *testing.T, got map[string]string, failures []ApplyChangesItemResult, file, want string) {
	t.Helper()
	if len(failures) != 0 {
		t.Fatalf("expected success, got failures: %+v", failures)
	}
	if len(got) != 1 || got[file] == "" {
		t.Fatalf("expected exactly one simulated file %q, got %v", file, got)
	}
	if got[file] != want {
		t.Errorf("%s = %q, want %q", file, got[file], want)
	}
}

func assertSingleFailure(t *testing.T, failures []ApplyChangesItemResult, file, substr string) {
	t.Helper()
	if len(failures) != 1 || !strings.Contains(failures[0].Error, substr) {
		t.Fatalf("expected a single failure for %q containing %q, got %+v", file, substr, failures)
	}
}

// ==================== Non-atomic mode: per-change partial application =======

func TestNonAtomicPartialSuccess(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb\nc\nd"})

	simulated, results, _ := SimulateApplyChangesNonAtomic([]Change{
		rep("f.txt", "a", "A1"),
		rep("f.txt", "c", "C1"),
		rep("f.txt", "nonexistent", "x"),
	})

	if len(simulated) != 1 {
		t.Fatalf("expected 1 file simulated, got %d", len(simulated))
	}
	if simulated["f.txt"] != "A1\nb\nC1\nd" {
		t.Errorf("simulated = %q, want %q", simulated["f.txt"], "A1\nb\nC1\nd")
	}

	// Per-change results: 3 entries (2 success, 1 failure).
	if len(results) != 3 {
		t.Fatalf("expected 3 per-change results, got %d: %+v", len(results), results)
	}
	if !results[0].Successful || results[0].ChangeNum != 1 {
		t.Errorf("change 1 should be successful, got %+v", results[0])
	}
	if !results[1].Successful || results[1].ChangeNum != 2 {
		t.Errorf("change 2 should be successful, got %+v", results[1])
	}
	if results[2].Successful || results[2].ChangeNum != 3 {
		t.Errorf("change 3 should be failed, got %+v", results[2])
	}
	if !strings.Contains(results[2].Error, "not found") {
		t.Errorf("change 3 error should mention not found, got %q", results[2].Error)
	}
}

func TestNonAtomicAllFail(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb"})

	simulated, results, _ := SimulateApplyChangesNonAtomic([]Change{
		rep("f.txt", "x1", "y"),
		rep("f.txt", "x2", "z"),
	})

	if len(simulated) != 0 {
		t.Fatalf("expected no simulated files, got %v", simulated)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 per-change results, got %d", len(results))
	}
	for i, r := range results {
		if r.Successful {
			t.Errorf("change %d should be failed, got %+v", i+1, r)
		}
	}
}

func TestNonAtomicOverlapRejection(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb\nc\nd"})

	simulated, results, _ := SimulateApplyChangesNonAtomic([]Change{
		rep("f.txt", "b\nc", "X"),
		anch("f.txt", "c", "d", "Y"),
		rep("f.txt", "a", "Z"),
	})

	if len(simulated) != 1 {
		t.Fatalf("expected 1 file simulated, got %d", len(simulated))
	}
	// Changes 1 and 2 overlap (both touch line c); change 3 (line a) is fine.
	want := "Z\nb\nc\nd"
	if simulated["f.txt"] != want {
		t.Errorf("simulated = %q, want %q", simulated["f.txt"], want)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	// Change 1 (b\nc) and change 2 (c-d) overlap: both rejected.
	if results[0].Successful {
		t.Errorf("change 1 should be rejected (overlap), got %+v", results[0])
	}
	if results[1].Successful {
		t.Errorf("change 2 should be rejected (overlap), got %+v", results[1])
	}
	// Change 3 (a) is independent: should succeed.
	if !results[2].Successful {
		t.Errorf("change 3 should succeed, got %+v", results[2])
	}
}

func TestAtomicDefaultUnchanged(t *testing.T) {
	setupModifyDir(t, map[string]string{"f.txt": "a\nb\nc"})

	// Atomic: one bad change aborts everything.
	simulated, results, _ := SimulateApplyChanges([]Change{
		rep("f.txt", "a", "A1"),
		rep("f.txt", "nonexistent", "x"),
	})

	if len(simulated) != 0 {
		t.Fatalf("atomic mode: expected no simulated files, got %v", simulated)
	}
	if len(results) != 1 || results[0].Successful {
		t.Fatalf("atomic mode: expected one failure, got %+v", results)
	}
}

// TestHandleModifyNonAtomicPartial runs the handler with nonAtomic=true and
// verifies that successful changes land on disk while failures are reported.
func TestHandleModifyNonAtomicPartial(t *testing.T) {
	path := "na.txt"
	setupModifyDir(t, map[string]string{path: "l1\nl2\nl3\nl4"})

	cap := &permCapture{}
	res := HandleToolCall(nil, nil, "system-modify", map[string]any{
		"nonAtomic": true,
		"changes": []any{
			map[string]any{"file": path, "type": "replace", "old": "l1", "new": "L1"},
			map[string]any{"file": path, "type": "replace", "old": "nonexistent", "new": "x"},
			map[string]any{"file": path, "type": "replace", "old": "l3", "new": "L3"},
		},
	}, cap.fn(true), nil)

	if !res.Success {
		t.Fatalf("non-atomic partial success should report success: %+v", res.Result)
	}
	status, _ := res.Result["status"].(string)
	if status != "partial" {
		t.Errorf("expected status=partial, got %q", status)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "L1\nl2\nL3\nl4"
	if string(got) != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}
