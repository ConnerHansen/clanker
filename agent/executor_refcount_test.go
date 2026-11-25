package agent

import (
	"clankerv2/data"
	"context"
	"os/exec"
	"testing"
	"time"
)

// resetExecutorPool clears all live executor bookkeeping between tests.
func resetExecutorPool() {
	config.executorMu.Lock()
	config.ActiveExecutors = make(map[string]*ActiveExecutor)
	config.OrphanedExecutors = make(map[*ActiveExecutor]bool)
	config.executorMu.Unlock()
}

// twoModelConfig is the shared fixture: two unmanaged (remote) models so no
// process/container lifecycle is involved — the refcount bookkeeping is what
// these tests exercise.
func twoModelConfig() *data.AppConfig {
	return &data.AppConfig{Models: []data.ModelConfig{
		modelCfg("m1", "openai", "http://a"),
		modelCfg("m2", "openai", "http://b"),
	}}
}

// findInstanceByOwner returns any live executor naming the given owner (active
// slot map or orphan pool), or nil.
func findInstanceByOwner(owner string) *ActiveExecutor {
	config.executorMu.Lock()
	defer config.executorMu.Unlock()
	for _, v := range config.ActiveExecutors {
		if v.Owners[owner] {
			return v
		}
	}
	for v := range config.OrphanedExecutors {
		if v.Owners[owner] {
			return v
		}
	}
	return nil
}

// slotModel reports the model ID currently mapped to a slot ("" when empty).
func slotModel(slot string) string {
	config.executorMu.Lock()
	defer config.executorMu.Unlock()
	if v, ok := config.ActiveExecutors[slot]; ok {
		return v.ModelID
	}
	return ""
}

// findOwner reports whether any live executor (active or orphaned) names the
// given owner and returns that executor (nil when absent).
func findOwner(owner string) (*ActiveExecutor, bool) {
	config.executorMu.Lock()
	defer config.executorMu.Unlock()
	for _, v := range config.ActiveExecutors {
		if v.Owners[owner] {
			return v, true
		}
	}
	for v := range config.OrphanedExecutors {
		if v.Owners[owner] {
			return v, true
		}
	}
	return nil, false
}

// refCountOf returns the RefCount of the executor owning the given owner (0 when
// no such executor is live).
func refCountOf(owner string) int {
	config.executorMu.Lock()
	defer config.executorMu.Unlock()
	for _, v := range config.ActiveExecutors {
		if v.Owners[owner] {
			return v.RefCount
		}
	}
	for v := range config.OrphanedExecutors {
		if v.Owners[owner] {
			return v.RefCount
		}
	}
	return 0
}

// poolSize counts live executor entries in both the slot map and the orphan
// pool.
func poolSize() int {
	config.executorMu.Lock()
	defer config.executorMu.Unlock()
	return len(config.ActiveExecutors) + len(config.OrphanedExecutors)
}

// TestExecutorRefcountSharedSlot locks the basic sharing contract: two runs of
// the same model on the same slot share ONE executor; releasing one keeps it
// alive for the other; the last release tears the entry down.
func TestExecutorRefcountSharedSlot(t *testing.T) {
	setTestAppConfig(t, twoModelConfig())
	resetExecutorPool()

	rtA := &AgentRuntime{ID: "run-a", ModelID: "m1", Slot: "chat"}
	if _, _, err := EnsureExecutorForRun(context.Background(), rtA, make(chan any, 64)); err != nil {
		t.Fatalf("ensure A: %v", err)
	}

	rtB := &AgentRuntime{ID: "run-b", ModelID: "m1", Slot: "chat"}
	if _, _, err := EnsureExecutorForRun(context.Background(), rtB, make(chan any, 64)); err != nil {
		t.Fatalf("ensure B: %v", err)
	}

	if n := refCountOf("run-a"); n != 2 {
		t.Fatalf("RefCount = %d after two joins, want 2", n)
	}

	ReleaseRunExecutors(rtB) // A still using it: must stay alive.
	if _, ok := findOwner("run-a"); !ok {
		t.Fatal("executor released while another run still owned it")
	}
	if n := refCountOf("run-a"); n != 1 {
		t.Fatalf("RefCount = %d after one release, want 1", n)
	}

	ReleaseRunExecutors(rtA) // last owner out: slot must be empty.
	if left := poolSize(); left != 0 {
		t.Fatalf("executor not reaped at zero owners: %d entry(ies) remain", left)
	}
}

