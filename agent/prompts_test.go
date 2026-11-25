package agent

import (
	"testing"

	"clankerv2/data"
)

// promptPresent drives one presentPrompt call in a goroutine and returns the
// UI channel plus the result channel; tests resolve from the main goroutine.
func promptPresent(t *testing.T, kind PromptKind, id string) (chan any, <-chan any) {
	t.Helper()
	// Buffer 2: presentPrompt emits StateMsg(waiting) + payload before returning.
	ch := make(chan any, 2)
	resultCh := make(chan (<-chan any), 1)
	go func() {
		resultCh <- presentPrompt(kind, id, ch, "payload")
	}()
	entry := <-resultCh

	// Drain state events until the payload itself arrives.
	var ev any
	for {
		ev = <-ch
		if _, isState := ev.(data.StateMsg); !isState {
			break
		}
	}
	if ev != "payload" {
		t.Fatalf("expected payload on channel, got %v", ev)
	}
	return ch, entry
}

// TestPromptFirstResponseWins locks the parallel-UI contract: exactly one
// resolver may unblock the waiting goroutine; racing duplicates and
// wrong-kind resolvers are dropped without error or double-delivery.
func TestPromptFirstResponseWins(t *testing.T) {
	// Winner is the FIRST delivery, even when two race.
	_, resultCh := promptPresent(t, PromptPermission, "perm-100")
	ResolvePermission("perm-100", false)
	ResolvePermission("perm-100", true) // racing duplicate: ignored
	if got := <-resultCh; got != false {
		t.Fatalf("first response must win: got %v", got)
	}

	// Resolving after the prompt is already gone is a logged no-op.
	ResolvePermission("perm-100", true)

	// A wrong-kind resolver can never touch another kind's prompt (ids are
	// namespaced, but the kind check keeps that guarantee structural).
	_, resultCh = promptPresent(t, PromptAsk, "ask-100")
	ResolvePermission("ask-100", true) // no-op: kind mismatch
	CancelAsk("ask-100")
	if got := <-resultCh; got != askCancelledEnvelope() {
		t.Fatalf("expected cancelled envelope, got %v", got)
	}

	// Duplicate ids are rejected at registration: the duplicate call returns a
	// dead channel (its payload never reaches the UI), while the LIVE prompt
	// stays resolvable and keeps its own result.
	_, resultCh = promptPresent(t, PromptPermission, "perm-101")
	dupUI := make(chan any, 8)
	dupResult := make(chan (<-chan any), 1)
	go func() {
		dupResult <- presentPrompt(PromptPermission, "perm-101", dupUI, "dup")
	}()
	select {
	case ev := <-dupUI:
		if _, isState := ev.(data.StateMsg); !isState {
			t.Fatalf("duplicate payload must not reach the UI channel: %v", ev)
		}
	default:
	}
	<-dupResult // drain so the goroutine exits cleanly
	ResolvePermission("perm-101", true)
	if got := <-resultCh; got != true {
		t.Fatalf("live prompt must stay resolvable after a duplicate attempt: got %v", got)
	}
}
