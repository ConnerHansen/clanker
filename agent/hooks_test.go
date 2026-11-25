package agent

import (
	"clankerv2/agent/structs"
	"errors"
	"testing"
)

var errTestHook = errors.New("hook deliberately failed")


// makeHook returns a hook with the given name/priority that reports itself
// when invoked and optionally injects the given calls.
func makeHook(name string, priority int, injected []structs.IncomingToolCall, wasRun *[]string) AfterToolTurnHook {
	return AfterToolTurnHook{
		Priority: priority,
		Fn: func(toolsCalled []structs.IncomingToolCall, rt *AgentRuntime) ([]structs.IncomingToolCall, error) {
			*wasRun = append(*wasRun, name)
			return injected, nil
		},
	}
}

func TestExecuteAfterShortCircuits(t *testing.T) {
	reg := &HookRegistry{}
	var wasRun []string

	low := makeHook("priority-1", 1, []structs.IncomingToolCall{{Name: "from-priority-1"}}, &wasRun)
	high := makeHook("priority-2", 2, []structs.IncomingToolCall{{Name: "from-priority-2"}}, &wasRun)

	// Registration order is deliberately reversed to prove ordering comes from priority, not insertion.
	reg.RegisterAfter(high)
	reg.RegisterAfter(low)

	injected, err := reg.ExecuteAfter(nil, MainRuntime())
	if err != nil {
		t.Fatalf("ExecuteAfter error: %v", err)
	}
	if len(injected) != 1 || injected[0].Name != "from-priority-1" {
		t.Fatalf("expected priority-1 injection to win, got %+v", injected)
	}
	if len(wasRun) != 1 || wasRun[0] != "priority-1" {
		t.Fatalf("expected short-circuit (only priority-1 ran), got run order %v", wasRun)
	}
}

func TestExecuteAfterFallsThroughToNext(t *testing.T) {
	reg := &HookRegistry{}
	var wasRun []string

	low := makeHook("priority-1", 1, nil, &wasRun) // declines
	high := makeHook("priority-2", 2, []structs.IncomingToolCall{{Name: "system-build"}}, &wasRun)

	reg.RegisterAfter(high)
	reg.RegisterAfter(low)

	injected, err := reg.ExecuteAfter(nil, MainRuntime())
	if err != nil {
		t.Fatalf("ExecuteAfter error: %v", err)
	}
	if len(injected) != 1 || injected[0].Name != "system-build" {
		t.Fatalf("expected priority-2 injection after decline, got %+v", injected)
	}
	if len(wasRun) != 2 || wasRun[0] != "priority-1" || wasRun[1] != "priority-2" {
		t.Fatalf("expected both hooks evaluated in priority order, got %v", wasRun)
	}
}

func TestExecuteAfterAllDecline(t *testing.T) {
	reg := &HookRegistry{}
	var wasRun []string

	reg.RegisterAfter(makeHook("h1", 1, nil, &wasRun))
	reg.RegisterAfter(makeHook("h2", 2, nil, &wasRun))

	injected, err := reg.ExecuteAfter(nil, MainRuntime())
	if err != nil {
		t.Fatalf("ExecuteAfter error: %v", err)
	}
	if len(injected) != 0 {
		t.Fatalf("expected no injection when all hooks decline, got %+v", injected)
	}
	if len(wasRun) != 2 {
		t.Fatalf("expected both hooks evaluated, got %v", wasRun)
	}
}

func TestExecuteAfterSkipsErroringHook(t *testing.T) {
	reg := &HookRegistry{}
	var wasRun []string

	broken := AfterToolTurnHook{Priority: 1, Fn: func([]structs.IncomingToolCall, *AgentRuntime) ([]structs.IncomingToolCall, error) {
		wasRun = append(wasRun, "broken")
		return nil, errTestHook
	}}
	fallback := makeHook("priority-2", 2, []structs.IncomingToolCall{{Name: "system-build"}}, &wasRun)

	reg.RegisterAfter(broken)
	reg.RegisterAfter(fallback)

	injected, err := reg.ExecuteAfter(nil, MainRuntime())
	if err != nil {
		t.Fatalf("ExecuteAfter should not propagate hook errors, got: %v", err)
	}
	if len(injected) != 1 || injected[0].Name != "system-build" {
		t.Fatalf("expected fallback injection after erroring hook, got %+v", injected)
	}
	if len(wasRun) != 2 || wasRun[0] != "broken" || wasRun[1] != "priority-2" {
		t.Fatalf("expected erroring hook to be skipped in order, got %v", wasRun)
	}
}
