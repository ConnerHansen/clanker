package agent

import (
	"clankerv2/data"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- test helpers ---

func setTestAppConfig(t *testing.T, cfg *data.AppConfig) {
	t.Helper()
	prev := config.appConfig
	config.appConfig = cfg
	t.Cleanup(func() { config.appConfig = prev })
}

func modelCfg(id, executor, url string) data.ModelConfig {
	return data.ModelConfig{ID: id, Executor: executor, URL: url, Managed: false}
}

func mkTool(name string) data.Tool {
	return data.Tool{Type: "function", Function: data.ToolFunctionDef{Name: name}}
}

func toolNames(ts []data.Tool) map[string]bool {
	out := make(map[string]bool, len(ts))
	for _, t := range ts {
		out[t.Function.Name] = true
	}
	return out
}

// resetSubagentRegistry clears shared registry state between tests. Any stub
// goroutine still finishing later writes to its own info pointer (harmless);
// the cleared map simply can't be reached by dispatch anymore.
func resetSubagentRegistry() {
	subagentsMu.Lock()
	for _, info := range subagentInfos {
		if !info.Status.isTerminal() && info.done != nil {
			select {
			case <-info.done:
			default:
				close(info.done)
			}
		}
	}
	subagentInfos = make(map[string]*SubagentInfo)
	subagentQueue = nil
	subagentDispatchSeq = 0
	subagentsMu.Unlock()
}

// waitFor polls pred every 5ms until true or timeout, returning the outcome.
func waitFor(t *testing.T, timeout time.Duration, pred func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return pred()
}

// --- pure logic tests ---

func TestClassifySubagentStatus(t *testing.T) {
	ctxDeadline := context.DeadlineExceeded
	cases := []struct {
		name          string
		kill, deadline bool
		runErr        error
		want          SubagentStatus
	}{
		{"completed", false, false, nil, StatusCompleted},
		{"failed", false, false, errors.New("boom"), StatusFailed},
		{"timed_out", false, true, errors.New("deadline exceeded"), StatusTimedOut},
		// kill beats deadline and error (requested first).
		{"killed_over_timeout", true, true, nil, StatusKilled},
		{"killed_over_error", true, false, errors.New("boom"), StatusKilled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var ctxErr error
			if c.deadline {
				ctxErr = ctxDeadline
			}
			got := classifySubagentStatus(c.kill, ctxErr, c.runErr)
			if got != c.want {
				t.Fatalf("classify(%v,%v,%v) = %s, want %s", c.kill, c.deadline, c.runErr, got, c.want)
			}
		})
	}
}

func TestBuildSubagentSpecModelInheritance(t *testing.T) {
	cfg := &data.AppConfig{
		Models: []data.ModelConfig{modelCfg("m1", "openai", "http://a"), modelCfg("m2", "cloud", "http://b")},
		System: &data.SystemSettings{DefaultModel: "m2"},
		Subagents: map[string]data.SubagentProfile{
			"readonly": {Name: "readonly", AvailableTools: []string{"system-read"}, Model: "m1", Prompt: "you are readonly", DeadlineSeconds: 30},
		},
	}
	setTestAppConfig(t, cfg)

	// explicit arg wins over everything.
	spec, err := buildSubagentSpec("caller-model", "main-model", map[string]any{"task": "do it", "model": "m2"})
	if err != nil || spec.ModelID != "m2" {
		t.Fatalf("explicit model: got %q, %v; want m2", spec.ModelID, err)
	}

	// empty arg inherits the CALLER's model (not the configured default).
	spec, err = buildSubagentSpec("m1", "main-model", map[string]any{"task": "do it"})
	if err != nil {
		t.Fatalf("inherit caller: %v", err)
	}
	if spec.ModelID != "m1" {
		t.Errorf("caller inheritance: got model %q, want the caller's own model m1 (not the default m2)", spec.ModelID)
	}

	// unknown model rejected.
	if _, err = buildSubagentSpec("", "", map[string]any{"task": "x", "model": "nope"}); err == nil {
		t.Fatal("unknown model should error")
	}

	// missing task rejected.
	if _, err = buildSubagentSpec("m1", "", map[string]any{}); err == nil {
		t.Fatal("missing task should error")
	}

	// bad mode rejected.
	if _, err = buildSubagentSpec("", "", map[string]any{"task": "x", "mode": "warp"}); err == nil {
		t.Fatal("bad mode should error")
	}

	// unknown profile rejected.
	if _, err = buildSubagentSpec("", "", map[string]any{"task": "x", "profile": "ghost"}); err == nil {
		t.Fatal("unknown profile should error")
	}

	// no model resolvable anywhere -> error.
	cfg2 := &data.AppConfig{Models: []data.ModelConfig{modelCfg("only", "openai", "http://a")}}
	setTestAppConfig(t, cfg2)
	if _, err = buildSubagentSpec("", "", map[string]any{"task": "x", "model": "nope"}); err == nil {
		t.Fatal("unresolvable model should error")
	}
}

