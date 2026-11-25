package agent

import (
	"clankerv2/agent/structs"
	"clankerv2/data"
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// activePlan returns the given runtime's session plan, or "" if none is
// active. Works identically for stealth and persisted sessions since the plan
// lives on the in-memory Session object. A nil runtime falls back to the main
// (interactive) runtime, so hook injection paths keep working.
func activePlan(rt *AgentRuntime) string {
	if rt == nil {
		rt = MainRuntime()
	}
	if rt.Session == nil {
		return ""
	}
	return rt.Session.CurrentPlan
}

// RegisterPlan stores the agreed-upon execution plan on the main runtime's
// session (the interactive agent's register_plan tool). It is called by the
// model via the register_plan tool after the user has approved a proposal, and
// stays visible (to PlanCheck) until FlushPlanFor.
func RegisterPlan(plan string) {
	RegisterPlanFor(MainRuntime(), plan)
}

// RegisterPlanFor stores a plan on the given runtime's session, creating a
// stealth dummy session when none exists so there is always a target. A nil
// receiver (or one whose session vanished) degrades to no-op rather than
// panicking, since tool dispatch can race with run teardown.
func RegisterPlanFor(rt *AgentRuntime, plan string) {
	if rt == nil {
		return
	}
	if rt.Session == nil {
		rt.Session = &data.Session{ID: -1} // stealth dummy; ensures a target exists
	}
	rt.Session.CurrentPlan = plan
	log.Printf("Plan registered for run %s (%d chars): %s", rt.ID, len(plan), plan)
}

// FlushPlan retires the main runtime's active plan. Called automatically at the
// end of every cleanly completed tool loop, so a plan can never outlive its
// task cycle.
func FlushPlan() {
	FlushPlanFor(MainRuntime())
}

// FlushPlanFor retires the given runtime's active plan. Safe to call with no
// plan (no-op), so interrupted-run cleanup paths never error.
func FlushPlanFor(rt *AgentRuntime) {
	if rt == nil || rt.Session == nil || rt.Session.CurrentPlan == "" {
		return
	}
	log.Printf("Flushing completed plan for run %s (%d chars)", rt.ID, len(rt.Session.CurrentPlan))
	rt.Session.CurrentPlan = ""
}

// writePathsAt returns the file paths touched by a single tool call, or empty
// for non-writing calls. For system-write it is the target path; for
// system-modify it is every file in the change set. The loop only appends
// calls that actually ran, so presence here means the model attempted the
// modification (even a permission-denied one is work worth re-verifying).
func writePathsAt(i int, toolsCalled []structs.IncomingToolCall) []string {
	var paths []string
	switch toolsCalled[i].Name {
	case "system-write":
		var args map[string]any
		if err := json.Unmarshal([]byte(toolsCalled[i].Arguments), &args); err == nil {
			if p, ok := args["path"].(string); ok && p != "" {
				paths = append(paths, p)
			}
		}
	case "system-modify":
		var args map[string]any
		if err := json.Unmarshal([]byte(toolsCalled[i].Arguments), &args); err == nil {
			for _, raw := range args["changes"].([]any) {
				if c, ok := raw.(map[string]any); ok {
					if f, ok := c["file"].(string); ok && f != "" {
						paths = append(paths, f)
					}
				}
			}
		}
	}
	return paths
}

// PlanCheck is the highest-priority after-turn hook. When a plan is active and
// there has been write activity since the last system-check_plan injection (or no
// check has happened this loop yet), it injects an internal-only system-check_plan
// call that mirrors the plan back to the model for self-verification before
// lower-priority post-steps (build validation) get their turn.
//
// "Since the last check" is derived directly from toolsCalled: only the hook
// ever emits a call named system-check_plan (it is not in the model's tool set), so
// scanning forward from the most recent one isolates the new, unverified work.
// If there is nothing new to verify, the hook declines and the loop proceeds
// down the priority chain — mirroring how ValidationGuard skips when a build
// already followed the newest write.
func PlanCheck(toolsCalled []structs.IncomingToolCall, rt *AgentRuntime) ([]structs.IncomingToolCall, error) {
	plan := activePlan(rt)
	if plan == "" {
		return nil, nil
	}

	lastCheckIdx := -1
	for i := len(toolsCalled) - 1; i >= 0; i-- {
		if toolsCalled[i].Name == "system-check_plan" {
			lastCheckIdx = i
			break
		}
	}

	var allModified, newModified []string
	for i := range toolsCalled {
		for _, p := range writePathsAt(i, toolsCalled) {
			allModified = append(allModified, p)
			if i > lastCheckIdx {
				newModified = append(newModified, p)
			}
		}
	}

	if len(newModified) == 0 {
		// No writes since the last check (or no writes at all yet): nothing to
		// verify. Hand the turn to lower-priority hooks.
		log.Println("PlanCheck: no new write activity since last check; declining")
		return nil, nil
	}

	log.Printf("PlanCheck: injecting system-check_plan (%d new file(s) since last check)", len(newModified))

	question := fmt.Sprintf(
		"Plan completion check.\n\nYour registered plan:\n\"\"\"\n%s\n\"\"\"\n\n"+
			"Files modified so far this turn: %s\n\n"+
			"Verify against the plan. If everything is complete, confirm briefly — the plan retires automatically when this turn closes. "+
			"If steps remain (or a planned file turned out to be unnecessary), continue working or explain which steps were skipped and why.",
		plan, formatFileList(allModified),
	)

	qArgs, _ := json.Marshal(map[string]string{"question": question})

	return []structs.IncomingToolCall{
		{
			Name:      "system-check_plan",
			Arguments: string(qArgs),
			ID:        "plan-check-injected",
			Type:      "function",
		},
	}, nil
}

func formatFileList(files []string) string {
	if len(files) == 0 {
		return "(none yet)"
	}
	return strings.Join(files, ", ")
}

func init() {
	Registry.RegisterAfter(AfterToolTurnHook{Priority: 1, Fn: PlanCheck})
}
