package agent

import (
	"clankerv2/agent/tools"
	"clankerv2/data"
	"os"
	"strings"
	"testing"
)

// TestRuntimesNeverCrossContaminate locks the core de-globalization guarantee:
// two live AgentRuntimes must not observe each other's history, session,
// stealth flag, or tool set. This is what makes subagents possible — before
// the refactor all of these lived in one process-global config.
func TestRuntimesNeverCrossContaminate(t *testing.T) {
	setTestAppConfig(t, &data.AppConfig{Models: []data.ModelConfig{modelCfg("m1", "openai", "http://a")}})

	// Both runtimes are stealth (dummy -1 sessions, created lazily, no DB
	// writes) so the test is hermetic: the guarantee under lock is that each
	// run's STATE — history, session, stealth flag, tool set — is independent,
	// which is what makes concurrent runs possible. The persistence branch
	// (stealth vs not) is exercised separately against real databases.
	main := NewAgentRuntime()
	main.Stealth = true // app-level state lives on the main runtime, per-run

	sub := &AgentRuntime{
		ID:      "sub-1",
		Role:    RoleSubagent,
		ModelID: "m1",
		Stealth: true,
		Slot:    "sub-1",
	}

	// Messages never leak across runtimes.
	main.AppendMessage("user", "m1", "main only")
	if len(sub.Messages) != 0 {
		t.Fatalf("main's append touched sub's history: %d message(s)", len(sub.Messages))
	}
	for _, m := range main.Messages {
		if c, _ := m["content"].(string); strings.Contains(c, "sub only") {
			t.Fatal("sub's message content leaked into main's history")
		}
	}

	// Stealth is per-run: both stay exactly where the app put them.
	if !main.Stealth || !sub.Stealth {
		t.Fatalf("stealth flags crossed: main=%v sub=%v", main.Stealth, sub.Stealth)
	}

	// Tool sets are per-run: sub's filtered set never leaks into main's and
	// vice versa.
	sub.Tools = filterTools([]data.Tool{mkTool("system-read"), mkTool("system-write")}, []string{"system-read"}, nil)
	if main.HasTool("system-write") != true {
		t.Fatal("main (unfiltered) must have every tool")
	}
	if sub.HasTool("system-write") || !sub.HasTool("system-read") {
		t.Fatalf("sub's filtered set wrong: read=%v write=%v", sub.HasTool("system-read"), sub.HasTool("system-write"))
	}

	// Session is per-run: adopting one runtime's session never touches the
	// other, and sub's lazily-created stealth dummy stays out of main.
	main.SetSession(&data.Session{ID: -1, Name: "test-main"}, nil)
	if sub.Session != nil {
		t.Fatalf("main's session adoption set sub's session: %+v", sub.Session)
	}

	sub.InferSession("dummy check") // stealth: creates the dummy (-1) in place
	if main.Session.Name != "test-main" || main.Session.ID != -1 {
		t.Fatalf("sub's InferSession disturbed main's session: %+v", main.Session)
	}
}

// TestHasToolGate locks the defense-in-depth check used by the tool handler:
// filtered runtimes deny tools they were never granted; unfiltered runtimes
// (nil/empty Tools = "everything configured") always pass.
func TestHasToolGate(t *testing.T) {
	empty := &AgentRuntime{ID: "main"}
	if !empty.HasTool("system-write") {
		t.Fatal("empty tool set must mean 'everything' for the main runtime")
	}

	filt := &AgentRuntime{ID: "sub", Tools: []data.Tool{mkTool("system-read")}}
	if !filt.HasTool("system-read") || filt.HasTool("system-write") {
		t.Fatalf("filtered membership wrong: read=%v write=%v", filt.HasTool("system-read"), filt.HasTool("system-write"))
	}
}

// fakeFilterRuntime is a minimal tools.ToolRuntime + ToolFilterer for testing
// the handler's denial gate without a real AgentRuntime.
type fakeFilterRuntime struct {
	tools []string
}

func (f fakeFilterRuntime) RunID() string           { return "fake" }
func (f fakeFilterRuntime) OwnerModel() string      { return "" }
func (f fakeFilterRuntime) RegisterPlan(string)     {}
func (f fakeFilterRuntime) HasTool(name string) bool {
	for _, n := range f.tools {
		if n == name {
			return true
		}
	}
	return false
}

// TestHandlerDeniesFilteredTools drives the REAL handler with a filtered
// runtime: calls to ungranted tools must come back as an error envelope and
// never execute; granted tools still work.
func TestHandlerDeniesFilteredTools(t *testing.T) {
	readonly := fakeFilterRuntime{tools: []string{"system-read"}}

	if res := tools.HandleToolCall(readonly, nil, "system-write", map[string]any{"path": "/tmp/never-written-by-test", "content": "x"}, nil, nil); res.Success {
		t.Fatal("write on a read-only runtime must be denied before execution")
	}

	// Granted tool still executes: point it at a real file.
	f := t.TempDir() + "/note.txt"
	if err := os.WriteFile(f, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := tools.HandleToolCall(readonly, nil, "system-read", map[string]any{"path": f}, nil, nil); !res.Success {
		t.Fatalf("granted tool (read) must still succeed: %v", res.Result)
	}

	// A runtime without a filter (main agent semantics) is never gated.
	unfiltered := fakeRuntimeNoFilter{}
	if res := tools.HandleToolCall(unfiltered, nil, "system-read", map[string]any{"path": f}, nil, nil); !res.Success {
		t.Fatalf("unfiltered runtime must not be gated: %v", res.Result)
	}
}

// TestHandlerCheckPlanBypass locks the hook-injection contract: internal
// post-step tools (system-check_plan) are in no model's tool set, so a MODEL
// call to one on a filtered runtime must still be denied — but the same call
// arriving as a HOOK INJECTION executes regardless of the granted set. The
// main runtime (no filter) passes both paths.
func TestHandlerCheckPlanBypass(t *testing.T) {
	question := "did you finish the plan?"
	args := map[string]any{"question": question}

	// A filtered subagent was never granted system-check_plan: a model call is
	// denied, an injection runs.
	filtered := fakeFilterRuntime{tools: []string{"system-read"}}
	if res := tools.HandleToolCall(filtered, nil, "system-check_plan", args, nil, nil); res.Success {
		t.Fatalf("model call to ungranted check_plan must be denied on a filtered runtime")
	}
	injected := tools.HandleInjectedToolCall(filtered, nil, "system-check_plan", args, nil, nil)
	if !injected.Success {
		t.Fatalf("hook-injected check_plan must run despite the filtered set: %v", injected.Result)
	}
	if injected.RawResult == nil || *injected.RawResult != question {
		t.Fatalf("injection did not echo the plan-check question: got %v", injected.RawResult)
	}

	// The main runtime (no filter) executes it through either path.
	main := fakeRuntimeNoFilter{}
	if res := tools.HandleToolCall(main, nil, "system-check_plan", args, nil, nil); !res.Success {
		t.Fatalf("unfiltered runtime must run check_plan: %v", res.Result)
	}
}

// fakeRuntimeNoFilter implements ToolRuntime without ToolFilterer (the main
// runtime / legacy fakes). The gate must leave it completely alone.
type fakeRuntimeNoFilter struct{}

func (fakeRuntimeNoFilter) RunID() string        { return "main" }
func (fakeRuntimeNoFilter) OwnerModel() string   { return "" }
func (fakeRuntimeNoFilter) RegisterPlan(string)  {}