// TestExecutorCrossSlotJoinSharesInstance locks the "model: null inherits the
// caller's model" contract: a run whose model is already live ANYWHERE joins
// that instance instead of starting a second server, and its release handle
// names the slot it actually joined.
func TestExecutorCrossSlotJoinSharesInstance(t *testing.T) {
	setTestAppConfig(t, twoModelConfig())
	resetExecutorPool()

	rtA := &AgentRuntime{ID: "run-a", ModelID: "m1", Slot: "chat"}
	if _, _, err := EnsureExecutorForRun(context.Background(), rtA, make(chan any, 64)); err != nil {
		t.Fatalf("ensure A: %v", err)
	}

	rtB := &AgentRuntime{ID: "run-b", ModelID: "m1", Slot: "sub-1"} // asks for its own slot...
	if _, _, err := EnsureExecutorForRun(context.Background(), rtB, make(chan any, 64)); err != nil {
		t.Fatalf("ensure B: %v", err)
	}
	if rtB.ExecutorHandle == nil || rtB.ExecutorHandle.Slot != "chat" {
		t.Fatalf("cross-slot join must record the joined slot; got %+v", rtB.ExecutorHandle)
	}

	config.executorMu.Lock()
	var count int
	for _, v := range config.ActiveExecutors {
		if v.ModelID == "m1" {
			count++
		}
	}
	config.executorMu.Unlock()
	if count != 1 {
		t.Fatalf("cross-slot join must NOT start a second server: %d m1 executor(s) live", count)
	}

	ReleaseRunExecutors(rtA) // B still owns it.
	if _, ok := findOwner("run-b"); !ok {
		t.Fatal("shared executor died while the other owner was still using it")
	}
	ReleaseRunExecutors(rtB)
	if left := poolSize(); left != 0 {
		t.Fatalf("reap at zero owners failed: %d entry(ies) remain", left)
	}
}

// TestExecutorIsolatedRunOwnsPrivateSlot locks the summarizer contract: an
// isolated run NEVER joins another slot's executor even for the same model,
// and its teardown cannot touch the shared one.
func TestExecutorIsolatedRunOwnsPrivateSlot(t *testing.T) {
	setTestAppConfig(t, twoModelConfig())
	resetExecutorPool()

	rtA := &AgentRuntime{ID: "run-a", ModelID: "m1", Slot: "chat"}
	if _, _, err := EnsureExecutorForRun(context.Background(), rtA, make(chan any, 64)); err != nil {
		t.Fatalf("ensure A: %v", err)
	}

	rtS := &AgentRuntime{ID: "sweep", ModelID: "m1", Slot: "summarizer", Isolation: true}
	if _, _, err := EnsureExecutorForRun(context.Background(), rtS, make(chan any, 64)); err != nil {
		t.Fatalf("ensure sweep: %v", err)
	}
	if rtS.ExecutorHandle == nil || rtS.ExecutorHandle.Slot != "summarizer" {
		t.Fatalf("isolated run must keep its own slot; got %+v", rtS.ExecutorHandle)
	}

	config.executorMu.Lock()
	var shared, isolated *ActiveExecutor
	for _, v := range config.ActiveExecutors {
		if v.Owners["run-a"] {
			shared = v
		}
		if v.Owners["sweep"] {
			isolated = v
		}
	}
	config.executorMu.Unlock()
	if shared == isolated || shared == nil || isolated == nil {
		t.Fatalf("isolated run must get a DISTINCT instance: shared=%v isolated=%v", shared, isolated)
	}

	ReleaseRunExecutors(rtS) // tear the private one down...
	if _, ok := findOwner("sweep"); ok {
		t.Fatal("isolated executor not reaped")
	}
	if slotModel("chat") != "m1" {
		t.Fatal("tearing down the isolated executor touched the shared chat slot")
	}

	ReleaseRunExecutors(rtA)
	if left := poolSize(); left != 0 {
		t.Fatalf("reap at zero owners failed: %d entry(ies) remain", left)
	}
}

