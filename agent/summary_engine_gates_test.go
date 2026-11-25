package agent

import (
	"clankerv2/data"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestSessionRunTracking covers the per-session busy counter the summary
// engine relies on: nesting, draining, over-decrement clamping, and dummy
// (stealth) session IDs being ignored entirely.
func TestSessionRunTracking(t *testing.T) {
	id := int64(4242) // arbitrary; no real DB row behind it in tests
	defer func() {
		for IsSessionBusy(id) {
			EndSessionRun(id)
		}
	}()

	if IsSessionBusy(id) {
		t.Fatalf("session should start idle")
	}

	BeginSessionRun(id)
	if !IsSessionBusy(id) {
		t.Fatalf("single run should read as busy")
	}
	EndSessionRun(id)
	if IsSessionBusy(id) {
		t.Fatalf("should be idle after the run ends")
	}

	// Concurrent runs nest and fully drain.
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			BeginSessionRun(id)
			time.Sleep(10 * time.Millisecond)
			EndSessionRun(id)
		}()
	}
	wg.Wait()
	if IsSessionBusy(id) {
		t.Fatalf("concurrent runs should fully drain")
	}

	// Over-decrement clamps at zero instead of sticking as busy forever.
	BeginSessionRun(id)
	EndSessionRun(id)
	EndSessionRun(id)
	if IsSessionBusy(id) {
		t.Fatalf("over-decrement must not leave the session busy")
	}

	// Stealth/dummy sessions (ID <= 0) have no DB row and are never tracked.
	BeginSessionRun(0)
	BeginSessionRun(-1)
	for _, probe := range []int64{0, -1, -999} {
		if IsSessionBusy(probe) {
			t.Fatalf("dummy session ID %d must never read as busy", probe)
		}
	}
}

