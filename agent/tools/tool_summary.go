package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// summaryLineLimit caps these lines: they are concatenated into transcripts
// fed to the summarizer model and shown inline in the UI, so they must stay
// one-line short regardless of how verbose a tool result is.
var summaryLineLimit = 200

// SummarizeTool produces a tool-defined one-line self-summary for a completed
// call: what was done plus the outcome — never the content itself. File reads
// report only the path, file writes report the target and how it went,
// modifications report every requested file with its result, searches/listings
// report the query and a match count. Unregistered tools (or malformed
// payloads) degrade to the generic fallback so nothing is ever blank.
//
// The success bool is the authoritative outcome signal: handlers derive it from
// the structured result's status at save time, and stored rows persist it in
// their envelope. It is NOT re-derived from `result` here because a stored row's
// result can be an unparseable blob (e.g. raw file content for reads), which
// would otherwise misclassify a successful call as failed.
func SummarizeTool(name string, paramsJSON string, result map[string]any, success bool) string {
	summary, ok := toolSummarizers[name]
	if !ok {
		return capSummary(fallbackToolSummary(name, result, success))
	}

	var params map[string]any
	if paramsJSON != "" {
		if err := json.Unmarshal([]byte(paramsJSON), &params); err != nil || params == nil {
			params = map[string]any{}
		}
	} else {
		params = map[string]any{}
	}

	return capSummary(summary(name, params, result, success))
}

// toolSummarizer builds the raw (uncapped) one-line summary for a tool.
type toolSummarizer func(name string, params map[string]any, result map[string]any, success bool) string

// toolSummarizers registers how each tool describes itself to downstream
// consumers (UI previews, session summarizer, memory extraction). Adding a new
// tool with its own summary is a single line here.
var toolSummarizers = map[string]toolSummarizer{
	"system-read":           summarizeRead,
	"system-write":          summarizeWrite,
	"system-delete":         summarizeDelete,
	"system-modify":         summarizeModify,
	"system-find":           summarizeFind,
	"system-list":           summarizeList,
	"system-search_by_name": summarizeSearchByName,
	"system-build":          summarizeBuild,
	"system-tests":          summarizeTests,
	"system-ask":            summarizeAsk,
	"system-register_plan":  summarizeRegisterPlan,
	"system-compact":        summarizeCompact,
	"system-register_compaction": summarizeRegisterCompaction,
}

// --- helpers ---

func strParam(params map[string]any, key string) string {
	s, _ := params[key].(string)
	return s
}

func strResult(result map[string]any, key string) string {
	if result == nil {
		return ""
	}
	s, _ := result[key].(string)
	return s
}

func intResult(result map[string]any, key string) (int, bool) {
	if result == nil {
		return 0, false
	}
	switch n := result[key].(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	}
	return 0, false
}

// errorOr extracts a human error description from the standard result shapes:
// an "error" field, or a bare {"result": "<denial text>"} envelope (permission
// denials surface this way). Returns "" when nothing is available (e.g. a stored
// row whose result is not a structured map).
func errorOr(result map[string]any) string {
	if e := strResult(result, "error"); e != "" {
		return e
	}
	if r := strResult(result, "result"); r != "" && result["path"] == nil && result["status"] == nil {
		return r
	}
	return ""
}

// failedLine renders "<verb> [target] — failed[; reason]" (no dangling
// "failed:" when no reason is available).
func failedLine(verb, target, reason string) string {
	subject := verb
	if target != "" {
		subject = verb + " " + target
	}
	if reason != "" {
		return fmt.Sprintf("%s — failed: %s", subject, firstLine(reason))
	}
	return fmt.Sprintf("%s — failed", subject)
}

func capSummary(s string) string {
	r := []rune(s)
	if len(r) <= summaryLineLimit {
		return s
	}
	return string(r[:summaryLineLimit-1]) + "…"
}