// TestExecutorModelSwitchOrphansLiveExecutor locks the mid-run switch rule:
// evicting a slot's occupant while a run still streams from it must NOT kill
// or lose the old executor — it is orphaned from the slot, keeps its owner(s),
// and is reaped only when its last owner releases. The new occupant takes the
// slot cleanly.
func TestExecutorModelSwitchOrphansLiveExecutor(t *testing.T) {
	setTestAppConfig(t, twoModelConfig())
	resetExecutorPool()

	rtA := &AgentRuntime{ID: "run-a", ModelID: "m1", Slot: "chat"} // in-flight run on m1
	if _, _, err := EnsureExecutorForRun(context.Background(), rtA, make(chan any, 64)); err != nil {
		t.Fatalf("ensure A: %v", err)
	}

	rtB := &AgentRuntime{ID: "run-b", ModelID: "m2", Slot: "chat"} // user switches models
	if _, _, err := EnsureExecutorForRun(context.Background(), rtB, make(chan any, 64)); err != nil {
		t.Fatalf("ensure B (switch): %v", err)
	}

	// The slot now maps to m2...
	if got := slotModel("chat"); got != "m2" {
		t.Fatalf("after switch chat=%q, want m2", got)
	}
	// ...and the old m1 instance is orphaned but fully intact: still owned by
	// run-a (NOT reaped), and reachable for its in-flight stream.
	old := findInstanceByOwner("run-a")
	if old == nil {
		t.Fatal("orphaned executor lost its owner record; its release could never reap it")
	}
	config.executorMu.Lock()
	mapsToOld := false
	for _, v := range config.ActiveExecutors {
		if v == old {
			mapsToOld = true
		}
	}
	oldOwners := len(old.Owners)
	config.executorMu.Unlock()
	if mapsToOld {
		t.Fatal("orphaned executor still referenced from the slot map after eviction")
	}
	if oldOwners != 1 {
		t.Fatalf("orphan owner count = %d, want 1", oldOwners)
	}

	// run-a finishes: the orphan is reaped; the live m2 occupant survives.
	ReleaseRunExecutors(rtA)
	if findInstanceByOwner("run-a") != nil {
		t.Fatal("orphan not reaped after its last owner released")
	}
	config.executorMu.Lock()
	_, stillOrphaned := config.OrphanedExecutors[old]
	config.executorMu.Unlock()
	if stillOrphaned {
		t.Fatal("reap did not remove the instance from the orphan pool")
	}
	if got := slotModel("chat"); got != "m2" {
		t.Fatalf("reaping the orphan disturbed the live slot: chat=%q", got)
	}

	ReleaseRunExecutors(rtB)
	if left := poolSize(); left != 0 {
		t.Fatalf("pool not empty at end: %d entry(ies)", left)
	}
}

