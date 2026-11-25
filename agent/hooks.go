package agent

import (
	"clankerv2/agent/structs"
	"log"
)

// AfterToolTurnHook is called after all LLM turns and tool executions for a user prompt are complete.
// It receives the calls executed this turn plus the runtime that owns them, and
// can return a list of ToolCalls that should be injected back into the loop as if requested by the model,
// forcing another round of processing (e.g., to perform missing validation). The Priority field orders the
// hooks: lower values run first, and ExecuteAfter short-circuits on the first hook that injects anything.
type AfterToolTurnHook struct {
	Priority int
	Fn       func(toolsCalled []structs.IncomingToolCall, rt *AgentRuntime) ([]structs.IncomingToolCall, error)
}

// ToolCall represents a simplified tool execution record used for hook analysis.
type ToolCall struct {
	Name string
	Args map[string]any
}

// BeginToolTurnHook is called when the agent first decides to execute tools in a turn.
type BeginToolTurnHook func()

type HookRegistry struct {
	beforeHooks []BeginToolTurnHook
	afterHooks  []AfterToolTurnHook
}

var Registry = &HookRegistry{
	beforeHooks: make([]BeginToolTurnHook, 0),
	afterHooks:  make([]AfterToolTurnHook, 0),
}

func (r *HookRegistry) RegisterBefore(h BeginToolTurnHook) {
	r.beforeHooks = append(r.beforeHooks, h)
}

// RegisterAfter registers an after-turn hook. Hooks self-register from their
// own init(); execution order is derived from the hook's Priority field.
func (r *HookRegistry) RegisterAfter(h AfterToolTurnHook) {
	r.afterHooks = append(r.afterHooks, h)
}

func (r *HookRegistry) ExecuteBefore() {
	for _, h := range r.beforeHooks {
		h()
	}
}

// ExecuteAfter visits after-turn hooks in ascending priority order and returns
// the injection from the first hook to produce one, short-circuiting any
// lower-priority hooks for this cycle. A hook that declines (or errors) simply
// passes its turn to the next priority; it will be re-evaluated on the next
// quiet evaluation, after the model has had a chance to act on an earlier
// injection. This is how serialized post-steps work: e.g., plan verification
// (priority 1) gets first dibs, and build validation (priority 2) only fires
// once the plan hook has nothing left to ask.
func (r *HookRegistry) ExecuteAfter(toolsCalled []structs.IncomingToolCall, rt *AgentRuntime) ([]structs.IncomingToolCall, error) {
	ordered := append([]AfterToolTurnHook(nil), r.afterHooks...)
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && ordered[j].Priority < ordered[j-1].Priority; j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}

	log.Printf("Running %d after execution hooks (short-circuit on first injection)", len(ordered))
	for _, h := range ordered {
		injected, err := h.Fn(toolsCalled, rt)
		if err != nil {
			log.Printf("Hook execution error: %v", err)
			continue
		}
		if len(injected) > 0 {
			return injected, nil
		}
	}

	return nil, nil
}
