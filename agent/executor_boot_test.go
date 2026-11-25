package agent

import (
	"clankerv2/data"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// resetExecutorPoolIncludingPending clears ALL live executor bookkeeping,
// including in-flight launches. Call BEFORE any stub is installed so a panic
// mid-test can't leave a claimed entry behind.
func resetExecutorPoolIncludingPending() {
	config.executorMu.Lock()
	config.ActiveExecutors = make(map[string]*ActiveExecutor)
	config.OrphanedExecutors = make(map[*ActiveExecutor]bool)
	config.StartingExecutors = make(map[string]*pendingLaunch)
	config.executorMu.Unlock()
}

// managedModelCfg builds a managed process-based model config for boot tests.
func managedModelCfg(id, command string, params ...string) data.ModelConfig {
	return data.ModelConfig{
		ID:        id,
		Executor:  "llama.cpp", // probe /health, like every real llama-server entry
		Managed:   true,
		Command:   command,
		Params:    params,
		PortFlag:  "--port",
		PortRange: "18000-18999",
	}
}

// TestConcurrentSameModelSingleFlight locks the duplicate-server fix: two runs
// spawned back to back for the same model (each in its own slot — exactly what
// happens when a parent launches two identical subagents) must share ONE boot.
// The stubbed launch sleeps before publishing, which deterministically covers
// the window where the second caller arrives while the first's launch is still
// un-published: the old check-then-start code let that second caller through to
// its own fork (two servers); the single-flight gate must make it wait and
// join instead.
func TestConcurrentSameModelSingleFlight(t *testing.T) {
	setTestAppConfig(t, &data.AppConfig{Models: []data.ModelConfig{managedModelCfg("m1", "sleep", "30")}})
	resetExecutorPoolIncludingPending()

	orig := launchManagedExecutorFn
	defer func() { launchManagedExecutorFn = orig }()

	var (
		mu    sync.Mutex
		calls int
	)

	// Fake boot: slow start (the real one's multi-minute model load), then
	// publish a shared instance under "chat" — as the real winner publishes its
	// own — and report success.
	launchManagedExecutorFn = func(ctx context.Context, modelID, slot, owner string, persistent bool, respChan chan<- any) (string, map[string]string, string, error) {
		mu.Lock()
		calls++
		mu.Unlock()

		time.Sleep(500 * time.Millisecond) // widen the claim-to-publish window

		config.executorMu.Lock()
		active, exists := config.ActiveExecutors["chat"]
		if !exists {
			active = &ActiveExecutor{
				ModelID:   modelID,
				Port:      18123,
				StartTime: time.Now(),
				RefCount:  1,
				Owners:    map[string]bool{},
				Aliases:   map[string]bool{"chat": true},
			}
			config.ActiveExecutors["chat"] = active
		} else {
			active.RefCount++
		}
		active.Owners[owner] = true
		config.executorMu.Unlock()

		return "http://localhost:18123", nil, "chat", nil
	}

	rtA := &AgentRuntime{ID: "sub-A", Role: RoleSubagent, ModelID: "m1", Slot: "sub-A"}
	rtB := &AgentRuntime{ID: "sub-B", Role: RoleSubagent, ModelID: "m1", Slot: "sub-B"}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); _, _, errs[0] = EnsureExecutorForRun(context.Background(), rtA, make(chan any, 64)) }()
	go func() { defer wg.Done(); _, _, errs[1] = EnsureExecutorForRun(context.Background(), rtB, make(chan any, 64)) }()
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}

	mu.Lock()
	n := calls
	mu.Unlock()
	if n != 1 {
		t.Fatalf("launch invoked %d times for two same-model runs; want exactly 1 (single-flight)", n)
	}

	config.executorMu.RLock()
	var instances int
	for k, v := range config.ActiveExecutors {
		if v.ModelID == "m1" {
			instances++
			t.Logf("slot %q -> owners=%v refcount=%d", k, v.Owners, v.RefCount)
		}
	}
	config.executorMu.RUnlock()
	if instances != 1 {
		t.Fatalf("two same-model runs left %d live executors; want 1 shared instance", instances)
	}

	if rtA.ExecutorHandle == nil || rtB.ExecutorHandle == nil || rtA.ExecutorHandle.Slot != rtB.ExecutorHandle.Slot {
		t.Fatalf("both runs must join the same slot: A=%+v B=%+v", rtA.ExecutorHandle, rtB.ExecutorHandle)
	}

	// Both runs hold ownership of the ONE shared instance.
	if rc := refCountOf("sub-A"); rc != 2 {
		t.Fatalf("shared executor RefCount = %d after two joins, want 2", rc)
	}

	ReleaseRunExecutors(rtA) // B still using it: must stay alive
	if _, ok := findOwner("sub-B"); !ok {
		t.Fatal("shared executor released while another run still owned it")
	}
	ReleaseRunExecutors(rtB)
	if left := poolSize(); left != 0 {
		t.Fatalf("executor not reaped at zero owners: %d entry(ies) remain", left)
	}
}