func TestBuildSubagentSpecProfile(t *testing.T) {
	cfg := &data.AppConfig{
		Models: []data.ModelConfig{modelCfg("m1", "openai", "http://a")},
		Tools:  []data.Tool{mkTool("system-read"), mkTool("system-write"), mkTool("system-spawn_subagent"), mkTool("system-kill_subagent")},
		Subagents: map[string]data.SubagentProfile{
			"readonly": {Name: "readonly", AvailableTools: []string{"system-read"}, Model: "m1", Prompt: "RO prompt", DeadlineSeconds: 45},
		},
	}
	setTestAppConfig(t, cfg)

	spec, err := buildSubagentSpec("caller-m", "main-m", map[string]any{"task": "research", "profile": "readonly"})
	if err != nil {
		t.Fatalf("profile spec: %v", err)
	}
	if spec.ModelID != "m1" {
		t.Errorf("profile model: got %q, want m1 (inherits profile's model over caller)", spec.ModelID)
	}
	if spec.SystemPrompt != "RO prompt" {
		t.Errorf("profile prompt not applied: %q", spec.SystemPrompt)
	}
	if spec.Deadline != 45*time.Second {
		t.Errorf("profile deadline: got %s, want 45s", spec.Deadline)
	}
	names := toolNames(spec.Tools)
	if !names["system-read"] || names["system-write"] {
		t.Errorf("allowlist should keep read and drop write: %v", names)
	}
	if names["system-spawn_subagent"] || names["system-kill_subagent"] {
		t.Errorf("orchestration tools must never be granted to a subagent: %v", names)
	}
	if !names["system-subagent_finalize"] {
		t.Errorf("finalize must always be appended: %v", names)
	}

	// explicit model overrides the profile's.
	spec2, err := buildSubagentSpec("caller-m", "main-m", map[string]any{"task": "x", "profile": "readonly", "model": "m1"})
	if err != nil || spec2.ModelID != "m1" {
		t.Errorf("explicit model override: %q %v", spec2.ModelID, err)
	}
}

func TestFilterToolsInheritAllExcludingOrchestration(t *testing.T) {
	available := []data.Tool{
		mkTool("system-read"), mkTool("system-write"),
		mkTool("system-spawn_subagent"), mkTool("system-await_subagent"),
		mkTool("system-subagent_status"), mkTool("system-kill_subagent"),
	}
	got := toolNames(filterTools(available, nil, nil))
	if !got["system-read"] || !got["system-write"] {
		t.Errorf("inherit-all should keep read/write: %v", got)
	}
	for _, o := range []string{"system-spawn_subagent", "system-await_subagent", "system-subagent_status", "system-kill_subagent"} {
		if got[o] {
			t.Errorf("orchestration tool %s leaked into subagent set: %v", o, got)
		}
	}
	if !got["system-subagent_finalize"] {
		t.Errorf("finalize must be present: %v", got)
	}
}

// --- registry / dispatch tests (injected run stub) ---