// SummarizeParams renders a one-line identifying summary of a tool call's
// parameters for the Parameters row of a tool-call block — paths and counts,
// never content. write omits its payload, modify shows only the target files,
// ask shows the question count, register_plan shows the plan length, and
// everything else falls back to k=v with bounded values. Empty params yield "".
func SummarizeParams(name string, params map[string]any) string {
	if len(params) == 0 {
		return ""
	}

	switch name {
	case "system-write":
		return `path="` + strParam(params, "path") + `"`

	case "system-modify":
		files := modifyFiles(params)
		if len(files) == 0 {
			return ""
		}
		parts := make([]string, 0, len(files))
		for _, f := range files {
			parts = append(parts, `{"file":`+strconv.Quote(f)+`}`)
		}
		return "changes=[" + strings.Join(parts, ",") + "]"

	case "system-ask":
		if q, ok := params["questions"].([]any); ok && len(q) > 0 {
			return fmt.Sprintf("questions=%d question(s)", len(q))
		}

	case "system-register_plan":
		if plan := strParam(params, "plan"); plan != "" {
			return fmt.Sprintf("plan=<%d chars>", len(plan))
		}

	case "system-register_compaction":
		// The summary IS the compaction payload: show only its length.
		if s := strParam(params, "summary"); s != "" {
			return fmt.Sprintf("summary=<%d chars>", len(s))
		}
	}

	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(params))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, boundedParamValue(params[k])))
	}
	return strings.Join(parts, ", ")
}

// modifyFiles extracts the unique target files from system-modify's changes in
// first-seen order (the same order summarizeModify reports them in).
func modifyFiles(params map[string]any) []string {
	var files []string
	seen := map[string]bool{}
	if changes, ok := params["changes"].([]any); ok {
		for _, raw := range changes {
			c, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			file, _ := c["file"].(string)
			if file == "" || seen[file] {
				continue
			}
			seen[file] = true
			files = append(files, file)
		}
	}
	return files
}

// boundedParamValue renders one parameter value for display: strings are quoted
// and clipped to 160 runes; anything else is JSON-marshaled and clipped.
func boundedParamValue(v any) string {
	switch val := v.(type) {
	case string:
		return capRunes(strconv.Quote(val), 160)
	default:
		b, err := json.Marshal(val)
		if err != nil {
			return "<invalid>"
		}
		return capRunes(string(b), 160)
	}
}

// capRunes bounds a string to max runes, appending an ellipsis when truncated.
func capRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// --- per-tool summarizers ---
// summarizeRead: only the file name matters, never the content; a
// byte-truncated read is flagged so transcripts show the cut.
func summarizeRead(name string, params map[string]any, result map[string]any, success bool) string {
	path := strParam(params, "path")
	if !success {
		return failedLine("Read", path, errorOr(result))
	}
	if b, _ := result["truncated"].(bool); b {
		return "Read " + path + " — truncated"
	}
	return "Read " + path
}

// summarizeWrite: the target file and what happened (bytes written or error).
func summarizeWrite(name string, params map[string]any, result map[string]any, success bool) string {
	path := strParam(params, "path")
	if !success {
		return failedLine("Wrote", path, errorOr(result))
	}
	if n, ok := intResult(result, "bytes_written"); ok {
		return fmt.Sprintf("Wrote %d bytes to %s", n, path)
	}
	return "Wrote " + path
}

// summarizeDelete: the target file and whether it was removed.
func summarizeDelete(name string, params map[string]any, result map[string]any, success bool) string {
	path := strParam(params, "path")
	if !success {
		return failedLine("Deleted", path, errorOr(result))
	}
	return "Deleted " + path
}

// summarizeModify: the requested files plus the batch outcome. Atomic mode is
// all-or-nothing; non-atomic mode can report partial success.
func summarizeModify(name string, params map[string]any, result map[string]any, success bool) string {
	var order []string
	seen := map[string]bool{}
	if changes, ok := params["changes"].([]any); ok {
		for _, raw := range changes {
			c, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			file, _ := c["file"].(string)
			if file == "" || seen[file] {
				continue
			}
			seen[file] = true
			order = append(order, file)
		}
	}

	if len(order) == 0 {
		return failedLine("Modified files", "", errorOr(result))
	}

	status := strResult(result, "status")
	switch {
	case success && status == "partial":
		applied, _ := intResult(result, "applied")
		total, _ := intResult(result, "total")
		return fmt.Sprintf("Modified %d file(s): %s — %d/%d changes applied (non-atomic)", len(order), strings.Join(order, ", "), applied, total)
	case success:
		return fmt.Sprintf("Modified %d file(s): %s — all ok", len(order), strings.Join(order, ", "))
	default:
		return fmt.Sprintf("Failed %d file(s): %s", len(order), strings.Join(order, ", "))
	}
}