// TestExecutorBootDeathDetected locks the "process died but nobody noticed"
// fix: a managed server that exits immediately must fail its boot promptly
// (not after the 10-minute probe timeout), the error must carry the exit code
// AND what the process said before dying, and no pool state may leak.
func TestExecutorBootDeathDetected(t *testing.T) {
	setTestAppConfig(t, &data.AppConfig{Models: []data.ModelConfig{
		managedModelCfg("fail-model", "sh", "-c", `echo "boom: bad flag --port"; echo "fatal: cannot load model" 1>&2; exit 7`),
	}})
	resetExecutorPoolIncludingPending()

	rt := &AgentRuntime{ID: "boot-run", ModelID: "fail-model"}
	start := time.Now()
	_, _, err := EnsureExecutorForRun(context.Background(), rt, make(chan any, 64))
	took := time.Since(start)

	if err == nil {
		t.Fatal("expected the boot to fail, got success")
	}
	if took > 30*time.Second {
		t.Fatalf("death detection took %s; expected failure within a few seconds of the exit", took)
	}
	msg := err.Error()
	if !strings.Contains(msg, "died during startup") || !strings.Contains(msg, "exit code 7") {
		t.Fatalf("error must report process death and its exit code, got: %q", msg)
	}
	for _, want := range []string{"boom: bad flag --port", "[stderr] fatal: cannot load model"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error must include the captured startup output (%q), got: %q", want, msg)
		}
	}

	config.executorMu.Lock()
	activeLeft := len(config.ActiveExecutors)
	startingLeft := len(config.StartingExecutors)
	orphansLeft := len(config.OrphanedExecutors)
	config.executorMu.Unlock()
	if activeLeft+startingLeft+orphansLeft != 0 {
		t.Fatalf("failed boot leaked pool state: active=%d starting=%d orphans=%d", activeLeft, startingLeft, orphansLeft)
	}
}

