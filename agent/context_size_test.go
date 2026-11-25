package agent

import (
	"clankerv2/data"
	"testing"
)

// setTestMainRuntime installs a fresh main runtime with a known context size,
// restoring the previous one on cleanup so tests do not leak state into each
// other.
func setTestMainRuntime(t *testing.T, rt *AgentRuntime) {
	t.Helper()
	prev := GetMainRuntimeInternal()
	SetMainRuntime(rt)
	t.Cleanup(func() { SetMainRuntime(prev) })
}

// withTestContextSize sets the main runtime's context size and optional
// session for the duration of a test.
func withTestContextSize(t *testing.T, size int, session *data.Session) {
	t.Helper()
	prev := GetMainRuntimeInternal()
	rt := NewAgentRuntime()
	rt.ContextSize = size
	rt.Session = session
	SetMainRuntime(rt)
	t.Cleanup(func() { SetMainRuntime(prev) })
}

func TestGetContextSizeDefaults(t *testing.T) {
	withTestContextSize(t, data.DefaultContextSize, nil)
	if got := GetContextSize(); got != data.DefaultContextSize {
		t.Fatalf("GetContextSize() = %d, want default %d", got, data.DefaultContextSize)
	}
}

func TestGetContextSizeNormalizesUnset(t *testing.T) {
	// Unset (zero) state must resolve to the default, never 0 or 16k.
	withTestContextSize(t, 0, nil)
	if got := GetContextSize(); got != data.DefaultContextSize {
		t.Fatalf("GetContextSize() with unset state = %d, want default %d", got, data.DefaultContextSize)
	}
}

func TestSetSessionContextSizeInMemoryOnly(t *testing.T) {
	// No real session (stealth/not-yet-created): the value applies to payloads
	// immediately but cannot be persisted.
	withTestContextSize(t, data.DefaultContextSize, nil)

	persisted, err := SetSessionContextSize(32768)
	if err != nil {
		t.Fatalf("SetSessionContextSize() error: %v", err)
	}
	if persisted {
		t.Fatal("expected no persistence without a real session")
	}
	if got := GetContextSize(); got != 32768 {
		t.Fatalf("GetContextSize() = %d, want 32768 (applied in-memory)", got)
	}

	// A stealth (dummy ID -1) session behaves the same way.
	MainRuntime().Session = &data.Session{ID: -1}
	persisted, err = SetSessionContextSize(65536)
	if err != nil {
		t.Fatalf("SetSessionContextSize() error: %v", err)
	}
	if persisted {
		t.Fatal("expected no persistence for a stealth session")
	}
	if got := GetContextSize(); got != 65536 {
		t.Fatalf("GetContextSize() = %d, want 65536 (applied in-memory)", got)
	}
}

func TestSetSessionAdoptsContextSize(t *testing.T) {
	setTestMainRuntime(t, NewAgentRuntime())

	session := &data.Session{ID: 1, Name: "test", ContextSize: 32768}
	SetSession(session, make([]data.Message, 0))

	if got := GetContextSize(); got != 32768 {
		t.Fatalf("GetContextSize() after loading session = %d, want 32768", got)
	}
	if session.ContextSize != 32768 {
		t.Fatalf("session.ContextSize = %d, want 32768", session.ContextSize)
	}

	// Switching to a new (nil) session must reset to the default rather than
	// inheriting the previous session's sizing.
	SetSession(nil, nil)
	if got := GetContextSize(); got != data.DefaultContextSize {
		t.Fatalf("GetContextSize() after clearing session = %d, want default %d", got, data.DefaultContextSize)
	}

	// Legacy sessions with an unset (0) size also fall back to the default.
	SetSession(&data.Session{ID: 2, Name: "legacy"}, make([]data.Message, 0))
	if got := GetContextSize(); got != data.DefaultContextSize {
		t.Fatalf("GetContextSize() with legacy session = %d, want default %d", got, data.DefaultContextSize)
	}
}
