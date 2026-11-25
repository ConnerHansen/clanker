package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustParams(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return string(b)
}

func TestSummarizeReadSuccessReportsPathOnly(t *testing.T) {
	got := SummarizeTool("system-read", `{"path":"./data/engine.go","line":10}`, map[string]any{
		"status": "success", "path": "./data/engine.go", "total_lines": 200, "content": strings.Repeat("x", 5000),
	}, true)

	if got != "Read ./data/engine.go" {
		t.Errorf("got %q, want %q", got, "Read ./data/engine.go")
	}
	if strings.Contains(got, "x") || len(got) > 60 {
		t.Errorf("read summary leaked content: %q", got)
	}
}

func TestSummarizeReadFailure(t *testing.T) {
	got := SummarizeTool("system-read", `{"path":"./missing.go"}`, map[string]any{
		"status": "error", "path": "./missing.go", "error": "failed to read file: no such file",
	}, false)

	if !strings.HasPrefix(got, "Read ./missing.go") || !strings.Contains(got, "failed") {
		t.Errorf("got %q", got)
	}
}

func TestSummarizeWrite(t *testing.T) {
	got := SummarizeTool("system-write", `{"path":"./a.txt"}`, map[string]any{
		"status": "success", "path": "./a.txt", "bytes_written": 1234,
	}, true)
	if got != "Wrote 1234 bytes to ./a.txt" {
		t.Errorf("got %q", got)
	}

	got = SummarizeTool("system-write", `{"path":"./a.txt"}`, map[string]any{
		"status": "error", "error": "failed to write file: permission denied",
	}, false)
	if !strings.Contains(got, "Wrote ./a.txt") || !strings.Contains(got, "permission denied") {
		t.Errorf("failure summary got %q", got)
	}
}

