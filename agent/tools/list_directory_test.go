package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// writeListFixture lays out a small tree in a fresh temp dir and chdirs into it:
//   - visible.txt  (3 lines, trailing newline)
//   - noNewline.txt (2 lines, NO trailing newline -> still counts as 2 records)
//   - .hidden.txt  (1 line)
//   - .hiddendir/inner.txt (1 line)
//   - subdir/deep.txt      (2 lines)
func writeListFixture(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"visible.txt":     "a\nb\nc\n",
		"noNewline.txt":   "x\ny",
		".hidden.txt":     "h\n",
		".hiddendir/inner.txt": "i\n",
		"subdir/deep.txt":  "d1\nd2\n",
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func findByPath(entries []DirEntry, name string) *DirEntry {
	for i := range entries {
		if entries[i].Path == name || filepath.Base(entries[i].Path) == name {
			return &entries[i]
		}
	}
	return nil
}

// TestListDirectoryLineCounts verifies each file entry reports a line count that
// matches system-read's record semantics (N newlines => N records, +1 only when
// there is no trailing newline), and directories report none.
func TestListDirectoryLineCounts(t *testing.T) {
	writeListFixture(t)

	res := ListDirectory(".", nil, nil, nil)
	if res.Status != "success" {
		t.Fatalf("list failed: %+v", res)
	}

	vis := findByPath(res.Entries, "visible.txt")
	if vis == nil || vis.Lines != 3 {
		t.Errorf("visible.txt lines = %v, want 3", vis)
	}
	noNL := findByPath(res.Entries, "noNewline.txt")
	if noNL == nil || noNL.Lines != 2 {
		t.Errorf("noNewline.txt lines = %v, want 2 (unterminated record counts)", noNL)
	}

	// Directory entries carry a size of 0 and no line count.
	sub := findByPath(res.Entries, "subdir")
	if sub == nil || !sub.IsDir {
		t.Fatalf("subdir missing or not flagged as dir: %+v", sub)
	}
	if sub.Lines != 0 {
		t.Errorf("subdir lines = %d, want 0 for directories", sub.Lines)
	}
}

// TestListDirectoryShowHidden verifies hidden files and dot-directories are
// skipped by default but included when showHidden is true. It also covers the
// root-dotdir case: listing a dot-directory directly previously returned empty.
func TestListDirectoryShowHidden(t *testing.T) {
	writeListFixture(t)

	// Default: hidden entries absent, visible ones present.
	res := ListDirectory(".", nil, nil, nil)
	if findByPath(res.Entries, ".hidden.txt") != nil {
		t.Errorf("default list should hide .hidden.txt: %+v", res.Entries)
	}
	if findByPath(res.Entries, "inner.txt") != nil {
		t.Errorf("default list should skip into hidden dir: %+v", res.Entries)
	}
	if findByPath(res.Entries, "visible.txt") == nil {
		t.Errorf("default list should still show visible.txt: %+v", res.Entries)
	}

	// showHidden=true: hidden entries appear. (Non-recursive listing never
	// descends into subdirectories, so inner.txt is reachable only recursively.)
	on := true
	res = ListDirectory(".", nil, nil, &on)
	if findByPath(res.Entries, ".hidden.txt") == nil {
		t.Errorf("showHidden list missing .hidden.txt: %+v", res.Entries)
	}
	rec := true
	res = ListDirectory(".", &rec, nil, &on)
	if findByPath(res.Entries, "inner.txt") == nil {
		t.Errorf("recursive showHidden list missing hidden-dir inner.txt: %+v", res.Entries)
	}

	// Listing a dot-directory directly must now work under showHidden.
	res = ListDirectory(".hiddendir", nil, nil, &on)
	if res.Status != "success" {
		t.Fatalf("list .hiddendir failed: %+v", res)
	}
	if findByPath(res.Entries, "inner.txt") == nil {
		t.Errorf("listing .hiddendir should yield inner.txt: %+v", res.Entries)
	}

	// Same dot-directory WITHOUT showHidden returns empty (root matches the skip).
	res = ListDirectory(".hiddendir", nil, nil, nil)
	if len(res.Entries) != 0 {
		t.Errorf("listing .hiddendir without showHidden should be empty, got %+v", res.Entries)
	}
}

// TestCountFileLines exercises the streaming counter's record semantics directly.
func TestCountFileLines(t *testing.T) {
	cases := []struct{ name, content string; want int }{
		{"empty", "", 0},
		{"one-newline", "\n", 1},
		{"newline-terminated", "a\nb\nc\n", 3},
		{"no-trailing-newline", "a\nb\nc", 3},
		{"single-no-nl", "x", 1},
	}
	for _, c := range cases {
		p := writeTestFile(t, "cnt-"+c.name+".txt", c.content)
		if got := countFileLines(p); got != c.want {
			t.Errorf("countFileLines(%q) = %d, want %d", c.name, got, c.want)
		}
	}

	// A file larger than the 32KB read buffer exercises the chunk boundary.
	var big string
	for i := 0; i < 10000; i++ {
		big += "0123456789\n" // 11 bytes each => ~110KB, well over one chunk
	}
	p := writeTestFile(t, "cnt-big.txt", big)
	if got := countFileLines(p); got != 10000 {
		t.Errorf("countFileLines(big) = %d, want 10000", got)
	}

	// Missing file degrades to 0 rather than erroring.
	if got := countFileLines(filepath.Join(t.TempDir(), "nope.txt")); got != 0 {
		t.Errorf("countFileLines(missing) = %d, want 0", got)
	}
}