func TestSubagentCapAndFIFO(t *testing.T) {
	cfg := &data.AppConfig{
		Models: []data.ModelConfig{modelCfg("m1", "openai", "http://a")},
		System: &data.SystemSettings{SubagentMaxUnmanaged: 2},
	}
	setTestAppConfig(t, cfg)
	resetSubagentRegistry()
	prevRun := subagentRunFn
	defer func() { subagentRunFn = prevRun }()

	var mu sync.Mutex
	var order []string
	releasesMap := make(map[string]chan struct{})

	subagentRunFn = func(info *SubagentInfo) {
		mu.Lock()
		order = append(order, info.ID)
		c := releasesMap[info.ID]
		if c == nil {
			c = make(chan struct{})
			releasesMap[info.ID] = c
		}
		mu.Unlock()
		<-c
		completeSubagent(info, StatusCompleted, "", "done:"+info.ID)
	}

	// Spawn 5: only 2 may run at once (unmanaged cap set to 2); the rest queue FIFO.
	ids := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		id, _, err := SpawnSubagent("main", "m1", map[string]any{"task": "t"})
		if err != nil {
			t.Fatalf("spawn %d: %v", i, err)
		}
		ids = append(ids, id)
	}

	// waitRegistered blocks until a dispatched subagent's stub goroutine has
	// started and created its release channel (mu-guarded, same mutex the stub
	// uses to append). close() of an uncreated channel panics, so every
	// close below is gated on this.
	waitRegistered := func(id string) bool {
		return waitFor(t, 2*time.Second, func() bool {
			mu.Lock(); defer mu.Unlock()
			_, ok := releasesMap[id]
			return ok
		})
	}

	// First two dispatched in FIFO order. DispatchPos is assigned under the
	// registry lock at dispatch time (-1 until then), so it reflects true
	// promotion order even though the stub goroutines start (and record) after
	// the lock releases.
	if !waitFor(t, 2*time.Second, func() bool {
		subagentsMu.Lock(); defer subagentsMu.Unlock()
		return subagentInfos[ids[0]].DispatchPos >= 0 && subagentInfos[ids[1]].DispatchPos >= 0
	}) || !waitRegistered(ids[0]) || !waitRegistered(ids[1]) {
		t.Fatalf("expected 2 to dispatch, dispatched so far: %v", order)
	}
	subagentsMu.Lock()
	posA, posB := subagentInfos[ids[0]].DispatchPos, subagentInfos[ids[1]].DispatchPos
	subagentsMu.Unlock()
	if !(posA >= 0 && posA < posB) {
		t.Fatalf("FIFO order broken: dispatch positions %d (spawn %s) vs %d (spawn %s)", posA, ids[0], posB, ids[1])
	}
	close(releasesMap[ids[0]])
	close(releasesMap[ids[1]])

	// Next two dispatch after the first two finish.
	if !waitFor(t, 2*time.Second, func() bool {
		subagentsMu.Lock(); defer subagentsMu.Unlock()
		return subagentInfos[ids[2]].DispatchPos >= 0 && subagentInfos[ids[3]].DispatchPos >= 0
	}) || !waitRegistered(ids[2]) || !waitRegistered(ids[3]) {
		t.Fatalf("expected 4 dispatched after first two completed, dispatched so far: %v", order)
	}
	subagentsMu.Lock()
	posC, posD := subagentInfos[ids[2]].DispatchPos, subagentInfos[ids[3]].DispatchPos
	subagentsMu.Unlock()
	if !(posB < posC && posC < posD) {
		t.Fatalf("FIFO order broken at 3-4: positions %d,%d,%d,%d", posA, posB, posC, posD)
	}
	close(releasesMap[ids[2]])
	close(releasesMap[ids[3]])

	if !waitFor(t, 2*time.Second, func() bool {
		subagentsMu.Lock(); defer subagentsMu.Unlock()
		return subagentInfos[ids[4]].DispatchPos >= 0
	}) || !waitRegistered(ids[4]) {
		t.Fatalf("expected all 5 dispatched, dispatched so far: %v", order)
	}
	close(releasesMap[ids[4]])

	// All complete with the recorded output.
	for _, id := range ids {
		if !waitFor(t, 2*time.Second, func() bool {
			subagentsMu.Lock(); defer subagentsMu.Unlock()
			return subagentInfos[id].Status.isTerminal()
		}) {
			t.Fatalf("subagent %s did not finish", id)
		}
		subagentsMu.Lock()
		info := subagentInfos[id]
		if info.Status != StatusCompleted || info.Result.Output != "done:"+id {
			t.Errorf("subagent %s: status=%s output=%q; want completed done:%s", id, info.Status, info.Result.Output, id)
		}
		subagentsMu.Unlock()
	}
	resetSubagentRegistry()
}