func TestSummarizeModifyAllOk(t *testing.T) {
	params := mustParams(t, map[string]any{
		"changes": []any{
			map[string]any{"file": "./a.go", "type": "replace", "old": "x", "new": "y"},
			map[string]any{"file": "./b.go", "type": "replace", "old": "p", "new": "q"},
			map[string]any{"file": "./a.go", "type": "replace", "old": "m", "new": "n"}, // same file again
		},
	})
	result := map[string]any{
		"status": "success",
		"results": []any{
			map[string]any{"file": "./a.go", "successful": true},
			map[string]any{"file": "./b.go", "successful": true},
		},
	}

	got := SummarizeTool("system-modify", params, result, true)
	want := "Modified 2 file(s): ./a.go, ./b.go \u2014 all ok"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSummarizeModifyWithFailure(t *testing.T) {
	params := mustParams(t, map[string]any{
		"changes": []any{
			map[string]any{"file": "./a.go", "type": "replace", "old": "x", "new": "y"},
			map[string]any{"file": "./b.go", "type": "replace", "old": "p", "new": "q"},
		},
	})
	result := map[string]any{
		"status": "error",
		"results": []any{
			map[string]any{"file": "./a.go", "successful": true},
			map[string]any{"file": "./b.go", "successful": false, "error": "'old' block not found"},
		},
	}

	got := SummarizeTool("system-modify", params, result, false)
	want := "Failed 2 file(s): ./a.go, ./b.go"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSummarizeFindNoContentsLeaked(t *testing.T) {
	result := map[string]any{
		"status": "success", "match_count": 42,
		"matches": []any{map[string]any{"path": "./x.go", "line": 1, "match": "secret-token-abc"}},
	}
	got := SummarizeTool("system-find", `{"regex":"CREATE TABLE","path":"./data"}`, result, true)

	if got != "Searched for /CREATE TABLE/ — 42 match(es)" {
		t.Errorf("got %q", got)
	}
	if strings.Contains(got, "secret-token-abc") || strings.Contains(got, "./x.go") {
		t.Errorf("find summary leaked match contents: %q", got)
	}
}

func TestSummarizeListAndSearchByName(t *testing.T) {
	got := SummarizeTool("system-list", `{"path":"./agent","recursive":true}`, map[string]any{
		"status": "success", "entry_count": 17,
		"entries": []any{map[string]any{"path": "./agent/secret.go"}},
	}, true)
	if got != "Listed ./agent — 17 entries" {
		t.Errorf("list got %q", got)
	}

	got = SummarizeTool("system-search_by_name", `{"pattern":"*test*.go"}`, map[string]any{
		"status": "success", "match_count": 5,
	}, true)
	if got != "Found files matching *test*.go — 5 file(s)" {
		t.Errorf("search got %q", got)
	}
}

func TestSummarizeBuildAndTests(t *testing.T) {
	got := SummarizeTool("system-build", "{}", map[string]any{
		"status": "error", "command": "go build -o build/bin/clankerv2 .", "exit_code": 1,
		"stderr": "./agent/summary_engine.go:10:2: undefined: X\n./more.go: more errors",
	}, false)
	if !strings.Contains(got, "failed") || !strings.Contains(got, "undefined: X") || strings.Contains(got, "more.go") {
		t.Errorf("build failure summary got %q", got)
	}

	got = SummarizeTool("system-tests", `{"parameters":"./data"}`, map[string]any{
		"status": "success", "command": "go test -v ./data",
	}, true)
	if got != "Ran `go test -v ./data` — all tests passed" {
		t.Errorf("tests got %q", got)
	}
}

func TestSummarizeAskAndPlan(t *testing.T) {
	got := SummarizeTool("system-ask", `{}`, map[string]any{"status": "cancelled"}, true)
	if got != "Asked user — cancelled" {
		t.Errorf("ask cancelled got %q", got)
	}

	got = SummarizeTool("system-ask", `{}`, map[string]any{"status": "success"}, true)
	if got != "Asked user — answered" {
		t.Errorf("ask answered got %q", got)
	}

	got = SummarizeTool("system-register_plan", `{"plan":"do things"}`, map[string]any{
		"status": "success", "message": "Plan registered.",
	}, true)
	if got != "Registered execution plan" {
		t.Errorf("plan got %q", got)
	}
}

func TestSummarizeFallbackAndCap(t *testing.T) {
	got := SummarizeTool("some.future.tool", `{"x":"y"}`, map[string]any{"status": "success"}, true)
	if got != "Ran some.future.tool" {
		t.Errorf("fallback got %q", got)
	}

	got = SummarizeTool("system.unknown", `{}`, map[string]any{
		"error": strings.Repeat("e", 500),
	}, false)
	if len([]rune(got)) > summaryLineLimit {
		t.Errorf("summary not capped: %d runes", len([]rune(got)))
	}
}

func TestSummarizeMalformedParamsDegrades(t *testing.T) {
	got := SummarizeTool("system-read", `not-json`, map[string]any{
		"status": "success", "path": "./a.go",
	}, true)
	if got == "" || !strings.Contains(got, "Read") {
		t.Errorf("malformed params should still produce a summary: %q", got)
	}
}

func TestSummarizeParamsIdentifyingOnly(t *testing.T) {
	// write: the path only — a large content payload must never leak.
	got := SummarizeParams("system-write", map[string]any{
		"path": "./a.txt", "content": strings.Repeat("x", 5000),
	})
	if got != "path=\"./a.txt\"" {
		t.Errorf("write params got %q", got)
	}

	// modify: the target files only — never block content.
	got = SummarizeParams("system-modify", map[string]any{
		"changes": []any{
			map[string]any{"file": "./a.go", "before": nil, "after": map[string]any{"line": 2, "text": "func main() {}"}, "lines": strings.Repeat("y", 5000)},
			map[string]any{"file": "./b.go", "before": nil, "after": nil, "lines": "z"},
		},
	})
	if got != `changes=[{"file":"./a.go"},{"file":"./b.go"}]` {
		t.Errorf("modify params got %q", got)
	}

	// Everything else: bounded k=v preview, keys in order.
	got = SummarizeParams("system-read", map[string]any{"path": "./x.go", "line": 3})
	if got != "line=3, path=\"./x.go\"" {
		t.Errorf("read params got %q", got)
	}

	if SummarizeParams("system-build", map[string]any{}) != "" {
		t.Errorf("empty params should yield an empty string")
	}
}
