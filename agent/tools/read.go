package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type ReadFileResult struct {
	Status     string `json:"status"`
	Path       string `json:"path"`
	TotalLines int    `json:"total_lines"`
	// FileBytes is total on-disk size; use it to decide whether a read should be
	// paged or delegated to a subagent.
	FileBytes int `json:"file_bytes"`
	// True when content was cut by the byte budget, not the line-range caps.
	Truncated bool `json:"truncated,omitempty"`
	// ContentHash is the first 16 hex chars of SHA-256 over the returned content; equal
	// hashes between two reads mean that content is unchanged.
	ContentHash string `json:"content_hash,omitempty"`
	// StartLine is the 1-based number of the first line in Content (0 = nothing
	// returned). EndLine is the 1-based inclusive number of the last line.
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	CRLF      bool   `json:"crlf,omitempty"` // true when the file uses Windows line endings (stripped before numbering)
	Content   string `json:"content"`
	Error     string `json:"error,omitempty"`
}

func ReadFile(path string, line *int, readLines *int, maxBytes *int) *ReadFileResult {
	if path == "" {
		return &ReadFileResult{Status: "error", Error: "path is required"}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return &ReadFileResult{Status: "error", Path: path, Error: fmt.Sprintf("failed to read file: %v", err)}
	}
	if len(data) == 0 {
		return &ReadFileResult{Status: "success", Path: path, TotalLines: 0, Content: ""}
	}

	lines := strings.Split(string(data), "\n")
	totalLines := len(lines)

	start := 0
	if line != nil && *line > 0 {
		start = *line - 1
	}
	if start >= totalLines {
		return &ReadFileResult{Status: "error", Path: path, Error: fmt.Sprintf("requested start line %d exceeds file length %d", *line, totalLines)}
	}

	end := totalLines
	if readLines != nil && *readLines > 0 {
		end = start + *readLines
	}
	if end > totalLines {
		end = totalLines
	}

	if end-start > MaxFileLines {
		end = start + MaxFileLines
	}

	r := &ReadFileResult{Status: "success", Path: path, TotalLines: totalLines, FileBytes: len(data)}
	// Line-range / MaxFileLines caps stop before the tail by design; only the
	// byte budget marks content as cut short (Truncated).
	r.Content, r.Truncated = renderChunk(lines[start:end], func(i int, l string) string { return l }, resolveByteBudget(maxBytes))
	r.ContentHash = hashContent(r.Content)
	return r
}

	const numberWidthMin = 6

// Numbered read: absolute 1-based line numbers, right-justified to max(6, digits(total)) so width
// is stable across pages. CRLF is stripped per line before numbering and flagged via CRLF so a writer
// can restore line endings.
func ReadFileNumbered(path string, line *int, readLines *int, maxBytes *int) *ReadFileResult {
	if path == "" {
		return &ReadFileResult{Status: "error", Error: "path is required"}
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return &ReadFileResult{Status: "error", Path: path, Error: fmt.Sprintf("failed to read file: %v", err)}
	}
	// CRLF is a file property: probe raw bytes once, then number LF text.
	crlf := detectCRLF(raw)
	lines := splitRecords(stripCR(string(raw)))
	totalLines := len(lines)
	if totalLines == 0 {
		return &ReadFileResult{Status: "success", Path: path, TotalLines: 0, FileBytes: len(raw), Content: ""}
	}

	startIdx := 0 // 0-based index of the first returned line
	if line != nil && *line > 0 {
		startIdx = *line - 1
	}
	if startIdx >= totalLines {
		return &ReadFileResult{Status: "error", Path: path, Error: fmt.Sprintf("requested start line %d exceeds file length %d", *line, totalLines)}
	}

	endIdx := totalLines // exclusive
	if readLines != nil && *readLines > 0 {
		endIdx = startIdx + *readLines
	}
	if endIdx > totalLines {
		endIdx = totalLines
	}
	if endIdx-startIdx > MaxFileLines {
		endIdx = startIdx + MaxFileLines
	}

	r := &ReadFileResult{
		Status:     "success",
		Path:       path,
		TotalLines: totalLines,
		StartLine:  startIdx + 1,
		CRLF:       crlf,
		FileBytes:  len(raw),
	}
	chunk := lines[startIdx:endIdx]
	w := numberWidth(totalLines) // stable across pages: derived from the file's total
	r.Content, r.Truncated = renderChunk(chunk, func(i int, l string) string {
		return fmt.Sprintf("%*d: %s", w, startIdx+1+i, l)
	}, resolveByteBudget(maxBytes))
	// EndLine is the LAST LINE ACTUALLY RENDERED; the byte budget may cut the chunk short of endIdx.
	r.EndLine = startIdx + strings.Count(r.Content, "\n") + 1
	r.ContentHash = hashContent(r.Content)
	return r
}

func stripCR(s string) string { return strings.ReplaceAll(s, "\r", "") }

// splitRecords: N newlines => N records + one when no trailing newline. Differs from strings.Split
// which invents a phantom line for files ending in "\n"; TotalLines must match what editors/nl count.
func splitRecords(s string) []string {
	if s == "" {
		return nil
	}
	n := strings.Count(s, "\n")
	records := make([]string, 0, n+1)
	rest := s
	for i := 0; i < n; i++ {
		idx := strings.IndexByte(rest, '\n')
		records = append(records, rest[:idx])
		rest = rest[idx+1:]
	}
	if rest != "" {
		records = append(records, rest)
	}
	return records
}

func numberWidth(total int) int {
	w := len(strconv.Itoa(total))
	if w < numberWidthMin {
		w = numberWidthMin
	}
	return w
}

func resolveByteBudget(maxBytes *int) int {
	if maxBytes != nil && *maxBytes > 0 {
		return *maxBytes
	}
	return MaxReadBytes
}

func hashContent(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

// renderChunk joins lines (each through format), stopping at the last whole line that fits the budget —
// never splitting a line. The first line renders even if it alone exceeds the budget.
func renderChunk(lines []string, format func(i int, l string) string, budget int) (string, bool) {
	var sb strings.Builder
	truncated := false
	for i, l := range lines {
		lineStr := format(i, l)
		if i > 0 {
			lineCost := len(lineStr) + 1 // preceding newline
			if sb.Len()+lineCost > budget {
				truncated = true
				break
			}
			sb.WriteByte('\n')
		} else if len(lineStr) > budget {
			truncated = true
		}
		sb.WriteString(lineStr)
	}
	return sb.String(), truncated
}

func detectCRLF(raw []byte) bool { return bytes.Contains(raw, []byte("\r\n")) }

// countFileLines streams in 32KB chunks using splitRecords semantics so the listing's line count
// matches system-read. Returns 0 on open/read failure.
func countFileLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	newlines := 0
	nTotal := 0
	lastByte := byte('\n') // no data read: treat as ending in newline => 0 records
	buf := make([]byte, 32*1024)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			nTotal += n
			lastByte = buf[n-1]
		newlines += bytes.Count(buf[:n], []byte{'\n'})
		}
		if rerr != nil {
			break
		}
	}
	if nTotal == 0 || lastByte == '\n' {
		return newlines
	}
	return newlines + 1
}