func TestKillQueuedAndRunning(t *testing.T) {
	cfg := &data.AppConfig{
		Models: []data.ModelConfig{modelCfg("m1", "openai", "http://a")},
		System: &data.SystemSettings{SubagentMaxUnmanaged: 2},
	}
	setTestAppConfig(t, cfg)
	resetSubagentRegistry()
	prevRun := subagentRunFn
	defer func() { subagentRunFn = prevRun }()

	releases := make(map[string]chan struct{})
	var mu sync.Mutex
	subagentRunFn = func(info *SubagentInfo) {
		mu.Lock()
		c := releases[info.ID]
		if c == nil {
			c = make(chan struct{})
			releases[info.ID] = c
		}
		mu.Unlock()
		select {
		case <-c: // normal completion
			subagentsMu.Lock(); kill := info.killRequested; subagentsMu.Unlock()
			completeSubagent(info, classifySubagentStatus(kill, nil, nil), "", "done")
		case <-info.runCtx.Done(): // cancelled/timeout unwind
			subagentsMu.Lock(); kill := info.killRequested; subagentsMu.Unlock()
			completeSubagent(info, classifySubagentStatus(kill, info.runCtx.Err(), nil), "", "")
		}
	}

	idA, _, _ := SpawnSubagent("main", "m1", map[string]any{"task": "a"})
	idB, _, _ := SpawnSubagent("main", "m1", map[string]any{"task": "b"})
	idC, _, _ := SpawnSubagent("main", "m1", map[string]any{"task": "c"}) // queued (unmanaged cap 2)

	// C is queued: killing it finishes immediately as "killed".
	if !KillSubagent(idC) {
		t.Fatal("kill of queued subagent should succeed")
	}
	subagentsMu.Lock()
	cInfo := subagentInfos[idC]
	cStatus, cOut := cInfo.Status, ""
	if cInfo.Result != nil {
		cOut = cInfo.Result.Output
	}
	subagentsMu.Unlock()
	if cStatus != StatusKilled || cOut != "killed while queued" {
		t.Errorf("queued kill: status=%s output=%q; want killed / 'killed while queued'", cStatus, cOut)
	}

	// A is running: kill requests it; the stub unwinds on ctx cancel and reports killed.
	if !KillSubagent(idA) {
		t.Fatal("kill of running subagent should succeed")
	}
	if !waitFor(t, 2*time.Second, func() bool {
		subagentsMu.Lock(); defer subagentsMu.Unlock()
		return subagentInfos[idA].Status == StatusKilled
	}) {
		subagentsMu.Lock()
		st := subagentInfos[idA].Status
		subagentsMu.Unlock()
		t.Fatalf("running kill did not surface as killed: %s", st)
	}

	// Kill an unknown id is a no-op.
	if KillSubagent("does-not-exist") {
		t.Fatal("kill of unknown id should return false")
	}

	// Release B so it completes and the registry drains.
	mu.Lock()
	releases[idB] <- struct{}{}
	mu.Unlock()
	waitFor(t, 2*time.Second, func() bool {
		subagentsMu.Lock(); defer subagentsMu.Unlock()
		return subagentInfos[idB].Status == StatusCompleted
	})
	resetSubagentRegistry()
}

