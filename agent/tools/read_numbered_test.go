package tools

import (
	"clankerv2/data"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mustIntPtr is a tiny helper for the pointer-typed read args.
func mustIntPtr(t *testing.T, v int) *int { t.Helper(); return &v }

// writeTestFile creates a file in a fresh temp dir and chdirs into it so
// relative paths behave like production (tools resolve against cwd).
func writeTestFile(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// numberedLine renders one expected canonical read line: "     N: text"; width
// derived from the total (so it matches the implementation's layout).
func numberedLine(n, total int, text string) string {
	return fmt.Sprintf("%*d: %s", numberWidth(total), n, text)
}
// TestSplitRecords checks the record model: N newlines => N records, plus one
// unterminated record only when there is no trailing newline.
func TestSplitRecords(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a\n", []string{"a"}},
		{"a", []string{"a"}},
		{"a\nb\nc\n", []string{"a", "b", "c"}},
		{"a\nb\nc", []string{"a", "b", "c"}},
		{"\n", []string{""}},
		{"a\n\nb\n", []string{"a", "", "b"}},
	}
	for _, c := range cases {
		got := splitRecords(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("splitRecords(%q) = %v (len %d), want %v (len %d)", c.in, got, len(got), c.want, len(c.want))
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Fatalf("splitRecords(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

// TestNumberingWidth checks the canonical "     N: text" rendering via
// renderChunk with a numbered format closure: right-justified at a minimum
// width of 6 (never shrinking below it), growing to fit the file's final line,
// and blank lines get their number too.
func TestNumberingWidth(t *testing.T) {
	num := func(total, firstLine int) func(i int, l string) string {
		return func(i int, l string) string { return fmt.Sprintf("%*d: %s", numberWidth(total), firstLine+i, l) }
	}

	got, _ := renderChunk([]string{"a", "", "c"}, num(12, 4), 1000000)
	want := strings.Join([]string{numberedLine(4, 12, "a"), numberedLine(5, 12, ""), numberedLine(6, 12, "c")}, "\n")
	if got != want {
		t.Fatalf("numbered render = %q, want %q", got, want)
	}
	// Width grows to fit the file's final line once it exceeds 6 chars.
	got, _ = renderChunk([]string{"a"}, num(98765, 12345), 1000000)
	want = numberedLine(12345, 98765, "a")
	if got != want {
		t.Fatalf("numbered wide = %q, want %q", got, want)
	}
	// The min-6 floor still wins for small files.
	got, _ = renderChunk([]string{"a"}, num(99, 12), 1000000)
	want = numberedLine(12, 99, "a")
	if got != want {
		t.Fatalf("numbered min-width = %q, want %q", got, want)
	}
}

// TestReadFileNumberedCore covers full/chunk reads, envelope fields, clamping,
// errors, and CRLF handling.
func TestReadFileNumberedCore(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	path := "sample.txt"
	// No trailing newline on purpose (the unterminated-record edge case).
	if err := os.WriteFile(path, []byte("alpha\nbravo\ncarlos\ndelta"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Full read: absolute numbers 1..4, min width 6.
	r := ReadFileNumbered(path, nil, nil, nil)
	if r.Status != "success" {
		t.Fatalf("full read failed: %+v", r)
	}
	want := strings.Join([]string{
		numberedLine(1, 4, "alpha"),
		numberedLine(2, 4, "bravo"),
		numberedLine(3, 4, "carlos"),
		numberedLine(4, 4, "delta"),
	}, "\n")
	if r.Content != want {
		t.Fatalf("full read content = %q, want %q", r.Content, want)
	}
	if r.TotalLines != 4 || r.StartLine != 1 || r.EndLine != 4 {
		t.Fatalf("full read envelope = total %d start %d end %d, want 4/1/4", r.TotalLines, r.StartLine, r.EndLine)
	}

	// Partial read: the chunk shows true ABSOLUTE numbers (re-based), not 1..n.
	r = ReadFileNumbered(path, mustIntPtr(t, 2), mustIntPtr(t, 2), nil)
	if r.Status != "success" {
		t.Fatalf("chunk read failed: %+v", r)
	}
	want = strings.Join([]string{numberedLine(2, 4, "bravo"), numberedLine(3, 4, "carlos")}, "\n")
	if r.Content != want {
		t.Fatalf("chunk content = %q, want %q", r.Content, want)
	}
	if r.TotalLines != 4 || r.StartLine != 2 || r.EndLine != 3 {
		t.Fatalf("chunk envelope = total %d start %d end %d, want 4/2/3", r.TotalLines, r.StartLine, r.EndLine)
	}

	// readLines past the tail clamps to the file end.
	r = ReadFileNumbered(path, mustIntPtr(t, 3), mustIntPtr(t, 10), nil)
	if r.Status != "success" {
		t.Fatalf("clamped chunk failed: %+v", r)
	}
	want = strings.Join([]string{numberedLine(3, 4, "carlos"), numberedLine(4, 4, "delta")}, "\n")
	if r.Content != want {
		t.Fatalf("clamped chunk = %q, want %q", r.Content, want)
	}

	// Out-of-range start errors (same message as legacy ReadFile).
	r = ReadFileNumbered(path, mustIntPtr(t, 5), nil, nil)
	if r.Status != "error" || !strings.Contains(r.Error, "exceeds file length") {
		t.Fatalf("out-of-range read = %+v, want exceeds-file-length error", r)
	}

	// Missing file errors.
	r = ReadFileNumbered("ghost.txt", nil, nil, nil)
	if r.Status != "error" || !strings.Contains(r.Error, "failed to read file") {
		t.Fatalf("missing file read = %+v, want read error", r)
	}

	// Empty file: success with zero lines.
	if err := os.WriteFile("empty.txt", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r = ReadFileNumbered("empty.txt", nil, nil, nil)
	if r.Status != "success" || r.TotalLines != 0 || r.Content != "" {
		t.Fatalf("empty file read = %+v, want success/0/empty", r)
	}

	// CRLF: content is stripped of \r and the flag is set.
	crlfPath := "win.txt"
	if err := os.WriteFile(crlfPath, []byte("one\r\ntwo\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = ReadFileNumbered(crlfPath, nil, nil, nil)
	if r.Status != "success" || !r.CRLF {
		t.Fatalf("crlf read = %+v, want success + crlf flag", r)
	}
	wantCRLF := strings.Join([]string{numberedLine(1, 2, "one"), numberedLine(2, 2, "two")}, "\n")
	if r.Content != wantCRLF {
		t.Fatalf("crlf content = %q, want %q (\\r must be stripped)", r.Content, wantCRLF)
	}

	// LF file: flag stays false.
	if err := os.WriteFile("lf.txt", []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := ReadFileNumbered("lf.txt", nil, nil, nil); r.Status != "success" || r.CRLF {
		t.Fatalf("lf read = %+v, want success without crlf flag", r)
	}
}

// TestReadFileNumberedWidthGrowth checks that a >999-line file keeps the same
// width for every page (width is derived from the TOTAL, not the chunk).
func TestReadFileNumberedWidthGrowth(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 1200; i++ {
		sb.WriteString("LLLL")
		sb.WriteString("\n")
	}
	path := writeTestFile(t, "big.txt", sb.String())

	// 1200 lines < 999999, so the min width of 6 still applies: first page.
	r := ReadFileNumbered(path, nil, mustIntPtr(t, 1), nil)
	if r.Status != "success" {
		t.Fatalf("head read failed: %+v", r)
	}
	if got, want := r.Content, numberedLine(1, 1200, "LLLL"); got != want {
		t.Fatalf("line 1 = %q, want %q (width derived from total)", got, want)
	}

	// Tail page uses the SAME width as the head page (6 here: 1200 < 999999).
	r = ReadFileNumbered(path, mustIntPtr(t, 1199), nil, nil)
	if r.Status != "success" {
		t.Fatalf("tail read failed: %+v", r)
	}
	want := strings.Join([]string{numberedLine(1199, 1200, "LLLL"), numberedLine(1200, 1200, "LLLL")}, "\n")
	if r.Content != want {
		t.Fatalf("tail = %q, want %q", r.Content, want)
	}

	// A genuinely >999999-line file is impractical; exercise the growth path
	// directly instead.
	if got := numberWidth(1234567); got != 7 {
		t.Fatalf("numberWidth(1234567) = %d, want 7", got)
	}
	if got, _ := renderChunk([]string{"x"}, func(i int, l string) string { return fmt.Sprintf("%*d: %s", numberWidth(9999999), 9999999+i, l) }, 1000000); got != numberedLine(9999999, 9999999, "x") {
		t.Fatalf("7-digit rendering = %q", got)
	}
}

// TestReadFileNumberedCap verifies the MaxFileLines constant bounds a chunk
// from BOTH the top (full read of an over-cap file) and the middle (a start
// line partway through a file longer than the cap).
func TestReadFileNumberedCap(t *testing.T) {
	var sb strings.Builder
	total := 2*MaxFileLines + 5 // long enough that a mid-start still exceeds the cap
	for i := 1; i <= total; i++ {
		sb.WriteString("ZZ")
		sb.WriteString("\n")
	}
	path := writeTestFile(t, "capped.txt", sb.String())

	// Top read: capped to the first MaxFileLines lines.
	r := ReadFileNumbered(path, nil, mustIntPtr(t, total), nil)
	if r.Status != "success" {
		t.Fatalf("top capped read failed: %+v", r)
	}
	if r.TotalLines != total || r.StartLine != 1 || r.EndLine != MaxFileLines {
		t.Fatalf("top cap envelope total %d start %d end %d, want %d/1/%d", r.TotalLines, r.StartLine, r.EndLine, total, MaxFileLines)
	}
	lines := strings.Split(r.Content, "\n")
	if len(lines) != MaxFileLines {
		t.Fatalf("top capped content has %d lines, want %d", len(lines), MaxFileLines)
	}
	first := numberedLine(1, total, "ZZ")
	last := numberedLine(MaxFileLines, total, "ZZ")
	if lines[0] != first || lines[len(lines)-1] != last {
		t.Fatalf("top capped bounds = %q / %q, want %q / %q", lines[0], lines[len(lines)-1], first, last)
	}

	// Mid read: start partway through; the chunk is still capped to MaxFileLines.
	r = ReadFileNumbered(path, mustIntPtr(t, 10), mustIntPtr(t, total), nil)
	if r.Status != "success" || r.StartLine != 10 || r.EndLine != 10+MaxFileLines-1 {
		t.Fatalf("mid cap envelope start %d end %d, want 10/%d", r.StartLine, r.EndLine, 10+MaxFileLines-1)
	}
	if got := strings.Count(r.Content, "\n"); got != MaxFileLines-1 {
		t.Fatalf("mid capped content has %d newlines (%d lines), want %d lines", got, got+1, MaxFileLines)
	}
}

// TestHandleSystemReadNumberedToggle covers the handler switch: numbered mode
// returns prefixed content + bounds fields; legacy mode returns plain content
// and no bounds fields.
func TestHandleSystemReadNumberedToggle(t *testing.T) {
	path := writeTestFile(t, "toggle.txt", "hello\nworld\n")

	prev := readNumbered
	t.Cleanup(func() { SetReadNumbered(prev) })

	SetReadNumbered(true)
	res := HandleToolCall(nil, nil, "system-read", map[string]any{"path": path}, nil, nil)
	if !res.Success {
		t.Fatalf("numbered read failed: %+v", res.Result)
	}
	// Numbered lines carry "N: text".
	if res.RawResult == nil || !strings.Contains(*res.RawResult, numberedLine(1, 2, "hello")) {
		t.Fatalf("numbered raw result = %q, want an '     1: hello' line", strPtr(res.RawResult))
	}
	if v := res.Result["start_line"]; v != float64(1) {
		t.Fatalf("start_line = %v, want 1", v)
	}

	SetReadNumbered(false)
	res = HandleToolCall(nil, nil, "system-read", map[string]any{"path": path}, nil, nil)
	if !res.Success {
		t.Fatalf("legacy read failed: %+v", res.Result)
	}
	// Legacy ReadFile's strings.Split model keeps the trailing empty element.
	if res.RawResult == nil || *res.RawResult != "hello\nworld\n" {
		t.Fatalf("legacy raw result = %q, want plain content (trailing newline preserved as read)", strPtr(res.RawResult))
	}
	if _, present := res.Result["start_line"]; present {
		t.Fatalf("legacy result must not carry start_line: %+v", res.Result)
	}
}

// strPtr dereferences a *string for readable failure messages (nil-safe).
func strPtr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// TestGetReadNumberedDefaults checks config resolution: nil-safe, default-on
// when any level is absent, explicit off honored.
func TestGetReadNumberedDefaults(t *testing.T) {
	off := false
	on := true
	cases := []struct {
		name string
		cfg  *data.AppConfig
		want bool
	}{
		{"nil config", nil, true},
		{"no system block", &data.AppConfig{}, true},
		{"system without read", &data.AppConfig{System: &data.SystemSettings{DefaultModel: "m"}}, true},
		{"read block without key", &data.AppConfig{System: &data.SystemSettings{Read: &data.ReadSettings{}}}, true},
		{"explicit off", &data.AppConfig{System: &data.SystemSettings{Read: &data.ReadSettings{Numbered: &off}}}, false},
		{"explicit on", &data.AppConfig{System: &data.SystemSettings{Read: &data.ReadSettings{Numbered: &on}}}, true},
	}
	for _, c := range cases {
		if got := c.cfg.GetReadNumbered(); got != c.want {
			t.Errorf("%s: GetReadNumbered = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestReadFileNumberedByteBudget verifies the byte cap stops a numbered read at
// the last WHOLE line that fits, sets Truncated, and reports the rendered count
// in EndLine. A nil budget leaves small files fully intact.
func TestReadFileNumberedByteBudget(t *testing.T) {
	path := writeTestFile(t, "budget.txt", "alpha\nbravo\ncarlos\ndelta\n")

	// Tight budget (27 bytes): lines 1-2 fit exactly, line 3 would blow it.
	r := ReadFileNumbered(path, nil, nil, mustIntPtr(t, 27))
	if r.Status != "success" {
		t.Fatalf("budget read failed: %+v", r)
	}
	want := strings.Join([]string{numberedLine(1, 4, "alpha"), numberedLine(2, 4, "bravo")}, "\n")
	if r.Content != want {
		t.Fatalf("budget content = %q, want %q", r.Content, want)
	}
	if !r.Truncated {
		t.Errorf("budget read not flagged truncated: %+v", r)
	}
	if r.TotalLines != 4 || r.StartLine != 1 || r.EndLine != 2 {
		t.Fatalf("budget envelope = total %d start %d end %d, want 4/1/2", r.TotalLines, r.StartLine, r.EndLine)
	}
	if r.FileBytes != len("alpha\nbravo\ncarlos\ndelta\n") {
		t.Errorf("file_bytes = %d, want the on-disk size", r.FileBytes)
	}

	// No budget (nil): all four lines render and no truncation.
	r = ReadFileNumbered(path, nil, nil, nil)
	if r.Truncated || r.TotalLines != 4 || r.StartLine != 1 || r.EndLine != 4 {
		t.Fatalf("nil-budget read should be whole: %+v", r)
	}

	// A budget larger than the file leaves it whole too.
	r = ReadFileNumbered(path, nil, nil, mustIntPtr(t, 10000))
	if r.Truncated || r.EndLine != 4 {
		t.Fatalf("large budget should not truncate: %+v", r)
	}

	// A budget smaller than even the first line still returns that one line and
	// marks truncated (never an empty read).
	r = ReadFileNumbered(path, nil, nil, mustIntPtr(t, 1))
	if !r.Truncated || r.Content != numberedLine(1, 4, "alpha") {
		t.Fatalf("tiny budget should return first line + truncation: %+v", r)
	}
}

// TestReadContentHash verifies the returned-content hash is stable across two
// identical reads, changes when content differs, and is empty for an empty file.
func TestReadContentHash(t *testing.T) {
	path := writeTestFile(t, "hash.txt", "one\ntwo\n")

	a := ReadFileNumbered(path, nil, nil, nil)
	b := ReadFileNumbered(path, nil, nil, nil)
	if a.ContentHash == "" {
		t.Fatalf("non-empty read has no content_hash: %+v", a)
	}
	if len(a.ContentHash) != 16 {
		t.Errorf("content_hash length = %d, want 16 hex chars", len(a.ContentHash))
	}
	if a.ContentHash != b.ContentHash {
		t.Errorf("identical reads disagree on hash: %q vs %q", a.ContentHash, b.ContentHash)
	}

	// A different read range yields different content and a different hash.
	c := ReadFileNumbered(path, mustIntPtr(t, 2), mustIntPtr(t, 1), nil)
	if c.ContentHash == "" || c.ContentHash == a.ContentHash {
		t.Errorf("different chunk should have its own non-empty distinct hash: %q (full=%q)", c.ContentHash, a.ContentHash)
	}

	// Empty file: no hash.
	empty := writeTestFile(t, "empty-hash.txt", "")
	if r := ReadFileNumbered(empty, nil, nil, nil); r.ContentHash != "" {
		t.Errorf("empty read should have no content_hash: %+v", r)
	}
}