// TestChatExecutorStaysWarmAfterTurn locks the chat-persistence contract:
// finishing an interactive turn releases the run's ownership but must NOT stop
// the chat model's server (a managed load can take minutes; every prompt should
// hit a warm one). The released instance stays mapped at zero owners, the next
// prompt joins it in one step, and a subagent inheriting the same model shares
// the SAME process. Only a different model evicting the slot (or app shutdown)
// tears it down.
func TestChatExecutorStaysWarmAfterTurn(t *testing.T) {
	setTestAppConfig(t, twoModelConfig())
	resetExecutorPool()

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start test process: %v", err)
	}
	defer cmd.Process.Kill()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Prompt 1 provisions the chat model's managed server.
	rt1 := &AgentRuntime{ID: "main", Role: RoleMain, ModelID: "m1"} // Slot defaults to "chat"
	if _, _, err := EnsureExecutorForRun(context.Background(), rt1, make(chan any, 64)); err != nil {
		t.Fatalf("ensure chat run 1: %v", err)
	}
	config.executorMu.Lock()
	first := config.ActiveExecutors["chat"]
	first.Process = cmd.Process // stand-in for the managed server's process
	persistent := first.Persistent
	config.executorMu.Unlock()
	if !persistent {
		t.Fatal("chat-slot executor must be marked persistent")
	}

	// Turn 1 completes: ownership is released...
	ReleaseRunExecutors(rt1)

	// ...and the server must survive, still mapped for the next prompt.
	if got := slotModel("chat"); got != "m1" {
		t.Fatalf("warm chat executor lost its slot mapping after release: chat=%q", got)
	}
	select {
	case <-done:
		t.Fatal("chat server was killed when the turn ended — it must stay warm")
	default:
	}

	// A subagent inheriting the same model joins the SAME instance.
	rtSub := &AgentRuntime{ID: "sub-1", Role: RoleSubagent, ModelID: "m1", Slot: "sub-1"}
	if _, _, err := EnsureExecutorForRun(context.Background(), rtSub, make(chan any, 64)); err != nil {
		t.Fatalf("ensure subagent join: %v", err)
	}
	config.executorMu.Lock()
	var count int
	for _, v := range config.ActiveExecutors {
		if v.ModelID == "m1" {
			count++
		}
	}
	subJoinedChat := rtSub.ExecutorHandle != nil && rtSub.ExecutorHandle.Slot == "chat"
	config.executorMu.Unlock()
	if !subJoinedChat || count != 1 {
		t.Fatalf("subagent must join the warm chat instance (joined=%v, m1 live=%d)", subJoinedChat, count)
	}

	// Prompt 2 joins the same still-running process.
	rt2 := &AgentRuntime{ID: "main", Role: RoleMain, ModelID: "m1"}
	if _, _, err := EnsureExecutorForRun(context.Background(), rt2, make(chan any, 64)); err != nil {
		t.Fatalf("ensure chat run 2: %v", err)
	}
	select {
	case <-done:
		t.Fatal("chat server was killed between prompts")
	default:
	}

	// Everyone out; the warm instance remains (reaped at shutdown, not here).
	ReleaseRunExecutors(rtSub)
	ReleaseRunExecutors(rt2)
	if got := slotModel("chat"); got != "m1" {
		t.Fatalf("warm chat executor must remain mapped: chat=%q", got)
	}

	// Switching to a different model evicts the warm server for real.
	rtM2 := &AgentRuntime{ID: "main", Role: RoleMain, ModelID: "m2"}
	if _, _, err := EnsureExecutorForRun(context.Background(), rtM2, make(chan any, 64)); err != nil {
		t.Fatalf("ensure model switch: %v", err)
	}
	if got := slotModel("chat"); got != "m2" {
		t.Fatalf("after switch chat=%q, want m2", got)
	}
	select {
	case <-done:
		// expected: the old warm server is stopped on eviction
	case <-time.After(3 * time.Second):
		t.Fatal("model switch did not stop the evicted warm executor")
	}

	ReleaseRunExecutors(rtM2)
	// m2 is now the chat model: its server stays warm too (the chat slot never
	// offloads between turns).
	if got := slotModel("chat"); got != "m2" {
		t.Fatalf("after m2's run released, the new chat model should stay warm: %q", got)
	}
}

// TestChatWarmServerEvictionWithLiveOwner locks the mid-run model switch rule
// against a WARM (zero-owner) chat server: switching models while nothing is
// streaming must stop the evicted executor immediately — no orphaned zombie —
// and the new model owns the slot cleanly.
func TestChatWarmServerEvictionByModelSwitch(t *testing.T) {
	setTestAppConfig(t, twoModelConfig())
	resetExecutorPool()

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start test process: %v", err)
	}
	defer cmd.Process.Kill()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	rtM1 := &AgentRuntime{ID: "main", Role: RoleMain, ModelID: "m1"}
	if _, _, err := EnsureExecutorForRun(context.Background(), rtM1, make(chan any, 64)); err != nil {
		t.Fatalf("ensure m1: %v", err)
	}
	config.executorMu.Lock()
	config.ActiveExecutors["chat"].Process = cmd.Process
	config.executorMu.Unlock()
	ReleaseRunExecutors(rtM1) // warm, zero owners

	if findInstanceByOwner("main") != nil {
		t.Fatal("warm chat executor should not name any owner after release")
	}

	rtM2 := &AgentRuntime{ID: "main", Role: RoleMain, ModelID: "m2"} // user switches models
	if _, _, err := EnsureExecutorForRun(context.Background(), rtM2, make(chan any, 64)); err != nil {
		t.Fatalf("ensure switch to m2: %v", err)
	}

	select {
	case <-done:
		// expected: the zero-owner warm server is stopped on eviction
	case <-time.After(3 * time.Second):
		t.Fatal("model switch left the old warm executor running")
	}
	config.executorMu.Lock()
	for e := range config.OrphanedExecutors {
		if e.Process == cmd.Process {
			config.executorMu.Unlock()
			t.Fatal("evicted zero-owner executor was orphaned instead of stopped")
		}
	}
	config.executorMu.Unlock()

	ReleaseRunExecutors(rtM2)
	// The new chat model's server stays warm; the old one is gone from every
	// map (stopped on eviction, not orphaned).
	if got := slotModel("chat"); got != "m2" {
		t.Fatalf("after release the warm m2 executor should remain: %q", got)
	}
}