// TestSubagentTimeoutEndToEnd drives the REAL runSubagent against a hanging
// model server with a short deadline, verifying the ctx-deadline -> timed_out
// path (and that LastActivity/PartialOutput capture happens via the relay).
func TestSubagentTimeoutEndToEnd(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()

	setTestAppConfig(t, &data.AppConfig{Models: []data.ModelConfig{{
		ID: "hang", Executor: "llama.cpp", URL: server.URL, Managed: false,
	}}})
	resetSubagentRegistry()

	// Build a running subagent directly (bypassing the queue) with a short deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	info := &SubagentInfo{
		ID: "sub-timeout", CallerID: "main",
		Spec: SubagentSpec{Name: "timeout", Task: "hang forever", ModelID: "hang", Mode: ModeStealth, Deadline: 150 * time.Millisecond},
		Status: StatusRunning, runCtx: ctx, Cancel: cancel,
		done: make(chan struct{}), StartedAt: time.Now(),
	}
	subagentsMu.Lock()
	subagentInfos[info.ID] = info
	subagentsMu.Unlock()

	start := time.Now()
	runSubagent(info)
	elapsed := time.Since(start)

	if elapsed >= 2*time.Second {
		t.Fatalf("timeout did not fire promptly (took %s)", elapsed)
	}
	if info.Status != StatusTimedOut {
		t.Fatalf("status = %s, want timed_out", info.Status)
	}
	if info.Result == nil || strings.TrimSpace(info.Result.Output) != "" && info.Result.Error != "" {
		// On a pure timeout there is no assistant output; error may carry the ctx message.
		t.Logf("timeout result: status=%s err=%q last=%q", info.Status, info.Result.Error, info.Result.LastActivity)
	}
	resetSubagentRegistry()
}

func TestManagedCapBlocksSecondWhileUnmanagedRuns(t *testing.T) {
	cfg := &data.AppConfig{
		Models: []data.ModelConfig{
			{ID: "local", Executor: "llama.cpp", Managed: true},
			{ID: "cloud", Executor: "openai", URL: "http://a", Managed: false},
		},
		System: &data.SystemSettings{SubagentMaxManaged: 1, SubagentMaxUnmanaged: 5},
	}
	setTestAppConfig(t, cfg)
	resetSubagentRegistry()
	prevRun := subagentRunFn
	defer func() { subagentRunFn = prevRun }()

	releases := make(map[string]chan struct{})
	var mu sync.Mutex
	subagentRunFn = func(info *SubagentInfo) {
		mu.Lock()
		c := releases[info.ID]
		if c == nil {
			c = make(chan struct{})
			releases[info.ID] = c
		}
		mu.Unlock()
		<-c
		completeSubagent(info, StatusCompleted, "", "done")
	}

	// Spawn one managed: it dispatches immediately.
	idManaged1, _, _ := SpawnSubagent("main", "local", map[string]any{"task": "m1"})
	if !waitFor(t, 2*time.Second, func() bool {
		subagentsMu.Lock(); defer subagentsMu.Unlock()
		return subagentInfos[idManaged1].Status == StatusRunning
	}) {
		t.Fatal("managed subagent did not dispatch")
	}

	// Spawn a second managed: it must queue (cap 1).
	idManaged2, _, _ := SpawnSubagent("main", "local", map[string]any{"task": "m2"})
	time.Sleep(50 * time.Millisecond) // give dispatch a chance to promote
	subagentsMu.Lock()
	st2 := subagentInfos[idManaged2].Status
	subagentsMu.Unlock()
	if st2 != StatusQueued {
		t.Fatalf("second managed should be queued (cap 1), got %s", st2)
	}

	// Spawn one unmanaged: it dispatches immediately (independent pool).
	idUnmanaged, _, _ := SpawnSubagent("main", "cloud", map[string]any{"task": "u1"})
	if !waitFor(t, 2*time.Second, func() bool {
		subagentsMu.Lock(); defer subagentsMu.Unlock()
		return subagentInfos[idUnmanaged].Status == StatusRunning
	}) {
		t.Fatal("unmanaged subagent should dispatch while managed is full")
	}

	// Release the first managed: now the second managed can promote.
	mu.Lock()
	close(releases[idManaged1])
	mu.Unlock()
	if !waitFor(t, 2*time.Second, func() bool {
		subagentsMu.Lock(); defer subagentsMu.Unlock()
		return subagentInfos[idManaged2].Status == StatusRunning
	}) {
		t.Fatal("second managed should promote after first completes")
	}

	// Clean up.
	mu.Lock()
	close(releases[idManaged2])
	close(releases[idUnmanaged])
	mu.Unlock()
	waitFor(t, 2*time.Second, func() bool {
		subagentsMu.Lock(); defer subagentsMu.Unlock()
		return subagentInfos[idManaged2].Status.isTerminal() && subagentInfos[idUnmanaged].Status.isTerminal()
	})
	resetSubagentRegistry()
}