// TestPromptWithContextTracksRun verifies the wiring end to end: an in-flight
// run (blocked on a hanging model response, standing in for long thinking or
// a pending system-ask form) is observable as busy, and cancelling it clears
// the counter exactly once.
// TestPromptWithContextTracksRun verifies the wiring end to end: an in-flight
// run (blocked on a hanging model response, standing in for long thinking or
// a pending system-ask form) is observable as busy, and cancelling it clears
// the counter exactly once. The full Stream path persists messages, so this
// test points the package DB at a throwaway file.
func TestPromptWithContextTracksRun(t *testing.T) {
	tmpDB := filepath.Join(t.TempDir(), "test.db")
	if _, err := data.OpenTestDB(tmpDB); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer data.CloseTestDB()

	// A real DB row so InferSession's update path works (a fake ID would fail
	// UpdateSession and nil out the session mid-run).
	session, err := data.NewSession("busy-tracking test")
	if err != nil {
		t.Fatalf("failed to create test session: %v", err)
	}
	id := session.ID
	prevRT := GetMainRuntimeInternal()
	prevConfig := config.appConfig

	// Handler that blocks until the request's context is done: keeps
	// StreamSSE stuck for the run's lifetime (mirroring long thinking or a
	// pending system-ask form). The timer cap is a teardown safety net —
	// httptest.Server.Close waits for handlers to finish, and an aborted
	// client request does not always cancel the server-side context, which
	// would otherwise hang Close forever.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()

	config.appConfig = &data.AppConfig{Models: []data.ModelConfig{{
		ID: "hang-model", Managed: false, Executor: "llama.cpp", URL: server.URL,
	}}}
	rt := NewAgentRuntime()
	rt.Session = session
	SetMainRuntime(rt)
	defer func() {
		SetMainRuntime(prevRT)
		config.appConfig = prevConfig
		ReleaseRunExecutors(MainRuntime()) // release any executor this run joined/started
	}()

	ch := make(chan any, 4)
	cancel := PromptWithContext(nil, ch, "hello", "hang-model")

	busy := false
	for i := 0; i < 200 && !busy; i++ {
		if IsSessionBusy(id) {
			busy = true
		} else {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !busy {
		t.Fatalf("in-flight run was never observed as busy")
	}

	cancel()

	for i := 0; i < 200 && IsSessionBusy(id); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if IsSessionBusy(id) {
		t.Fatalf("run still busy after cancel: leaked run counter would block the summarizer forever")
	}
}

// TestPromptWithContextDiscardsQueuedOnCancel covers the interjection queue's
// cancel path: messages queued while a run is in flight are dropped when that
// run is cancelled (they must not leak into the NEXT prompt), mirroring the
// documented intent of DiscardQueuedMessages.
func TestPromptWithContextDiscardsQueuedOnCancel(t *testing.T) {
	tmpDB := filepath.Join(t.TempDir(), "test.db")
	if _, err := data.OpenTestDB(tmpDB); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer data.CloseTestDB()

	session, err := data.NewSession("queue-discard test")
	if err != nil {
		t.Fatalf("failed to create test session: %v", err)
	}
	prevRT := GetMainRuntimeInternal()
	prevConfig := config.appConfig

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()

	config.appConfig = &data.AppConfig{Models: []data.ModelConfig{{
		ID: "hang-model", Managed: false, Executor: "llama.cpp", URL: server.URL,
	}}}
	rt := NewAgentRuntime()
	rt.Session = session
	SetMainRuntime(rt)
	defer func() {
		SetMainRuntime(prevRT)
		config.appConfig = prevConfig
		ReleaseRunExecutors(MainRuntime())
	}()

	ch := make(chan any, 4)
	cancel := PromptWithContext(nil, ch, "hello", "hang-model")

	// Wait until the run is in flight (model call blocked on the hanging
	// response), then queue two interjections like the UIs do.
	for i := 0; i < 200 && !IsSessionBusy(session.ID); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	EnqueueMainUserMessage("first interjection")
	EnqueueMainUserMessage("second interjection")

	deadline := time.Now().Add(2 * time.Second)
	for MainRuntimePendingUserMessages() != 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := MainRuntimePendingUserMessages(); got != 2 {
		t.Fatalf("queued messages not visible before cancel: got %d, want 2", got)
	}

	cancel()

	// The run's deferred discard clears the queue once its goroutine ends.
	deadline = time.Now().Add(2 * time.Second)
	for MainRuntimePendingUserMessages() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := MainRuntimePendingUserMessages(); got != 0 {
		t.Fatalf("queue not discarded after cancel: %d message(s) would leak into the next prompt", got)
	}
}

// TestReleaseExecutor covers the post-sweep teardown: unmanaged models just
// lose their slot, managed processes are killed, and missing slots are a
// no-op. The map is swapped in so other tests keep a pristine global.
func TestReleaseExecutor(t *testing.T) {
	config.executorMu.Lock()
	saved := config.ActiveExecutors
	config.ActiveExecutors = make(map[string]*ActiveExecutor)
	config.executorMu.Unlock()
	defer func() {
		config.executorMu.Lock()
		config.ActiveExecutors = saved
		config.executorMu.Unlock()
	}()

	// Unmanaged (remote) model: nothing to stop, the slot disappears.
	config.ActiveExecutors["summarizer"] = &ActiveExecutor{ModelID: "remote-sum", StartTime: time.Now()}
	releaseExecutor("summarizer")
	if _, ok := config.ActiveExecutors["summarizer"]; ok {
		t.Fatalf("releaseExecutor should remove the slot")
	}

	// Missing slot is a no-op.
	releaseExecutor("summarizer")

	// Managed process: killed AND removed (the kill is what frees GPU RAM).
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start test process: %v", err)
	}
	config.ActiveExecutors["summarizer"] = &ActiveExecutor{ModelID: "proc-sum", Process: cmd.Process, StartTime: time.Now()}

	releaseExecutor("summarizer")

	if _, ok := config.ActiveExecutors["summarizer"]; ok {
		t.Fatalf("releaseExecutor should remove the process slot")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		// Process was killed: Wait returns (with a kill error).
	case <-time.After(3 * time.Second):
		t.Fatalf("managed process was not killed by releaseExecutor")
		cmd.Process.Kill()
	}
}
