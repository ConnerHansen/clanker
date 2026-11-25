package tools

import (
	"clankerv2/data"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withSkillSource temporarily swaps the global skill source, restoring the
// previous value on return. A nil argument simulates no config loaded at all.

// withSkillDir creates a real temp SKILL.md under a fresh temp dir and returns
// the dir. Tests using withSkillSource(t, ...) pass this Dir instead of a fake
// path so the live re-read in handleSkills finds a real file.
func withSkillDir(t *testing.T, body string, subagent bool) string {
	t.Helper()
	dir := t.TempDir()
	raw := "---\nname: alpha\ndescription: does things\n"
	if subagent {
		raw += "subagent: true\n"
	}
	raw += "---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func withSkillSource(t *testing.T, skills []data.Skill) {
	t.Helper()
	prev := skillSource
	if len(skills) == 0 {
		SetSkillSource(nil)
	} else {
		src := skills
		SetSkillSource(func() []data.Skill { return src })
	}
	t.Cleanup(func() { SetSkillSource(prev) })
}

func TestHandleSkillsNoSource(t *testing.T) {
	withSkillSource(t, nil)
	res := handleSkills(&HandlerCtx{args: map[string]any{"name": "x"}}, "system-skills")
	if res.Success {
		t.Fatal("expected failure with no skill source wired")
	}
}

func TestHandleSkillsLoadsBodyAndPath(t *testing.T) {
	dir := withSkillDir(t, "Step 1. Do it.", false)
	withSkillSource(t, []data.Skill{
		{Name: "alpha", Description: "does things", Dir: dir, Body: "Step 1. Do it."},
	})
	res := handleSkills(&HandlerCtx{args: map[string]any{"name": "alpha"}}, "system-skills")
	if !res.Success {
		t.Fatalf("expected success, got %+v", res.Result)
	}
	if body, _ := res.Result["instructions"].(string); body != "Step 1. Do it." {
		t.Errorf("instructions = %q", body)
	}
	if p, _ := res.Result["path"].(string); p != dir {
		t.Errorf("path = %q", p)
	}
	if !strings.Contains(res.Summary, "alpha") {
		t.Errorf("summary should name the skill: %q", res.Summary)
	}
}

func TestHandleSkillsMissingName(t *testing.T) {
	withSkillSource(t, []data.Skill{{Name: "alpha"}})
	res := handleSkills(&HandlerCtx{args: map[string]any{}}, "system-skills")
	if res.Success {
		t.Fatal("expected failure when name is omitted")
	}
}

func TestHandleSkillsUnknownNameListsAvailable(t *testing.T) {
	withSkillSource(t, []data.Skill{{Name: "alpha"}, {Name: "beta"}})
	res := handleSkills(&HandlerCtx{args: map[string]any{"name": "nope"}}, "system-skills")
	if res.Success {
		t.Fatal("expected failure for an unknown skill")
	}
	errMsg, _ := res.Result["error"].(string)
	if !strings.Contains(errMsg, "alpha") || !strings.Contains(errMsg, "beta") {
		t.Errorf("unknown-skill error should list available skills: %q", errMsg)
	}
}

// fakeRT is a minimal ToolRuntime for handler tests: its RunID drives the
// main-agent-only gate of subagent-forced skills.
type fakeRT struct{ runID, model string }

func (f fakeRT) RunID() string          { return f.runID }
func (f fakeRT) OwnerModel() string     { return f.model }
func (f fakeRT) RegisterPlan(string)    {}

// withSubagentBackend temporarily swaps the subagent lifecycle backend, restoring
// the previous one on return.
func withSubagentBackend(t *testing.T, b SubagentBackend) {
	t.Helper()
	prev := subagentBackend
	SetSubagentBackend(b)
	t.Cleanup(func() { SetSubagentBackend(prev) })
}

type stubBackend struct {
	spawnArgs map[string]any
	spawnErr  error
	awaitMap  map[string]any
}

func (s *stubBackend) Spawn(callerID, callerModel string, args map[string]any) (string, error) {
	s.spawnArgs = args
	if s.spawnErr != nil {
		return "", s.spawnErr
	}
	return "sub-3", nil
}
func (s *stubBackend) Await(id string, timeoutSeconds int) map[string]any { return s.awaitMap }
func (s *stubBackend) List(callerID string) []map[string]any             { return nil }
func (s *stubBackend) Kill(id string) bool                               { return false }

// TestHandleSkillsSubagentSkillSpawnsAndAwaits locks the forced-subagent path:
// main agent + subagent:true => spawn stealth child, block-await it, and return
// the terminal report as the tool result.
func TestHandleSkillsSubagentSkillSpawnsAndAwaits(t *testing.T) {
	b := true
	dir := withSkillDir(t, "Step 1. Do it.", true)
	withSkillSource(t, []data.Skill{
		{Name: "alpha", Description: "does things", Dir: dir, Body: "Step 1. Do it.", Subagent: &b},
	})
	backend := &stubBackend{awaitMap: map[string]any{"status": "completed", "id": "sub-3", "output": "did the thing"}}
	withSubagentBackend(t, backend)

	res := handleSkills(&HandlerCtx{
		rt:   fakeRT{runID: "main", model: "m1"},
		args: map[string]any{"name": "alpha", "args": "target repo"},
	}, "system-skills")

	if !res.Success {
		t.Fatalf("expected success, got %+v", res.Result)
	}
	if backend.spawnArgs == nil {
		t.Fatal("spawner was never called")
	}
	if task, _ := backend.spawnArgs["task"].(string); !strings.Contains(task, filepath.Join(dir, "SKILL.md")) {
		t.Errorf("spawn task should reference the skill dir: %q", task)
	}
	if mode, _ := backend.spawnArgs["mode"].(string); mode != "stealth" {
		t.Errorf("skill subagent should be stealth, got %q", mode)
	}
	if s, _ := res.Result["skill"].(string); s != "alpha" {
		t.Errorf("result.skill = %q", s)
	}
	if st, _ := res.Result["status"].(string); st != "completed" {
		t.Errorf("result.status = %q", st)
	}
	if out, _ := res.Result["output"].(string); out != "did the thing" {
		t.Errorf("await report must be merged into the result, output=%q", out)
	}
	if !strings.Contains(res.Summary, "sub-3") || !strings.Contains(res.Summary, "completed") {
		t.Errorf("summary should name the subagent and its status: %q", res.Summary)
	}
}

// TestHandleSkillsSubagentSkillFailureSurfacesFailedStatus maps a non-completed
// terminal state to Success=false.
func TestHandleSkillsSubagentSkillFailureStatus(t *testing.T) {
	b := true
	dir := withSkillDir(t, "body", true)
	withSkillSource(t, []data.Skill{{Name: "alpha", Dir: dir, Subagent: &b}})
	backend := &stubBackend{awaitMap: map[string]any{"status": "failed", "id": "sub-3", "error": "boom"}}
	withSubagentBackend(t, backend)

	res := handleSkills(&HandlerCtx{
		rt:   fakeRT{runID: "main"},
		args: map[string]any{"name": "alpha"},
	}, "system-skills")
	if res.Success {
		t.Fatal("expected failure for a failed subagent run")
	}
	if st, _ := res.Result["status"].(string); st != "failed" {
		t.Errorf("result.status = %q", st)
	}
}

// TestHandleSkillsSubagentSkillMainOnly rejects a forced-subagent skill invoked
// by a non-main run: those have no await tools and could never collect the
// grandchild. Spawn must not be called.
func TestHandleSkillsSubagentSkillMainOnly(t *testing.T) {
	b := true
	dir := withSkillDir(t, "body", true)
	withSkillSource(t, []data.Skill{{Name: "alpha", Dir: dir, Subagent: &b}})
	backend := &stubBackend{awaitMap: map[string]any{"status": "completed"}}
	withSubagentBackend(t, backend)

	res := handleSkills(&HandlerCtx{
		rt:   fakeRT{runID: "sub-9"},
		args: map[string]any{"name": "alpha"},
	}, "system-skills")
	if res.Success {
		t.Fatal("expected rejection from a subagent run")
	}
	if backend.spawnArgs != nil {
		t.Error("spawn must not be called for a rejected subagent-run invocation")
	}
	if errMsg, _ := res.Result["error"].(string); !strings.Contains(errMsg, "main agent") {
		t.Errorf("rejection error should explain the main-agent restriction: %q", errMsg)
	}
}

func TestHandleSkillsSubagentSkillSpawnError(t *testing.T) {
	b := true
	dir := withSkillDir(t, "body", true)
	withSkillSource(t, []data.Skill{{Name: "alpha", Dir: dir, Subagent: &b}})
	backend := &stubBackend{spawnErr: errors.New("at cap")}
	withSubagentBackend(t, backend)

	res := handleSkills(&HandlerCtx{
		rt:   fakeRT{runID: "main"},
		args: map[string]any{"name": "alpha"},
	}, "system-skills")
	if res.Success {
		t.Fatal("expected failure when spawn errors")
	}
	if errMsg, _ := res.Result["error"].(string); !strings.Contains(errMsg, "at cap") {
		t.Errorf("spawn error should surface: %q", errMsg)
	}
}

func TestHandleSkillsSubagentSkillNoBackend(t *testing.T) {
	b := true
	dir := withSkillDir(t, "body", true)
	withSkillSource(t, []data.Skill{{Name: "alpha", Dir: dir, Subagent: &b}})
	withSubagentBackend(t, nil)

	res := handleSkills(&HandlerCtx{
		rt:   fakeRT{runID: "main"},
		args: map[string]any{"name": "alpha"},
	}, "system-skills")
	if res.Success {
		t.Fatal("expected failure when the subagent backend is unavailable")
	}
}

func TestHandleSkillsReadsBodyFromDiskAtInvocation(t *testing.T) {
	dir := withSkillDir(t, "original body", false)
	withSkillSource(t, []data.Skill{
		{Name: "alpha", Description: "does things", Dir: dir, Body: "stale body"},
	})

	// First call reads the on-disk content.
	res := handleSkills(&HandlerCtx{args: map[string]any{"name": "alpha"}}, "system-skills")
	if !res.Success {
		t.Fatalf("expected success, got %+v", res.Result)
	}
	if body, _ := res.Result["instructions"].(string); body != "original body" {
		t.Errorf("instructions = %q, want on-disk body", body)
	}

	// Overwrite the file; the cached skill body is now stale.
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: alpha\ndescription: does things\n---\nupdated body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res = handleSkills(&HandlerCtx{args: map[string]any{"name": "alpha"}}, "system-skills")
	if body, _ := res.Result["instructions"].(string); body != "updated body" {
		t.Errorf("instructions after disk edit = %q, want fresh body", body)
	}
}

func TestHandleSkillsReportsUnreadableSkillFile(t *testing.T) {
	dir := withSkillDir(t, "body", false)
	withSkillSource(t, []data.Skill{
		{Name: "alpha", Description: "does things", Dir: dir, Body: "body"},
	})
	if err := os.Remove(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	res := handleSkills(&HandlerCtx{args: map[string]any{"name": "alpha"}}, "system-skills")
	if res.Success {
		t.Fatal("expected failure when SKILL.md is unreadable")
	}
	errMsg, _ := res.Result["error"].(string)
	if !strings.Contains(errMsg, "SKILL.md") {
		t.Errorf("error should name the missing file: %q", errMsg)
	}
}