// summarizeFind: what was searched and how much matched — never the matches.
func summarizeFind(name string, params map[string]any, result map[string]any, success bool) string {
	regex := strParam(params, "regex")
	if !success {
		return failedLine("Searched for /"+regex+"/", "", errorOr(result))
	}
	if count, ok := intResult(result, "match_count"); ok {
		return fmt.Sprintf("Searched for /%s/ — %d match(es)", regex, count)
	}
	return fmt.Sprintf("Searched for /%s/", regex)
}

// summarizeList: the directory and how many entries it returned.
func summarizeList(name string, params map[string]any, result map[string]any, success bool) string {
	path := strParam(params, "path")
	if path == "" {
		path = "."
	}
	if !success {
		return failedLine("Listed", path, errorOr(result))
	}
	if count, ok := intResult(result, "entry_count"); ok {
		return fmt.Sprintf("Listed %s — %d entries", path, count)
	}
	return "Listed " + path
}

// summarizeSearchByName: the glob pattern and how many files matched.
func summarizeSearchByName(name string, params map[string]any, result map[string]any, success bool) string {
	pattern := strParam(params, "pattern")
	if !success {
		return failedLine("Found files matching "+pattern, "", errorOr(result))
	}
	if count, ok := intResult(result, "match_count"); ok {
		return fmt.Sprintf("Found files matching %s — %d file(s)", pattern, count)
	}
	return fmt.Sprintf("Found files matching %s", pattern)
}

// summarizeBuild: the command and whether it passed; first error line on fail.
func summarizeBuild(name string, params map[string]any, result map[string]any, success bool) string {
	cmd := strResult(result, "command")
	if cmd == "" {
		cmd = "make build"
	}
	if !success {
		reason := errorOr(result)
		if reason == "" {
			reason = strResult(result, "stderr")
		}
		return failedLine("Ran `"+cmd+"`", "", reason)
	}
	return fmt.Sprintf("Ran `%s` — ok", cmd)
}

// summarizeTests: the command actually run plus pass/fail with a hint of the
// failure.
func summarizeTests(name string, params map[string]any, result map[string]any, success bool) string {
	cmd := strResult(result, "command")
	if cmd == "" {
		paramsStr := strParam(params, "parameters")
		if paramsStr != "" {
			cmd = "go test -v " + paramsStr
		} else {
			cmd = "go test -v ./..."
		}
	}
	if !success {
		reason := errorOr(result)
		if reason == "" {
			reason = strResult(result, "stderr")
		}
		return failedLine("Ran `"+cmd+"`", "", reason)
	}
	return fmt.Sprintf("Ran `%s` — all tests passed", cmd)
}

// summarizeAsk: whether the user answered or cancelled. The outcome is carried
// in result["status"] (the handler returns success for both), with the success
// bool only as a fallback for unknown shapes.
func summarizeAsk(name string, params map[string]any, result map[string]any, success bool) string {
	status := strResult(result, "status")
	switch status {
	case "cancelled":
		return "Asked user — cancelled"
	case "success":
		return "Asked user — answered"
	default:
		if success {
			return "Asked user — answered"
		}
		return failedLine("Asked user", "", errorOr(result))
	}
}

// summarizeRegisterPlan: just that a plan was committed.
func summarizeRegisterPlan(name string, params map[string]any, result map[string]any, success bool) string {
	if !success {
		return failedLine("Registered plan", "", errorOr(result))
	}
	return "Registered execution plan"
}

// summarizeCompact: the compaction request fired (manual or auto-injected).
func summarizeCompact(name string, params map[string]any, result map[string]any, success bool) string {
	if !success {
		return failedLine("Context compaction", "", errorOr(result))
	}
	return "Context compaction requested"
}

// summarizeRegisterCompaction: the compaction completed; note its size.
func summarizeRegisterCompaction(name string, params map[string]any, result map[string]any, success bool) string {
	if !success {
		return failedLine("Registered compaction", "", errorOr(result))
	}
	if s := strParam(params, "summary"); s != "" {
		return fmt.Sprintf("Context compacted (%d chars of summary)", len(s))
	}
	return "Context compacted"
}

// --- fallback for unregistered tools / hook-injected internal calls ---

func fallbackToolSummary(name string, result map[string]any, success bool) string {
	if !success {
		if e := errorOr(result); e != "" {
			return fmt.Sprintf("Ran %s — failed: %s", name, firstLine(e))
		}
		return "Ran " + name + " — failed"
	}
	return "Ran " + name
}

// firstLine trims a possibly multi-line output down to its first non-empty line.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	for i, c := range s {
		if c == '\n' {
			return s[:i]
		}
	}
	return s
}
