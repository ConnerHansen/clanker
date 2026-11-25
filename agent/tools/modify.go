package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ApplyChangesItemResult is the per-change (or per-file, in atomic mode)
// outcome of a system-modify request.
type ApplyChangesItemResult struct {
	File       string `json:"file"`
	ChangeNum  int    `json:"change_num,omitempty"`
	Successful bool   `json:"successful"`
	Lines      string `json:"lines,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ApplyChangesResponse is the structured result of writing pre-simulated edits.
type ApplyChangesResponse struct {
	Status  string                   `json:"status"`
	Results []ApplyChangesItemResult `json:"results"`
	Error   string                   `json:"error,omitempty"`
}

// Change is one content-anchored edit (no line numbers): regions are located by
// matching raw block content against the file's CURRENT lines, so earlier changes
// in a request never invalidate later ones.
//
//	Type "replace": find-and-replace; Old must match exactly once and is swapped for New
//	("") deletes it).
//
//	Type "anchor": replace the INCLUSIVE span from Start to End with New. Start/End are raw
//	multi-line blocks, each matching exactly once; a kept boundary line must appear verbatim in
//	New. Empty Start = line 1, empty End = EOF, both empty replaces the whole file.
//
// Matching is record-based (real newlines only) and two-tier: strict per-line equality,
// then per-line trimmed + whitespace-collapsed when strict finds nothing (tab/space drift
// tolerated, noted in the result). Blocks in one file must not overlap; new files need system-write.
type Change struct {
	File  string `json:"file"`
	Type  string `json:"type"` // "replace" or "anchor"
	Old   string `json:"old,omitempty"`   // replace: exact block to find (matches exactly once)
	Start string `json:"start,omitempty"` // anchor: first block (inclusive); "" = line 1
	End   string `json:"end,omitempty"`   // anchor: last block (inclusive); "" = end of file
	New   string `json:"new"`             // replacement content for the located region ("" deletes)
}

// locatedChange is a change resolved against the file's ORIGINAL lines: its 0-based
// region [start, end) plus replacement lines (nil deletes). Resolving every change
// against one snapshot means no edit can invalidate another.
type locatedChange struct {
	num     int
	start   int      // 0-based first replaced line
	end     int      // one past the last replaced line
	newLns  []string // replacement lines; nil deletes
	lenient bool     // block located via the whitespace-insensitive tier
}

// SimulateApplyChanges calculates what each file would look like after its
// changes are applied, per file. It is a pure simulation: nothing is written.
// Atomic mode: every failing change aborts its file; the map returned third
// carries fresh numbered context around each splice for successful files.
func SimulateApplyChanges(changes []Change) (map[string]string, []ApplyChangesItemResult, map[string]string) {
	return simulateApplyChangesImpl(changes, false)
}

// SimulateApplyChangesNonAtomic is like SimulateApplyChanges but each change resolves independently:
// successes land, failures are reported per-change without rolling back.
func SimulateApplyChangesNonAtomic(changes []Change) (map[string]string, []ApplyChangesItemResult, map[string]string) {
	return simulateApplyChangesImpl(changes, true)
}

func simulateApplyChangesImpl(changes []Change, nonAtomic bool) (map[string]string, []ApplyChangesItemResult, map[string]string) {
	fileChanges := make(map[string][]Change)
	var order []string
	for _, c := range changes {
		if _, seen := fileChanges[c.File]; !seen {
			order = append(order, c.File)
		}
		fileChanges[c.File] = append(fileChanges[c.File], c)
	}

	simulated := make(map[string]string)
	contexts := make(map[string]string)
	var results []ApplyChangesItemResult
	for _, file := range order {
		content, ctx, perChange, err := simulateFile(file, fileChanges[file], nonAtomic)
		if err != nil {
			if nonAtomic && len(perChange) > 0 {
				results = append(results, perChange...)
			} else {
				results = append(results, ApplyChangesItemResult{File: file, Successful: false, Error: err.Error()})
			}
			continue
		}
		simulated[file] = content
		contexts[file] = ctx
		if nonAtomic && len(perChange) > 0 {
			results = append(results, perChange...)
		}
	}
	return simulated, results, contexts
}

// simulateFile resolves and applies one file's changes against a single snapshot of its original lines;
// atomic mode aborts the whole file on any failure, non-atomic lands each success independently.
// Edits are line-span based (terminator-free) and rejoin restores trailing-newline-ness plus CRLF.
func simulateFile(file string, changes []Change, nonAtomic bool) (string, string, []ApplyChangesItemResult, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", "", nil, fmt.Errorf("file %q does not exist or cannot be read; system-modify requires an existing file (use system-write to create new files): %v", file, err)
	}
	// CRLF is normalized away so blocks match the same way numbered reads do;
	// it is restored before writing so line endings are preserved.
	crlf := detectCRLF(raw)
	lines := splitRecords(stripCR(string(raw)))
	if len(lines) == 0 {
		return "", "", nil, fmt.Errorf("file %q is empty; nothing to locate blocks against (use system-write to create content)", file)
	}

	if !nonAtomic {
		return simulateFileAtomic(changes, lines, crlf, raw)
	}
	return simulateFileNonAtomic(file, changes, lines, crlf, raw)
}

// simulateFileAtomic is the all-or-nothing path: every change must resolve or
// the file is untouched.
func simulateFileAtomic(changes []Change, lines []string, crlf bool, raw []byte) (string, string, []ApplyChangesItemResult, error) {
	located := make([]locatedChange, 0, len(changes))
	for i, c := range changes {
		lc, err := resolveChange(i+1, c, lines)
		if err != nil {
			return "", "", nil, err
		}
		located = append(located, lc)
	}

	text, ctx, err := spliceAndRender(located, lines, crlf, raw)
	if err != nil {
		return "", "", nil, err
	}
	return text, ctx, nil, nil
}

// simulateFileNonAtomic resolves each change independently; successful changes
// splice, failures are reported per-change without rolling back successes.
func simulateFileNonAtomic(file string, changes []Change, lines []string, crlf bool, raw []byte) (string, string, []ApplyChangesItemResult, error) {
	results := make([]ApplyChangesItemResult, len(changes))
	for i := range results {
		results[i] = ApplyChangesItemResult{File: file, ChangeNum: i + 1, Successful: true}
	}

	var located []locatedChange
	for i, c := range changes {
		lc, err := resolveChange(i+1, c, lines)
		if err != nil {
			results[i] = ApplyChangesItemResult{File: file, ChangeNum: i + 1, Successful: false, Error: err.Error()}
			continue
		}
		located = append(located, lc)
	}

	if len(located) == 0 {
		return "", "", results, fmt.Errorf("all %d change(s) failed to resolve", len(changes))
	}

	// Overlap detection on the successful subset: both members of an overlapping
	// pair are rejected.
	slices.SortStableFunc(located, func(a, b locatedChange) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return a.end - b.end
	})
	overlapNums := make(map[int]bool)
	for i := 1; i < len(located); i++ {
		if located[i].start < located[i-1].end {
			overlapNums[located[i-1].num] = true
			overlapNums[located[i].num] = true
		}
	}

	var surviving []locatedChange
	for _, lc := range located {
		if overlapNums[lc.num] {
			results[lc.num-1] = ApplyChangesItemResult{File: file, ChangeNum: lc.num, Successful: false,
				Error: fmt.Sprintf("overlaps with another change in the same file; both rejected (%s)", regionDesc(lc))}
			continue
		}
		surviving = append(surviving, lc)
	}

	if len(surviving) == 0 {
		return "", "", results, fmt.Errorf("all changes rejected (overlap)")
	}

	text, ctx, err := spliceAndRender(surviving, lines, crlf, raw)
	if err != nil {
		return "", "", results, err
	}

	// Annotate successful results with their new-file line ranges.
	for _, lc := range surviving {
		if len(lc.newLns) > 0 {
			results[lc.num-1].Lines = fmt.Sprintf("%d-%d", lc.start+1, lc.start+len(lc.newLns))
		}
	}

	return text, ctx, results, nil
}

// spliceAndRender performs the bottom-up splice of located changes and renders
// the new file text plus edit context. Shared by atomic and non-atomic paths.
func spliceAndRender(located []locatedChange, lines []string, crlf bool, raw []byte) (string, string, error) {
	// Stable sort keeps the model's argument order deterministic.
	slices.SortStableFunc(located, func(a, b locatedChange) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return a.end - b.end
	})

	// Safety-net overlap check (the non-atomic path pre-filters; this catches
	// any residual overlap in the atomic path).
	for i := 1; i < len(located); i++ {
		if located[i].start < located[i-1].end {
			return "", "", fmt.Errorf("changes %d and %d overlap in the file (%s vs %s); split them into non-overlapping blocks",
				located[i-1].num, located[i].num,
				regionDesc(located[i-1]), regionDesc(located[i]))
		}
	}

	// Splice bottom-up so every splice sees line coordinates that are still valid (a lower edit
	// cannot shift higher content).
	out := append([]string(nil), lines...)
	newStarts := make([]int, 0, len(located)) // NEW 0-based first line per splice
	for i := len(located) - 1; i >= 0; {
		k := located[i].start
		groupEnd := i
		for j := i; j >= 0 && located[j].start == k; j-- {
			out = spliceLines(out, located[j].start, located[j].end, located[j].newLns)
			groupEnd = j
		}
		newStarts = append(newStarts, k)
		i = groupEnd - 1
	}
	// newStarts collected bottom-up; the context renderer sorts by position.
	slices.Reverse(newStarts)

	newText := strings.Join(out, "\n")
	if len(out) > 0 && rawHasTrailingNewline(stripCR(string(raw))) {
		newText += "\n" // preserve the original trailing newline
	}
	if crlf {
		newText = strings.ReplaceAll(newText, "\n", "\r\n")
	}

	ctx := renderEditContext(out, newStarts)
	var lenientNums []string
	for _, lc := range located {
		if lc.lenient {
			lenientNums = append(lenientNums, strconv.Itoa(lc.num))
		}
	}
	if len(lenientNums) > 0 {
		ctx += "\n(note: change " + strings.Join(lenientNums, ", ") + " located via whitespace-insensitive matching — indentation/tabs were normalized, content matched case-sensitively)"
	}
	return newText, ctx, nil
}

// rawHasTrailingNewline reports whether the (CR-stripped) text ends in a
// newline, i.e. its last record is terminated.
func rawHasTrailingNewline(s string) bool {
	return strings.HasSuffix(s, "\n")
}

// renderEditContext renders a compact numbered snippet around each splice in the NEW file
// (2 lines each side, "N: text") plus the total line count — fresh context for follow-up edits.
func renderEditContext(lines []string, starts []int) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%d lines total after edit; context (2 around each splice):\n", len(lines)))
	type win struct{ lo, hi int }
	windows := make([]win, 0, len(starts))
	for _, s := range starts {
		lo := s - 2
		if lo < 0 {
			lo = 0
		}
		hi := s + 3 // up to 2 lines after (spliced region may be several lines)
		if hi > len(lines) {
			hi = len(lines)
		}
		windows = append(windows, win{lo: lo, hi: hi})
	}
	// Merge overlapping/adjacent windows so repeated context is not shown twice.
	var merged []win
	for _, w := range windows {
		if len(merged) > 0 && w.lo <= merged[len(merged)-1].hi {
			if w.hi > merged[len(merged)-1].hi {
				merged[len(merged)-1].hi = w.hi
			}
		} else {
			merged = append(merged, w)
		}
	}
	for _, w := range merged {
		for n := w.lo; n < w.hi; n++ {
			b.WriteString(fmt.Sprintf("%6d: %s\n", n+1, lines[n]))
		}
		b.WriteString("...\n")
	}
	return b.String()
}

// spliceLines returns s with [start, end) replaced by ins. start==end inserts
// before index start; empty ins deletes the span.
func spliceLines(s []string, start, end int, ins []string) []string {
	out := make([]string, 0, len(s)-end+start+len(ins))
	out = append(out, s[:start]...)
	out = append(out, ins...)
	out = append(out, s[end:]...)
	return out
}

// regionDesc renders a resolved region for overlap errors.
func regionDesc(lc locatedChange) string {
	return fmt.Sprintf("lines %d-%d", lc.start+1, lc.end)
}

// blockSpan is one located occurrence of a block against the file's lines.
type blockSpan struct {
	start   int  // 0-based first matched line
	end     int  // one past the last matched line
	lenient bool // found by the whitespace-insensitive tier
}

// normalizeBlock prepares model-supplied block content for matching: CR is dropped,
// one optional trailing newline and leading/trailing blanks are stripped (common LLM
// padding); interior blanks preserved, "" normalizes to no lines.
func normalizeBlock(s string) []string {
	s = strings.TrimSuffix(stripCR(s), "\n")
	lines := splitRecords(s)
	lo, hi := 0, len(lines)
	for lo < hi && strings.TrimSpace(lines[lo]) == "" {
		lo++
	}
	for hi > lo && strings.TrimSpace(lines[hi-1]) == "" {
		hi--
	}
	return lines[lo:hi]
}

// tier2Line renders one line for lenient comparison: trimmed, with internal
// whitespace runs collapsed to a single space. Case is NOT folded — a case
// difference is still a mismatch; only indentation and spacing drift is not.
func tier2Line(l string) string {
	return strings.Join(strings.Fields(l), " ")
}

// matchBlock finds every occurrence of block within lines: strict per-line equality first,
// then (only if that yields nothing) tier 2 compares trimmed + whitespace-collapsed content
// with the same ordered alignment. The bool reports whether a lenient-tier match was found.
func matchBlock(lines []string, block []string) ([]blockSpan, bool) {
	n, m := len(lines), len(block)
	if n == 0 || m == 0 || m > n {
		return nil, false
	}
	scan := func(srcL, srcB []string, lenient bool) []blockSpan {
		var spans []blockSpan
		for i := 0; i+m <= n; i++ {
			ok := true
			for j := 0; j < m; j++ {
				if srcL[i+j] != srcB[j] {
					ok = false
					break
				}
			}
			if ok {
				spans = append(spans, blockSpan{start: i, end: i + m, lenient: lenient})
			}
		}
		return spans
	}

	if spans := scan(lines, block, false); len(spans) > 0 {
		return spans, false
	}
	lines2 := make([]string, n)
	for i, l := range lines {
		lines2[i] = tier2Line(l)
	}
	block2 := make([]string, m)
	for j, l := range block {
		block2[j] = tier2Line(l)
	}
	spans := scan(lines2, block2, true)
	return spans, len(spans) > 0
}

// collapseRuns collapses runs of a repeated character into one. A diagnostic
// fingerprint only: two texts sharing a collapsed signature but differing by such a
// run point at exactly that class of drift (the LLM's most frequent byte-level slip).
func collapseRuns(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	var last rune
	for i := 0; i < len(s); {
		r, sz := utf8.DecodeRuneInString(s[i:])
		if r != last {
			b.WriteRune(r)
			last = r
		}
		i += sz
	}
	return b.String()
}

// nearMissSpans reports file spans equal to the block once both are collapsed — candidates for
// a repeated-character drift. Capped, and empty when nothing resembles the block.
func nearMissSpans(lines []string, block []string) []blockSpan {
	n, m := len(lines), len(block)
	if n == 0 || m == 0 || m > n {
		return nil
	}
	var cached [2][]string // 0 = collapsed file lines, 1 = collapsed block (lazy)
	getCollapsed := func(kind int) []string {
		if cached[kind] != nil {
			return cached[kind]
		}
		count, src := n, lines
		if kind == 1 {
			count, src = m, block
		}
		out := make([]string, count)
		for i, l := range src {
			out[i] = collapseRuns(l)
		}
		cached[kind] = out
		return out
	}
	fl, bl := getCollapsed(0), getCollapsed(1)
	var spans []blockSpan
	for i := 0; i+m <= n && len(spans) < 6; i++ { // collect a few, trim to the hint cap below
		ok := true
		for j := 0; j < m; j++ {
			if fl[i+j] != bl[j] {
				ok = false
				break
			}
		}
		if ok {
			spans = append(spans, blockSpan{start: i, end: i + m})
		}
	}
	if len(spans) > 3 {
		spans = spans[:3]
	}
	return spans
}

// uniqueMatch requires exactly one occurrence. Multi-match lists up to three candidate
// ranges; when nothing matches, a near-miss diagnostic quotes the ACTUAL on-disk text of any
// span differing only by repeated-character runs.
func uniqueMatch(what string, num int, lines []string, rawBlock []string) (blockSpan, error) {
	spans, _ := matchBlock(lines, rawBlock)
	switch len(spans) {
	case 0:
		errMsg := fmt.Sprintf("change %d: %s not found in the file (strictly, and with whitespace-insensitive matching); re-read the region and copy it exactly", num, what)
		if nn := nearMissSpans(lines, rawBlock); len(nn) > 0 {
			parts := make([]string, 0, len(nn))
			for _, s := range nn {
				parts = append(parts, fmt.Sprintf("line %d: %s", s.start+1, capRunes(strings.Join(lines[s.start:s.end], " / "), 120)))
			}
			errMsg += "; the block resembles the file at " + strings.Join(parts, "; ") + " — copy that text verbatim (compare repeated-character counts at that line)"
		}
		return blockSpan{}, errors.New(errMsg)
	case 1:
		return spans[0], nil
	default:
		cands := spans
		if len(cands) > 3 {
			cands = cands[:3]
		}
		parts := make([]string, len(cands))
		for i, s := range cands {
			parts[i] = fmt.Sprintf("lines %d-%d", s.start+1, s.end)
		}
		return blockSpan{}, fmt.Errorf("change %d: %s matched %d times (%s); extend it with more surrounding lines so exactly one match remains", num, what, len(spans), strings.Join(parts, ", "))
	}
}

// resolveChange validates one change and resolves it to a line region against
// the original snapshot. Every failure message names the exact problem (block
// not found, ambiguous match, inverted anchors) so the model can re-read the
// region and retry with sharper blocks.
func resolveChange(num int, c Change, lines []string) (locatedChange, error) {
	// Replacement content: one optional trailing newline is dropped; "" means
	// delete the located region. Interior blanks are preserved as-is.
	var newLns []string
	if trimmed := strings.TrimSuffix(c.New, "\n"); trimmed != "" {
		newLns = splitRecords(trimmed)
	}

	switch c.Type {
	case "replace":
		old := normalizeBlock(c.Old)
		if len(old) == 0 {
			return locatedChange{}, fmt.Errorf("change %d: 'old' is required for the replace shape (the exact block to find)", num)
		}
		sp, err := uniqueMatch("'old' block", num, lines, old)
		if err != nil {
			return locatedChange{}, err
		}
		if isNoOp(lines[sp.start:sp.end], newLns) {
			return locatedChange{}, fmt.Errorf("change %d: 'new' reproduces lines %d-%d unchanged (a no-op); drop the change or alter the content", num, sp.start+1, sp.end)
		}
		return locatedChange{num: num, start: sp.start, end: sp.end, newLns: newLns, lenient: sp.lenient}, nil

	case "anchor":
		totalLines := len(lines)
		lenient := false
		regionStart := 0 // empty start block = line 1
		if c.Start != "" {
			startBlock := normalizeBlock(c.Start)
			if len(startBlock) == 0 {
				return locatedChange{}, fmt.Errorf("change %d: 'start' is blank after trimming; pass a real block (or null to begin at line 1)", num)
			}
			sp, err := uniqueMatch("'start' anchor", num, lines, startBlock)
			if err != nil {
				return locatedChange{}, err
			}
			regionStart = sp.start
			lenient = sp.lenient
		}
		regionEnd := totalLines // empty end block = run to the last line
		if c.End != "" {
			endBlock := normalizeBlock(c.End)
			if len(endBlock) == 0 {
				return locatedChange{}, fmt.Errorf("change %d: 'end' is blank after trimming; pass a real block (or null to run to end of file)", num)
			}
			sp, err := uniqueMatch("'end' anchor", num, lines, endBlock)
			if err != nil {
				return locatedChange{}, err
			}
			regionEnd = sp.end
			lenient = lenient || sp.lenient
		}
		if regionEnd <= regionStart {
			return locatedChange{}, fmt.Errorf("change %d: anchors are inverted — the 'start' block matched at line %d, at or after the 'end' block's last line", num, regionStart+1)
		}
		if isNoOp(lines[regionStart:regionEnd], newLns) {
			return locatedChange{}, fmt.Errorf("change %d: 'new' reproduces lines %d-%d unchanged (a no-op); drop the change or alter the content", num, regionStart+1, regionEnd)
		}
		return locatedChange{num: num, start: regionStart, end: regionEnd, newLns: newLns, lenient: lenient}, nil

	default:
		return locatedChange{}, fmt.Errorf("change %d: unknown type %q (must be \"replace\" or \"anchor\")", num, c.Type)
	}
}

// isNoOp reports whether replacing region with newLns would change nothing.
// Lengths must match first so deleting a single blank line does not masquerade
// as reproducing an empty region.
func isNoOp(region, newLns []string) bool {
	if len(region) != len(newLns) {
		return false
	}
	for i := range region {
		if region[i] != newLns[i] {
			return false
		}
	}
	return true
}

// ApplyChanges writes pre-simulated contents to disk (creating parent dirs). It never re-resolves:
// callers pass SimulateApplyChanges's output so what lands is exactly what was approved.
// After each write it re-reads and byte-compares; any mismatch restores the previous
// bytes and fails, so a caller can never believe an edit landed that did not.
func ApplyChanges(simulated map[string]string) *ApplyChangesResponse {
	results := make([]ApplyChangesItemResult, 0, len(simulated))
	hasError := false

	for file, newContent := range simulated {
		oldContent, _ := os.ReadFile(file)
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			results = append(results, ApplyChangesItemResult{File: file, Successful: false, Error: fmt.Sprintf("failed to create directories: %v", err)})
			hasError = true
			continue
		}
		if err := os.WriteFile(file, []byte(newContent), 0644); err != nil {
			results = append(results, ApplyChangesItemResult{File: file, Successful: false, Error: fmt.Sprintf("write failed: %v", err)})
			hasError = true
			continue
		}
		written, rerr := os.ReadFile(file)
		if rerr != nil || string(written) != newContent {
			// The bytes on disk are not what was approved: roll back and fail.
			_ = os.WriteFile(file, oldContent, 0644)
			errMsg := "content verification failed — file restored to its previous state (re-read the file and retry)"
			if rerr != nil {
				errMsg = fmt.Sprintf("post-write read failed (%v); file restored to its previous state", rerr)
			}
			results = append(results, ApplyChangesItemResult{File: file, Successful: false, Error: errMsg})
			hasError = true
			continue
		}
		results = append(results, ApplyChangesItemResult{File: file, Successful: true})
	}

	status := "success"
	if hasError {
		status = "error"
	}

	return &ApplyChangesResponse{Status: status, Results: results}
}