// TestExecutorBootCancelTearsDown locks the ctx-wiring contract: cancelling a
// run while its executor is still booting must (a) make EnsureExecutorForRun
// return promptly with context.Canceled, (b) KILL the freshly started process,
// and (c) leave every pool entry clean.
func TestExecutorBootCancelTearsDown(t *testing.T) {
	setTestAppConfig(t, &data.AppConfig{Models: []data.ModelConfig{
		// `sh -c` swallows the appended --port flag harmlessly (unlike sleep,
		// which rejects it — see TestExecutorBootDeathDetected), so this boot
		// survives long enough to be cancelled mid-wait.
		managedModelCfg("slow-model", "sh", "-c", "sleep 30"),
	}})
	resetExecutorPoolIncludingPending()

	ctx, cancel := context.WithCancel(context.Background())
	rt := &AgentRuntime{ID: "cancel-run", ModelID: "slow-model"} // Slot "" -> "chat"

	done := make(chan error, 1)
	go func() {
		_, _, err := EnsureExecutorForRun(ctx, rt, make(chan any, 64))
		done <- err
	}()

	// Wait until the boot has published its process, then escape.
	var proc *os.Process
	deadline := time.Now().Add(5 * time.Second)
	for {
		config.executorMu.RLock()
		if e := config.ActiveExecutors["chat"]; e != nil && e.Owners["cancel-run"] && e.Process != nil {
			proc = e.Process
			config.executorMu.RUnlock()
			break
		}
		config.executorMu.RUnlock()
		if time.Now().After(deadline) {
			t.Fatal("boot never published its process")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EnsureExecutorForRun did not return promptly after cancel")
	}

	// The process must actually be dead, not just unmapped. SIGKILL delivery is
	// async, so poll /proc until the pid is gone (dead AND reaped by our Wait).
	requireProcessDead(t, proc.Pid)

	config.executorMu.Lock()
	activeLeft := len(config.ActiveExecutors)
	startingLeft := len(config.StartingExecutors)
	orphansLeft := len(config.OrphanedExecutors)
	config.executorMu.Unlock()
	if activeLeft+startingLeft+orphansLeft != 0 {
		t.Fatalf("cancelled boot leaked pool state: active=%d starting=%d orphans=%d", activeLeft, startingLeft, orphansLeft)
	}

	// The wrapper's CHILDREN must be dead too: this is the whole point of the
	// process-group teardown — a killed sh -c wrapper must not leave the inner
	// sleep (in production: the model server holding GPU RAM) behind.
	if live := findLiveChildren(proc.Pid); len(live) > 0 {
		t.Fatalf("wrapper's child processes survived cancellation: %v", live)
	}
}

// requireProcessDead polls /proc until pid is gone (dead and/or a zombie),
// failing after the deadline. SIGKILL delivery + reap is async, so a teardown
// that merely ISSUED the kill must be verified this way rather than by a second
// Kill's ESRCH (which races the kernel).
func requireProcessDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat("/proc/" + strconv.Itoa(pid)); err != nil {
			return // gone: dead and reaped
		}
		if state, _ := readProcState(pid); state == "Z" {
			return // zombie: dead, just awaiting reap by its parent (our Wait)
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d still alive after teardown", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// readProcState returns the process state byte from /proc/<pid>/stat ("Z" =
// zombie, "R"/"S" = live). "" when unreadable (vanished).
func readProcState(pid int) (string, error) {
	status, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", err
	}
	// stat: pid (comm) state ppid ... — comm may hold spaces/parens; anchor on LAST ')'.
	idx := strings.LastIndex(string(status), ")")
	if idx < 0 {
		return "", nil
	}
	fields := strings.Fields(string(status[idx+1:]))
	if len(fields) < 1 {
		return "", nil
	}
	return fields[0], nil
}

// findLiveChildren returns the pids of any still-ALIVE direct children of pid,
// walking /proc (Linux). Re-parented orphans (ppid 1) are included; zombies
// are not — a reaped zombie is as dead as it gets without consuming resources.
func findLiveChildren(pid int) []int {
	var live []int
	dir, err := os.Open("/proc")
	if err != nil {
		return live
	}
	defer dir.Close()
	entries, _ := dir.Readdirnames(-1)
	for _, name := range entries {
		if !isDigit(name) { continue }
		status, err := os.ReadFile("/proc/" + name + "/stat")
		if err != nil { continue } // process vanished mid-walk
		// stat: pid (comm) state ppid ... — comm may contain spaces/parens,
		// so anchor on the LAST ')'.
		idx := strings.LastIndex(string(status), ")")
		if idx < 0 { continue }
		fields := strings.Fields(string(status[idx+1:]))
		if len(fields) < 2 { continue }
		state, ppid := fields[0], fields[1]
		if state == "Z" || ppid != strconv.Itoa(pid) {
			continue
		}
		live = append(live, atoiOrZero(name))
	}
	return live
}

func isDigit(s string) bool {
	for _, r := range s { if r < '0' || r > '9' { return false } }
	return len(s) > 0
}

func atoiOrZero(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
