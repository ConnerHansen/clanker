package agent

import (
	"strings"
	"testing"

	"clankerv2/data"
)

// permRoundTrip drives one RequestPermission call through a fake UI channel:
// it captures the data.PermissionRequest that would reach the TUI/web
// frontend, approves it, and returns the envelope plus the tool's result
// string.
func permRoundTrip(t *testing.T, tool, path, action, prompt string, viewport *data.PermissionViewport) (data.PermissionRequest, string) {
	t.Helper()

	// Buffer 2: RequestPermission emits StateMsg(waiting) + payload before blocking.
	ch := make(chan any, 2)
	done := make(chan string, 1)
	go func() { done <- RequestPermission(ch, tool, path, action, prompt, viewport) }()

	var ev any
	for {
		ev = <-ch
		if _, isState := ev.(data.StateMsg); !isState {
			break
		}
	}
	req, ok := ev.(data.PermissionRequest)
	if !ok {
		t.Fatalf("expected data.PermissionRequest on channel, got %T", ev)
	}

	ResolvePermission(req.ID, true)
	return req, <-done
}

// TestRequestPermissionLevelByAction locks the coloration contract: level is
// derived from the action (single source of truth), the caller's descriptor
// passes through untouched so the panel header is not hard-coded, and a denied
// result reads "denied" (the string the tool layer checks for).
func TestRequestPermissionLevelByAction(t *testing.T) {
	cases := []struct {
		action  string
		prompt  string
		wantLvl string
	}{
		{"create", "Create new file: a.txt", "info"},
		{"overwrite", "Overwrite existing file: a.txt", "warning"},
		{"update", "Modify 2 file(s)", "warning"},
		{"delete", "Delete file: a.txt", "danger"},
	}

	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			req, out := permRoundTrip(t, "system.x", "a.txt", tc.action, tc.prompt, nil)
			if req.Level != tc.wantLvl {
				t.Errorf("level = %q, want %q", req.Level, tc.wantLvl)
			}
			if req.Prompt != tc.prompt {
				t.Errorf("prompt pass-through: got %q, want %q", req.Prompt, tc.prompt)
			}
			if req.Action != tc.action || req.Tool != "system.x" || req.Path != "a.txt" {
				t.Errorf("request fields wrong: %+v", req)
			}
			if out == "" || strings.Contains(out, "denied") {
				t.Errorf("approved call should not read as denied: %q", out)
			}
		})
	}

	// Denied resolves with a result the tool layer treats as rejection.
	deniedCh := make(chan any, 2)
	deniedDone := make(chan string, 1)
	go func() { deniedDone <- RequestPermission(deniedCh, "system-delete", "b.txt", "delete", "Delete file: b.txt", nil) }()
	var deniedReq data.PermissionRequest
	for {
		ev := <-deniedCh
		if _, isState := ev.(data.StateMsg); !isState {
			deniedReq = ev.(data.PermissionRequest)
			break
		}
	}
	ResolvePermission(deniedReq.ID, false)
	out := <-deniedDone
	if !strings.Contains(out, "denied") {
		t.Errorf("denied call must read as denied: %q", out)
	}
}
