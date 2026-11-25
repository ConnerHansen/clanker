package tools

import (
	"clankerv2/data"
	"reflect"
	"strings"
	"testing"
	"time"
)

// withCommandSource temporarily swaps the global run-block source for a test
// table, restoring the previous value on return. An empty table simulates no
// config being loaded at all.
func withCommandSource(t *testing.T, table map[string]*data.ToolRun) {
	t.Helper()
	prev := commandSource
	if len(table) == 0 {
		SetCommandSource(nil)
	} else {
		SetCommandSource(func(name string) *data.ToolRun { return table[name] })
	}
	t.Cleanup(func() { SetCommandSource(prev) })
}

func TestResolveRunPlain(t *testing.T) {
	run, err := resolveRun("system-build", &data.ToolRun{
		Command: "make",
		Params:  []string{"build"},
	}, map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := &data.ToolRun{Command: "make", Params: []string{"build"}}
	if !reflect.DeepEqual(run, want) {
		t.Errorf("got %+v, want %+v", run, want)
	}
}

func TestResolveRunArrayParameters(t *testing.T) {
	// Array values expand element-by-element, verbatim, as given: no splitting,
	// and spaces inside an element stay intact within that single word.
	run, err := resolveRun("system-tests", &data.ToolRun{
		Command: "go",
		Params:  []string{"test", "-v", "${PARAMETERS}"},
	}, map[string]any{"parameters": []any{"./agent", "weird name"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := &data.ToolRun{Command: "go", Params: []string{"test", "-v", "./agent", "weird name"}}
	if !reflect.DeepEqual(run, want) {
		t.Errorf("got %+v, want %+v", run, want)
	}
}

// TestResolveRunStringParametersSplit locks the LEGACY string fallback: a
// plain-string parameter value (old configs, model habit) is whitespace-split
// into words.
func TestResolveRunStringParametersSplit(t *testing.T) {
	run, err := resolveRun("system-tests", &data.ToolRun{
		Command: "go",
		Params:  []string{"test", "-v", "${PARAMETERS}"},
	}, map[string]any{"parameters": "./agent -run TestFoo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := &data.ToolRun{Command: "go", Params: []string{"test", "-v", "./agent", "-run", "TestFoo"}}
	if !reflect.DeepEqual(run, want) {
		t.Errorf("got %+v, want %+v", run, want)
	}
}

func TestResolveRunDropsPlaceholderWithoutParameters(t *testing.T) {
	// A ${PARAMETERS} entry is REMOVED (not emptied) when no parameters are
	// passed, so plain invocations run with their static params only.
	run, err := resolveRun("system-tests", &data.ToolRun{
		Command: "go",
		Params:  []string{"test", "-v", "${PARAMETERS}"},
	}, map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := &data.ToolRun{Command: "go", Params: []string{"test", "-v"}}
	if !reflect.DeepEqual(run, want) {
		t.Errorf("got %+v, want %+v", run, want)
	}
}

func TestResolveRunScriptParamVerbatim(t *testing.T) {
	// A parameter literally named `script` substitutes VERBATIM as a single
	// argv element: no splitting, the value is the whole shell script.
	run, err := resolveRun("bash-cmd", &data.ToolRun{
		Command: "bash",
		Params:  []string{"-c", "${SCRIPT}"},
	}, map[string]any{"script": "echo a b && ls"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := &data.ToolRun{Command: "bash", Params: []string{"-c", "echo a b && ls"}}
	if !reflect.DeepEqual(run, want) {
		t.Errorf("got %+v, want %+v", run, want)
	}
}

// TestBashScriptToolExecutes end-to-end: bash-cmd's `script` param reaches
// bash -c as one verbatim argv element — the && chain executes for real.
func TestBashScriptToolExecutes(t *testing.T) {
	withCommandSource(t, map[string]*data.ToolRun{
		"bash-cmd": {Command: "bash", Params: []string{"-c", "${SCRIPT}"}},
	})
	withPermissionGated(t, nil)

	res := HandleToolCall(nil, nil, "bash-cmd", map[string]any{"script": `echo "it's alive" && echo done`}, nil, nil)
	if !res.Success {
		t.Fatalf("bash script tool should succeed, got %+v", res.Result)
	}
	out, _ := res.Result["stdout"].(string)
	if !strings.Contains(out, "it's alive") || !strings.Contains(out, "done") {
		t.Errorf("expected both echo outputs from the shell script, got %q", out)
	}
}

// TestBashScriptToolFailsClosedOnBlankScript: a missing or blank `script`
// must fail with an explicit error — never silently run `bash -c ""`.
func TestBashScriptToolFailsClosedOnBlankScript(t *testing.T) {
	withCommandSource(t, map[string]*data.ToolRun{
		"bash-cmd": {Command: "bash", Params: []string{"-c", "${SCRIPT}"}},
	})
	withPermissionGated(t, nil)

	for _, args := range []map[string]any{nil, {"script": "   "}} {
		res := HandleToolCall(nil, nil, "bash-cmd", args, nil, nil)
		if res.Success {
			t.Fatalf("blank/missing script must fail, got %+v", res.Result)
		}
		errMsg, _ := res.Result["error"].(string)
		if !strings.Contains(errMsg, "script") {
			t.Errorf("expected a script-missing error, got %q", errMsg)
		}
	}
}

func TestResolveRunEmbeddedPlaceholderIsSingleElement(t *testing.T) {
	// A placeholder embedded in a larger entry is substituted in place and the
	// entry stays one element (script semantics), no splitting.
	run, err := resolveRun("custom-script", &data.ToolRun{
		Command: "bash",
		Params:  []string{"-c", "echo start ${PARAMETERS} end"},
	}, map[string]any{"parameters": "a b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := &data.ToolRun{Command: "bash", Params: []string{"-c", "echo start a b end"}}
	if !reflect.DeepEqual(run, want) {
		t.Errorf("got %+v, want %+v", run, want)
	}
}

// TestResolveRunScriptParamMissingSentinel: a missing `script` arg swaps in
// the fail-closed sentinel (the handler rejects it); an empty array drops the
// entry.
func TestResolveRunScriptParamMissingSentinel(t *testing.T) {
	run, err := resolveRun("bash-cmd", &data.ToolRun{
		Command: "bash",
		Params:  []string{"-c", "${SCRIPT}"},
	}, map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := &data.ToolRun{Command: "bash", Params: []string{"-c", scriptSentinel}}
	if !reflect.DeepEqual(run, want) {
		t.Errorf("got %+v, want %+v", run, want)
	}

	run, err = resolveRun("system-tests", &data.ToolRun{
		Command: "go",
		Params:  []string{"test", "${PARAMETERS}"},
	}, map[string]any{"parameters": []any{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want = &data.ToolRun{Command: "go", Params: []string{"test"}}
	if !reflect.DeepEqual(run, want) {
		t.Errorf("empty array got %+v, want %+v", run, want)
	}
}

func TestResolveRunMultipleNamedParamsOneEntry(t *testing.T) {
	run, err := resolveRun("custom-script", &data.ToolRun{
		Command: "bash",
		Params:  []string{"-c", "${GREETING} ${NAME}"},
	}, map[string]any{"greeting": "hello", "name": "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := &data.ToolRun{Command: "bash", Params: []string{"-c", "hello world"}}
	if !reflect.DeepEqual(run, want) {
		t.Errorf("got %+v, want %+v", run, want)
	}
}

func TestResolveRunNilOrEmptyCommandErrors(t *testing.T) {
	if _, err := resolveRun("system.whatever", nil, map[string]any{}); err == nil {
		t.Error("expected an error for a nil run block")
	}
	if _, err := resolveRun("system.whatever", &data.ToolRun{Params: []string{"x"}}, map[string]any{}); err == nil {
		t.Error("expected an error for a missing command")
	}
}

func TestCommandForToolFallbackDefaults(t *testing.T) {
	withCommandSource(t, nil) // simulate no config loaded

	buildRun, err := runForTool("system-build", map[string]any{})
	if err != nil {
		t.Fatalf("build fallback error: %v", err)
	}
	wantBuild := &data.ToolRun{Command: "make", Params: []string{"build"}}
	if !reflect.DeepEqual(buildRun, wantBuild) {
		t.Errorf("build got %+v, want %+v", buildRun, wantBuild)
	}

	testsRun, err := runForTool("system-tests", map[string]any{})
	if err != nil {
		t.Fatalf("tests fallback error: %v", err)
	}
	wantTests := &data.ToolRun{Command: "go", Params: []string{"test", "-v"}, TimeoutSeconds: 30}
	if !reflect.DeepEqual(testsRun, wantTests) {
		t.Errorf("tests got %+v, want %+v", testsRun, wantTests)
	}

	testsTargeted, err := runForTool("system-tests", map[string]any{"parameters": []any{"./agent"}})
	if err != nil {
		t.Fatalf("tests targeted fallback error: %v", err)
	}
	wantTargeted := &data.ToolRun{Command: "go", Params: []string{"test", "-v", "./agent"}, TimeoutSeconds: 30}
	if !reflect.DeepEqual(testsTargeted, wantTargeted) {
		t.Errorf("tests targeted got %+v, want %+v", testsTargeted, wantTargeted)
	}

	if _, err := runForTool("system.unknown", nil); err == nil {
		t.Error("expected an error for an unknown tool")
	}
}

func TestRunForToolUsesConfiguredRuns(t *testing.T) {
	withCommandSource(t, map[string]*data.ToolRun{
		"system-build": {Command: "cargo", Params: []string{"build"}},
		"system-tests": {Command: "cargo", Params: []string{"test", "${PARAMETERS}"}},
	})

	buildRun, err := runForTool("system-build", map[string]any{})
	if err != nil {
		t.Fatalf("build error: %v", err)
	}
	wantBuild := &data.ToolRun{Command: "cargo", Params: []string{"build"}}
	if !reflect.DeepEqual(buildRun, wantBuild) {
		t.Errorf("build got %+v, want %+v", buildRun, wantBuild)
	}

	testsRun, err := runForTool("system-tests", map[string]any{"parameters": []any{"--lib"}})
	if err != nil {
		t.Fatalf("tests error: %v", err)
	}
	wantTests := &data.ToolRun{Command: "cargo", Params: []string{"test", "--lib"}}
	if !reflect.DeepEqual(testsRun, wantTests) {
		t.Errorf("tests got %+v, want %+v", testsRun, wantTests)
	}

	// Fallback defaults only cover the two known command tools; anything else
	// must be configured explicitly.
	if _, err := runForTool("system.unknown-tool", map[string]any{}); err == nil {
		t.Error("expected an error for a tool with no configured run block")
	}
}

// withPermissionGated temporarily swaps the global requestPermission lookup
// for a test table (tool name -> flagged), restoring the previous value on
// return. An empty table simulates a config with no flags.
func withPermissionGated(t *testing.T, gated map[string]bool) {
	t.Helper()
	prev := permissionGated
	if len(gated) == 0 {
		SetPermissionGated(nil)
	} else {
		SetPermissionGated(func(name string) bool { return gated[name] })
	}
	t.Cleanup(func() { SetPermissionGated(prev) })
}

// TestCommandToolFallbackExecutes drives the generic command-backed path: a
// configured tool with NO built-in handler runs its resolved argv through the
// default case (this is what makes custom script-runner tools executable).
func TestCommandToolFallbackExecutes(t *testing.T) {
	withCommandSource(t, map[string]*data.ToolRun{
		"custom-script": {Command: "echo", Params: []string{"ran", "${PARAMETERS}"}},
	})
	withPermissionGated(t, nil)

	res := HandleToolCall(nil, nil, "custom-script", map[string]any{"parameters": []any{"--all", "now"}}, nil, nil)
	if !res.Success {
		t.Fatalf("fallback command tool should succeed, got %+v", res.Result)
	}
	out, _ := res.Result["stdout"].(string)
	if !strings.Contains(out, "ran --all now") {
		t.Errorf("expected echoed expanded params in stdout, got %q", out)
	}
}

// TestCommandToolFallbackUnknownWithoutConfig: no run block for the name and
// no built-in default -> a clean unknown-tool failure, nothing executed.
func TestCommandToolFallbackUnknownWithoutConfig(t *testing.T) {
	withCommandSource(t, nil)
	res := HandleToolCall(nil, nil, "nope-tool", map[string]any{}, nil, nil)
	if res.Success {
		t.Fatalf("unconfigured tool should fail, got %+v", res.Result)
	}
	errMsg, _ := res.Result["error"].(string)
	if !strings.Contains(errMsg, "unknown tool") {
		t.Errorf("expected unknown tool error, got %q", errMsg)
	}
}

// TestFlaggedCommandToolRequiresPermission locks the requestPermission contract:
// approval runs the command (the FULLY assembled argv, ${PARAMETERS} expanded,
// rides in the script viewport payload), denial stops before execution, and a
// flagged tool with no connected UI fails closed.
func TestFlaggedCommandToolRequiresPermission(t *testing.T) {
	withCommandSource(t, map[string]*data.ToolRun{
		"custom-script": {Command: "echo", Params: []string{"run", "${PARAMETERS}"}},
	})
	withPermissionGated(t, map[string]bool{"custom-script": true})

	captured := permCapture{}
	res := HandleToolCall(nil, nil, "custom-script", map[string]any{"parameters": []any{"--all"}}, captured.fn(true), nil)
	if !res.Success {
		t.Fatalf("approved flagged tool should succeed, got %+v", res.Result)
	}
	want := "custom-script requests permission to run a command"
	if captured.prompt != want {
		t.Errorf("prompt = %q, want %q", captured.prompt, want)
	}
	if captured.viewport == nil || captured.viewport.Kind != "script" ||
		captured.viewport.Title != "custom-script command" ||
		captured.viewport.Content != "echo run --all" {
		t.Errorf("script viewport wrong: %+v", captured.viewport)
	}
	if captured.tool != "custom-script" || captured.action != "execute" {
		t.Errorf("request = tool %q action %q, want custom-script/execute", captured.tool, captured.action)
	}

	captured = permCapture{}
	res = HandleToolCall(nil, nil, "custom-script", map[string]any{}, captured.fn(false), nil)
	if res.Success {
		t.Fatalf("denied flagged tool must not run, got %+v", res.Result)
	}
	denied, _ := res.Result["result"].(string)
	if !strings.Contains(denied, "denied") {
		t.Errorf("expected denial surfaced to the model, got %q", denied)
	}

	// No connected UI: fail closed (the command must not execute).
	res = HandleToolCall(nil, nil, "custom-script", map[string]any{}, nil, nil)
	if res.Success {
		t.Fatalf("flagged tool without a UI should fail, got %+v", res.Result)
	}
	errMsg, _ := res.Result["error"].(string)
	if !strings.Contains(errMsg, "requires user approval") {
		t.Errorf("expected fail-closed error, got %q", errMsg)
	}
}

// TestUnflaggedCommandToolSkipsPermission: the permission requester is NOT
// consulted for tools without the flag — custom tooling stays frictionless by
// default.
func TestUnflaggedCommandToolSkipsPermission(t *testing.T) {
	withCommandSource(t, map[string]*data.ToolRun{
		"custom-go": {Command: "go", Params: []string{"version"}},
	})
	withPermissionGated(t, map[string]bool{"custom-go": false})

	prompted := false
	req := func(channel chan any, tool, path, action, prompt string, viewport *data.PermissionViewport) string {
		prompted = true
		return "Operation approved by the user"
	}
	res := HandleToolCall(nil, nil, "custom-go", map[string]any{}, req, nil)
	if !res.Success {
		t.Fatalf("unflagged tool should succeed, got %+v", res.Result)
	}
	if prompted {
		t.Error("permission requester must not be consulted for unflagged tools")
	}
}

// TestCommandDisplay locks the assembled-command rendering used in permission
// prompts: bare command when no params, space-joined argv otherwise.
func TestCommandDisplay(t *testing.T) {
	if got := commandDisplay("make", nil); got != "make" {
		t.Errorf("no-params display = %q, want make", got)
	}
	if got := commandDisplay("go", []string{"test", "-v", "./agent"}); got != "go test -v ./agent" {
		t.Errorf("display = %q, want go test -v ./agent", got)
	}
}

// TestConsolidatedCommandToolsRouteThroughDefault locks the consolidation
// contract: system-build / system-tests carry no built-in handler case and run
// through the generic command-backed path, so their requestPermission flag is
// honored (this was silently ignored before they collapsed into default:).
func TestConsolidatedCommandToolsRouteThroughDefault(t *testing.T) {
	withCommandSource(t, map[string]*data.ToolRun{
		"system-tests": {Command: "echo", Params: []string{"ran", "${PARAMETERS}"}, TimeoutSeconds: 30},
	})

	// Flagged + model call (approved): prompts with a short label; the full
	// assembled command rides in the script viewport payload.
	withPermissionGated(t, map[string]bool{"system-tests": true})
	captured := permCapture{}
	res := HandleToolCall(nil, nil, "system-tests", map[string]any{"parameters": []any{"./agent"}}, captured.fn(true), nil)
	if !res.Success {
		t.Fatalf("approved consolidated tool should succeed, got %+v", res.Result)
	}
	want := "system-tests requests permission to run a command"
	if captured.prompt != want {
		t.Errorf("prompt = %q, want %q", captured.prompt, want)
	}
	if captured.viewport == nil || captured.viewport.Kind != "script" ||
		captured.viewport.Title != "system-tests command" ||
		captured.viewport.Content != "echo ran ./agent" {
		t.Errorf("script viewport wrong: %+v", captured.viewport)
	}

	// Flagged + model call (denied): must not execute.
	withPermissionGated(t, map[string]bool{"system-tests": true})
	deniedCap := permCapture{}
	res = HandleToolCall(nil, nil, "system-tests", map[string]any{}, deniedCap.fn(false), nil)
	if res.Success {
		t.Fatalf("denied consolidated tool must not run, got %+v", res.Result)
	}

	// Flagged + no connected UI: fail closed.
	withPermissionGated(t, map[string]bool{"system-tests": true})
	res = HandleToolCall(nil, nil, "system-tests", map[string]any{}, nil, nil)
	if res.Success {
		t.Fatalf("flagged tool without a UI should fail, got %+v", res.Result)
	}

	// Unflagged: no prompt at all.
	withPermissionGated(t, nil)
	prompted := false
	req := func(channel chan any, tool, path, action, prompt string, viewport *data.PermissionViewport) string {
		prompted = true
		return "Operation approved by the user"
	}
	res = HandleToolCall(nil, nil, "system-tests", map[string]any{}, req, nil)
	if !res.Success {
		t.Fatalf("unflagged consolidated tool should succeed, got %+v", res.Result)
	}
	if prompted {
		t.Error("permission requester must not be consulted for unflagged tools")
	}
}

// TestInjectedGatedToolBypassesPermission locks the injection contract:
// hook-injected runs of a flagged command tool execute WITHOUT prompting (and
// without failing closed) — they are agent-internal post-steps, and the prompt
// is reserved for model requests.
func TestInjectedGatedToolBypassesPermission(t *testing.T) {
	withCommandSource(t, map[string]*data.ToolRun{
		"system-tests": {Command: "echo", Params: []string{"injected"}},
	})
	withPermissionGated(t, map[string]bool{"system-tests": true})

	injCap := permCapture{}
	res := HandleInjectedToolCall(nil, nil, "system-tests", map[string]any{}, injCap.fn(false), nil)
	if !res.Success {
		t.Fatalf("injected gated tool should run without a prompt, got %+v", res.Result)
	}
	out, _ := res.Result["stdout"].(string)
	if !strings.Contains(out, "injected") {
		t.Errorf("expected echoed output from the injected run, got %q", out)
	}
}

// TestRunBlockTimeoutSecondsHonored locks per-tool timeout plumbing: a 1s cap
// kills a 5s sleep promptly (well under the 5m default) with the timeout error.
func TestRunBlockTimeoutSecondsHonored(t *testing.T) {
	withCommandSource(t, map[string]*data.ToolRun{
		"slow-tool": {Command: "sleep", Params: []string{"5"}, TimeoutSeconds: 1},
	})
	withPermissionGated(t, nil)

	start := time.Now()
	res := HandleToolCall(nil, nil, "slow-tool", map[string]any{}, nil, nil)
	elapsed := time.Since(start)
	if res.Success {
		t.Fatalf("timed-out command should not succeed, got %+v", res.Result)
	}
	errMsg, _ := res.Result["error"].(string)
	if !strings.Contains(errMsg, "timed out") {
		t.Errorf("expected timeout error, got %q", errMsg)
	}
	if elapsed >= 3*time.Second {
		t.Errorf("1s cap not honored; execution took %s", elapsed)
	}
}
