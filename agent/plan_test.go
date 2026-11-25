package agent

import (
	"clankerv2/agent/structs"
	"clankerv2/data"
	"encoding/json"
	"strings"
	"testing"
)

// setTestMainRuntimeForPlans installs a main runtime carrying a stealth-style
// session and restores the previous runtime on cleanup so plan state never
// leaks between tests.
func setTestMainRuntimeForPlans(t *testing.T) {
	t.Helper()
	prev := GetMainRuntimeInternal()
	rt := NewAgentRuntime()
	rt.Session = &data.Session{ID: -1}
	SetMainRuntime(rt)
	t.Cleanup(func() { SetMainRuntime(prev) })
}

func writeCall(path string) structs.IncomingToolCall {
	b, _ := json.Marshal(map[string]string{"path": path})
	return structs.IncomingToolCall{Name: "system-write", Arguments: string(b)}
}

func applyChangeCall(files ...string) structs.IncomingToolCall {
	var changes []map[string]any
	for _, f := range files {
		changes = append(changes, map[string]any{"file": f, "type": "replace", "old": "o", "new": "n"})
	}
	b, _ := json.Marshal(map[string]any{"changes": changes})
	return structs.IncomingToolCall{Name: "system-modify", Arguments: string(b)}
}

func checkCall() structs.IncomingToolCall {
	return structs.IncomingToolCall{Name: "system-check_plan", Arguments: `{"question":"..."}`}
}

func TestRegisterPlanFlush(t *testing.T) {
	setTestMainRuntimeForPlans(t)

	RegisterPlan("step 1; step 2")
	if got := MainRuntime().Session.CurrentPlan; got != "step 1; step 2" {
		t.Fatalf("after RegisterPlan: got %q, want %q", got, "step 1; step 2")
	}

	FlushPlan()
	if got := MainRuntime().Session.CurrentPlan; got != "" {
		t.Fatalf("after FlushPlan: got %q, want empty", got)
	}

	// Flushing with no plan is a safe no-op.
	FlushPlan()
}

func TestRegisterPlanCreatesSession(t *testing.T) {
	prev := GetMainRuntimeInternal()
	rt := NewAgentRuntime() // no session yet
	SetMainRuntime(rt)
	t.Cleanup(func() { SetMainRuntime(prev) })

	RegisterPlan("plan")
	if rt.Session == nil {
		t.Fatal("RegisterPlan should create a session target when none exists")
	}
	if rt.Session.CurrentPlan != "plan" {
		t.Fatalf("got %q, want %q", rt.Session.CurrentPlan, "plan")
	}
}

func TestWritePathsAt(t *testing.T) {
	calls := []structs.IncomingToolCall{
		writeCall("a.go"),
		applyChangeCall("b.go", "c.go"),
		{Name: "system-read", Arguments: `{"path":"ignored.go"}`},
		{Name: "system-write", Arguments: `{not-json`}, // malformed args degrade to no paths
	}

	want := [][]string{
		{"a.go"},
		{"b.go", "c.go"},
		nil,
		nil,
	}
	for i, c := range calls {
		got := writePathsAt(i, calls)
		if len(got) != len(want[i]) {
			t.Fatalf("call %d (%s): got %v, want %v", i, c.Name, got, want[i])
		}
		for j := range want[i] {
			if got[j] != want[i][j] {
				t.Fatalf("call %d (%s): got %v, want %v", i, c.Name, got, want[i])
			}
		}
	}
}

func TestPlanCheck(t *testing.T) {
	setTestMainRuntimeForPlans(t)
	rt := MainRuntime()
	rt.Session.CurrentPlan = "update a.go and b.go"

	calls := []structs.IncomingToolCall{writeCall("a.go"), applyChangeCall("b.go")}

	// No plan active -> never inject.
	rt.Session.CurrentPlan = ""
	if got, _ := PlanCheck(calls, rt); len(got) != 0 {
		t.Fatalf("no plan: expected no injection, got %d calls", len(got))
	}
	rt.Session.CurrentPlan = "update a.go and b.go"

	// Writes present, no check yet -> inject.
	got, err := PlanCheck(calls, rt)
	if err != nil {
		t.Fatalf("PlanCheck error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "system-check_plan" {
		t.Fatalf("expected a single system-check_plan injection, got %+v", got)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(got[0].Arguments), &args); err != nil {
		t.Fatalf("injected arguments are not valid JSON: %v", err)
	}
	for _, f := range []string{"a.go", "b.go"} {
		if !strings.Contains(args["question"], f) {
			t.Fatalf("question should list modified file %q, got: %s", f, args["question"])
		}
	}

	// Simulate the loop having executed the injected check.
	calls = append(calls, checkCall())

	// No new writes since the last check -> decline so lower-priority hooks run.
	if got, _ := PlanCheck(calls, rt); len(got) != 0 {
		t.Fatalf("checked with no new writes: expected no injection, got %d calls", len(got))
	}

	// New write after the last check -> re-inject.
	calls = append(calls, writeCall("c.go"))
	got, _ = PlanCheck(calls, rt)
	if len(got) != 1 || got[0].Name != "system-check_plan" {
		t.Fatalf("new write after check: expected re-injection, got %+v", got)
	}

	// And declining again once that check has run.
	calls = append(calls, checkCall())
	if got, _ := PlanCheck(calls, rt); len(got) != 0 {
		t.Fatalf("expected decline after second check, got %d calls", len(got))
	}
}

func TestPlanCheckNoWrites(t *testing.T) {
	setTestMainRuntimeForPlans(t)
	rt := MainRuntime()
	rt.Session.CurrentPlan = "read the docs and summarize"

	calls := []structs.IncomingToolCall{
		{Name: "system-read", Arguments: `{"path":"README.md"}`},
	}

	// Read-only turn: nothing to verify, even though the plan is active.
	if got, _ := PlanCheck(calls, rt); len(got) != 0 {
		t.Fatalf("read-only turn: expected no injection, got %d calls", len(got))
	}
}
