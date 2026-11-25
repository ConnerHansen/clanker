package tui

import (
	"clankerv2/data"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// toolCallBlock renders a completed tool call (live or loaded) as:
//
//	Tool: <name>
//	 ├ Status: ✓ | 🞪
//	 ├ Parameters: <tool-defined one-line summary, or k=v preview>
//	 └ Results: <all keys, values truncated>
//
// The block carries a one-column left margin and is tinted dim cyan on
// success or dim yellow on failure.
func (m *Model) toolCallBlock(success bool, name string, args map[string]any, paramLine, preview string) string {
	titleStyle := styleToolSuccessTitle
	bodyStyle := styleToolSuccess
	status := checkPrefix
	if !success {
		titleStyle = styleToolFailureTitle
		bodyStyle = styleToolFailure
		status = xPrefix
	}

	lines := []string{
		titleStyle.Render(name, status),
	}
	params := paramLine
	if params == "" {
		params = formatJSONPreview(args)
	}
	if params != "" {
		lines = append(lines, bodyStyle.Render(" ├ Parameters: "+params))
	}

	if preview != "" {
		lines = append(lines, bodyStyle.Render(" └ Results: "+truncateDisplay(preview, 600)))
	}

	return strings.Join(lines, "\n")
}

// storedToolRow matches the content persisted by AppendToolCall for a finished
// tool call. Result is kept as an object (not stringified) so it can be
// previewed without nested escaping.
type storedToolRow struct {
	Name    string         `json:"name"`
	Success *bool          `json:"success,omitempty"`
	Params  map[string]any `json:"params,omitempty"`
	Result  string         `json:"result,omitempty"`
}

// legacyToolRow matches the pre-summary wire format that older sessions may
// still have stored: {"id","type","name","result" (stringified JSON)}.
type legacyToolRow struct {
	Name   string `json:"name"`
	Result string `json:"result"`
}

// parsedToolRow is the normalized view of a session's stored tool row,
// regardless of which era it was written in.
type parsedToolRow struct {
	Name    string
	Params  map[string]any
	Result  string
	Success bool
}

// parseStoredToolRow recovers the tool name, params, result and success flag
// from a session row's stored content. It understands the current storage
// format (storedToolRow) and the legacy wire format (legacyToolRow).
func parseStoredToolRow(content string) (parsedToolRow, bool) {
	if strings.TrimSpace(content) == "" {
		return parsedToolRow{}, false
	}

	var stored storedToolRow
	if err := json.Unmarshal([]byte(content), &stored); err == nil && stored.Name != "" {
		row := parsedToolRow{
			Name:   data.CanonicalToolName(stored.Name),
			Params: stored.Params,
			Result: stored.Result,
		}
		if stored.Success != nil {
			row.Success = *stored.Success
		}

		return row, true
	}

	var legacy legacyToolRow
	if err := json.Unmarshal([]byte(content), &legacy); err == nil && legacy.Name != "" {
		var inner map[string]any
		if err := json.Unmarshal([]byte(legacy.Result), &inner); err == nil {
			if status, hasStatus := inner["status"].(string); hasStatus {
				legacySuccess := status == "success"
				return parsedToolRow{Name: data.CanonicalToolName(legacy.Name), Result: legacy.Result, Success: legacySuccess}, true
			}
		}
		// Result wasn't a JSON object; show it raw and assume failure.
		return parsedToolRow{Name: data.CanonicalToolName(legacy.Name), Result: legacy.Result}, false
	}

	return parsedToolRow{}, false
}

// maxPreviewStringLen bounds any single string value inside a JSON preview
// (in runes). All keys are preserved; long values (file contents, etc.) are
// clipped so a large payload can't blow up the display.
const maxPreviewStringLen = 160

// formatJSONPreview renders a compact single-line preview of raw JSON for
// display. All keys are shown; long string values are truncated to
// maxPreviewStringLen runes. Non-object JSON falls back to a plain truncated
// string. Returns "" for empty input.
func formatJSONPreview(raw map[string]any) string {
	if len(raw) == 0 {
		return ""
	}

	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(raw))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, formatPreviewValue(raw[k])))
	}

	return strings.Join(parts, ", ")
}

// formatPreviewValue renders one JSON value for a preview: strings are quoted
// and truncated to maxPreviewStringLen runes, numbers and booleans render
// plain, and nested structures are re-marshaled (with their strings clipped).
func formatPreviewValue(v any) string {
	switch val := v.(type) {
	case string:
		return truncateDisplay(strconv.Quote(strings.ReplaceAll(val, "\n", "\\n")), maxPreviewStringLen)
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(val)
	default:
		trimmed := clipStringsDeep(v)
		b, err := json.Marshal(trimmed)
		if err != nil {
			return "<invalid>"
		}
		return truncateDisplay(string(b), 160)
	}
}

// clipStringsDeep recursively clips long string values inside nested JSON
// structures so nested payloads stay bounded too.
func clipStringsDeep(v any) any {
	switch val := v.(type) {
	case string:
		return capRunes(val, maxPreviewStringLen)
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, mv := range val {
			out[k] = clipStringsDeep(mv)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, mv := range val {
			out[i] = clipStringsDeep(mv)
		}
		return out
	default:
		return v
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

// truncateDisplay folds whitespace and caps the string (in runes) with an
// ellipsis so display lines stay bounded.
func truncateDisplay(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
