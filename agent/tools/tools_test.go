package tools

import (
	"clankerv2/data"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func StripAnsi(str string) string {
	ansiRegexp := `\x1b\[[0-9;]*[a-zA-Z]`
	re := regexp.MustCompile(ansiRegexp)
	return re.ReplaceAllString(str, "")
}

func TestGenerateDiff(t *testing.T) {
	tmpDir := t.TempDir()

	file1 := filepath.Join(tmpDir, "testfile.txt")
	content1 := "Line 1\nLine 2\nLine 3"
	os.WriteFile(file1, []byte(content1), 0644)

	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	tests := []struct {
		name        string
		path        string
		newContent  string
		wantContain []string
		wantErr     bool
	}{
		{
			name:       "Existing File Change",
			path:       "testfile.txt",
			newContent: "Line 1\nLine 2 modified\nLine 3",
			wantContain: []string{"-Line 2", "+Line 2 modified"},
			wantErr:     false,
		},
		{
			name:       "New File",
			path:       "newfile.txt",
			newContent: "Fresh content",
			wantContain: []string{"+Fresh content"},
			wantErr:     false,
		},
		{
			name:       "No Change",
			path:       "testfile.txt",
			newContent: content1,
			wantContain: []string{}, // Should be empty
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GenerateDiff(tt.path, tt.newContent)
			if (err != nil) != tt.wantErr {
				t.Errorf("GenerateDiff() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				gotStripped := StripAnsi(got)
				for _, want := range tt.wantContain {
					if !strings.Contains(gotStripped, want) {
						t.Errorf("GenerateDiff() got %q (stripped), does not contain %q", gotStripped, want)
					}
				}
				if len(tt.wantContain) == 0 && gotStripped != "" {
					t.Errorf("GenerateDiff() got %q (stripped), want empty string", gotStripped)
				}
			}
		})
	}
}

// fakeToolRuntime records plan registrations against a named run, standing in
// for an agent.AgentRuntime (which the tools package cannot reference without
// an import cycle).
type fakeToolRuntime struct {
	id         string
	registered []string
}

func (f *fakeToolRuntime) RunID() string { return f.id }

func (f *fakeToolRuntime) OwnerModel() string { return "test-model" }

func (f *fakeToolRuntime) RegisterPlan(plan string) { f.registered = append(f.registered, plan) }

func TestHandleRegisterPlan(t *testing.T) {
	rt := &fakeToolRuntime{id: "sub-1"}

	res := HandleToolCall(rt, nil, "system-register_plan", map[string]any{"plan": "update a.go, add test"}, nil, nil)
	if !res.Success {
		t.Fatalf("system-register_plan should succeed, got %+v", res.Result)
	}
	if len(rt.registered) != 1 || rt.registered[0] != "update a.go, add test" {
		t.Fatalf("expected plan to be registered via runtime, got %v", rt.registered)
	}

	// Empty plan is rejected.
	res = HandleToolCall(rt, nil, "system-register_plan", map[string]any{"plan": "   "}, nil, nil)
	if res.Success {
		t.Fatalf("empty plan should fail, got %+v", res.Result)
	}

	// Missing plan arg is rejected.
	res = HandleToolCall(rt, nil, "system-register_plan", map[string]any{}, nil, nil)
	if res.Success {
		t.Fatalf("missing plan should fail, got %+v", res.Result)
	}
}

func TestHandleRegisterPlanNoRuntime(t *testing.T) {
	rt := &fakeToolRuntime{id: "sub-2"}

	res := HandleToolCall(nil, nil, "system-register_plan", map[string]any{"plan": "plan"}, nil, nil)
	if res.Success {
		t.Fatalf("system-register_plan without an owning runtime should fail, got %+v", res.Result)
	}

	// system-ask with an answering requester succeeds and surfaces the answers.
	res = HandleToolCall(rt, nil, "system-ask", map[string]any{"questions": []any{
		map[string]any{
			"id":      "q1",
			"type":    "select",
			"prompt":  "pick an option",
			"options": []any{map[string]any{"label": "alpha", "value": "a"}},
		},
	}}, nil, func(chan any, []any) string {
		return `{"status":"success","answers":{"q1":"a"}}`
	})
	if !res.Success {
		t.Fatalf("system-ask with an answering requester should succeed, got %+v", res.Result)
	}

	// Without a UI requester the call fails fast instead of blocking.
	res = HandleToolCall(rt, nil, "system-ask", map[string]any{"questions": []any{map[string]any{
		"id":      "q1",
		"type":    "select",
		"prompt":  "pick an option",
		"options": []any{map[string]any{"label": "alpha", "value": "a"}},
	}}}, nil, nil)
	if res.Success {
		t.Fatalf("system-ask without a requester should fail, got %+v", res.Result)
	}
}

func TestHandleCheckPlan(t *testing.T) {
	// system-check_plan is internal-only: it echoes the hook-provided question.
	want := "did you finish a.go?"
	res := HandleToolCall(nil, nil, "system-check_plan", map[string]any{"question": want}, nil, nil)
	if !res.Success {
		t.Fatalf("system-check_plan should succeed, got %+v", res.Result)
	}
	if res.RawResult == nil || *res.RawResult != want {
		t.Fatalf("check_plan raw result: got %v, want %q", res.RawResult, want)
	}

	// Empty question degrades gracefully.
	res = HandleToolCall(nil, nil, "system-check_plan", map[string]any{}, nil, nil)
	if !res.Success {
		t.Fatalf("system-check_plan with no question should still succeed, got %+v", res.Result)
	}
}

// --- system-delete ---

type permCapture struct {
	tool, path, action, prompt string
	viewport                   *data.PermissionViewport
}

func (c *permCapture) fn(allowed bool) PermissionRequester {
	return func(channel chan any, tool, path, action, prompt string, viewport *data.PermissionViewport) string {
		*c = permCapture{tool: tool, path: path, action: action, prompt: prompt, viewport: viewport}
		if allowed {
			return "Operation approved by the user"
		}
		return "Permission denied by the user"
	}
}

// TestDeleteFile covers the raw primitive: success removes the file (result
// path is relative, like WriteFile), missing paths and directories error.
func TestDeleteFile(t *testing.T) {
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	target := filepath.Join(tmpDir, "doomed.txt")
	if err := os.WriteFile(target, []byte("bye"), 0644); err != nil {
		t.Fatal(err)
	}

	res := DeleteFile("doomed.txt")
	if res.Status != "success" {
		t.Fatalf("delete should succeed: %+v", res)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("file still exists after delete: %v", err)
	}

	// Missing path errors with the not-exist hint.
	if r := DeleteFile("ghost.txt"); r.Status != "error" || !strings.Contains(r.Error, "does not exist") {
		t.Errorf("missing file: got %+v", r)
	}

	// Directories are rejected (files only).
	subdir := filepath.Join(tmpDir, "a-dir")
	os.MkdirAll(subdir, 0755)
	if r := DeleteFile("a-dir"); r.Status != "error" || !strings.Contains(r.Error, "directory") {
		t.Errorf("directory delete must be rejected: %+v", r)
	}

	// Empty path errors.
	if r := DeleteFile(""); r.Status != "error" {
		t.Errorf("empty path must error: %+v", r)
	}
}

// TestHandleSystemDelete drives the REAL handler with a fake permission
// requester: asserts the descriptor/action surfaced to the UI, that the diff
// viewport previews the removal, that denial leaves the file untouched, and
// that missing/directory targets error WITHOUT ever prompting.
func TestHandleSystemDelete(t *testing.T) {
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	target := filepath.Join(tmpDir, "doomed.txt")
	if err := os.WriteFile(target, []byte("line one\nline two"), 0644); err != nil {
		t.Fatal(err)
	}

	// Denied: nothing is deleted.
	cap := &permCapture{}
	res := HandleToolCall(nil, nil, "system-delete", map[string]any{"path": "doomed.txt"}, cap.fn(false), nil)
	if res.Success {
		t.Fatalf("denied delete must fail: %+v", res.Result)
	}
	if cap.action != "delete" || !strings.HasPrefix(cap.prompt, "Delete file:") || cap.tool != "system-delete" {
		t.Errorf("permission request descriptor wrong: %+v", cap)
	}
	if cap.viewport == nil || !strings.Contains(StripAnsi(cap.viewport.Content), "-line one") {
		t.Errorf("viewport should preview the removal diff, got %v", cap.viewport)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("denied delete removed the file: %v", err)
	}

	// Approved: file is gone.
	cap = &permCapture{}
	res = HandleToolCall(nil, nil, "system-delete", map[string]any{"path": "doomed.txt"}, cap.fn(true), nil)
	if !res.Success {
		t.Fatalf("approved delete should succeed: %+v", res.Result)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("file should be gone after approved delete: %v", err)
	}

	// Missing target: error BEFORE any permission prompt.
	cap = &permCapture{}
	res = HandleToolCall(nil, nil, "system-delete", map[string]any{"path": "ghost.txt"}, cap.fn(true), nil)
	if res.Success {
		t.Fatalf("missing-file delete must fail: %+v", res.Result)
	}
	if cap.prompt != "" {
		t.Errorf("missing-file delete must not prompt the user, got %+v", cap)
	}

	// Directory target: error BEFORE any permission prompt.
	subdir := filepath.Join(tmpDir, "a-dir")
	os.MkdirAll(subdir, 0755)
	cap = &permCapture{}
	res = HandleToolCall(nil, nil, "system-delete", map[string]any{"path": "a-dir"}, cap.fn(true), nil)
	if res.Success {
		t.Fatalf("directory delete must fail: %+v", res.Result)
	}
	if cap.prompt != "" {
		t.Errorf("directory delete must not prompt the user, got %+v", cap)
	}

	// Missing path arg.
	res = HandleToolCall(nil, nil, "system-delete", map[string]any{}, nil, nil)
	if res.Success {
		t.Fatalf("missing path should fail: %+v", res.Result)
	}
}