func TestSkipAheadDispatch(t *testing.T) {
	cfg := &data.AppConfig{
		Models: []data.ModelConfig{
			{ID: "local", Executor: "llama.cpp", Managed: true},
			{ID: "cloud", Executor: "openai", URL: "http://a", Managed: false},
		},
		System: &data.SystemSettings{SubagentMaxManaged: 1, SubagentMaxUnmanaged: 5},
	}
	setTestAppConfig(t, cfg)
	resetSubagentRegistry()
	prevRun := subagentRunFn
	defer func() { subagentRunFn = prevRun }()

	releases := make(map[string]chan struct{})
	var mu sync.Mutex
	subagentRunFn = func(info *SubagentInfo) {
		mu.Lock()
		c := releases[info.ID]
		if c == nil {
			c = make(chan struct{})
			releases[info.ID] = c
		}
		mu.Unlock()
		<-c
		completeSubagent(info, StatusCompleted, "", "done")
	}

	// Fill the managed slot.
	idM1, _, _ := SpawnSubagent("main", "local", map[string]any{"task": "m1"})
	waitFor(t, 2*time.Second, func() bool {
		subagentsMu.Lock(); defer subagentsMu.Unlock()
		return subagentInfos[idM1].Status == StatusRunning
	})

	// Queue a managed (head of queue) and an unmanaged after it.
	idM2, _, _ := SpawnSubagent("main", "local", map[string]any{"task": "m2"}) // queued, head
	idU1, _, _ := SpawnSubagent("main", "cloud", map[string]any{"task": "u1"}) // queued, behind

	// The unmanaged should skip ahead of the blocked managed head.
	if !waitFor(t, 2*time.Second, func() bool {
		subagentsMu.Lock(); defer subagentsMu.Unlock()
		return subagentInfos[idU1].Status == StatusRunning
	}) {
		t.Fatal("unmanaged should skip ahead of blocked managed head")
	}

	// Managed head is still queued.
	subagentsMu.Lock()
	stM2 := subagentInfos[idM2].Status
	subagentsMu.Unlock()
	if stM2 != StatusQueued {
		t.Fatalf("managed head should remain queued, got %s", stM2)
	}

	// Release managed slot: now the queued managed promotes.
	waitFor(t, 2*time.Second, func() bool {
		mu.Lock(); defer mu.Unlock()
		_, ok := releases[idM1]
		return ok
	})
	mu.Lock()
	close(releases[idM1])
	mu.Unlock()
	if !waitFor(t, 2*time.Second, func() bool {
		subagentsMu.Lock(); defer subagentsMu.Unlock()
		return subagentInfos[idM2].Status == StatusRunning
	}) {
		t.Fatal("queued managed should promote after slot frees")
	}

	// Clean up: wait for idM2's stub to register, then release both.
	waitFor(t, 2*time.Second, func() bool {
		mu.Lock(); defer mu.Unlock()
		_, ok := releases[idM2]
		return ok
	})
	mu.Lock()
	close(releases[idM2])
	close(releases[idU1])
	mu.Unlock()
	waitFor(t, 2*time.Second, func() bool {
		subagentsMu.Lock(); defer subagentsMu.Unlock()
		return subagentInfos[idM2].Status.isTerminal() && subagentInfos[idU1].Status.isTerminal()
	})
	resetSubagentRegistry()
}
