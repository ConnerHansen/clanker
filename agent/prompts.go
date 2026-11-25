package agent

import (
	"encoding/json"
	"log"
	"strconv"
	"sync"
)

// prompts.go is the shared plumbing behind every blocking UI prompt — today
// permission approvals and system-ask questions, tomorrow any other prompt a
// tool or hook wants to surface. Both paths run through the same registry,
// the same emit-and-wait helper, and the same result-delivery rules, so a new
// prompt kind only needs a payload type plus a small wrapper function (see
// RequestPermission in agent.go and AskUser in askuser.go for the two current
// examples).
//
// Result delivery is FIRST-RESPONSE-WINS: the entry is removed from the
// registry under lock before the result is sent, so exactly one resolver can
// ever unblock the waiting tool goroutine; racing resolvers are dropped with
// a log line. That keeps the registry correct no matter which client
// answered and sets the groundwork for parallel UIs (e.g. CLI + web):
// whichever interface resolves the prompt unblocks the run, and once
// resolution events are broadcast to every client, one answer dismisses the
// prompt in all of them without any change to this layer.

// PromptKind names the payload type a pending prompt carries on the UI
// channel. A future prompt form is a new kind + payload type; nothing else in
// this file needs to know about it.
type PromptKind string

const (
	PromptPermission PromptKind = "permission"
	PromptAsk        PromptKind = "ask"
)

// pendingPrompt tracks one blocking UI request. The tool goroutine that
// raised it blocks on Result until the UI calls the matching Resolve*; the
// buffered channel makes delivery non-blocking, and the map delete in
// deliverPromptResult keeps it idempotent regardless of timing.
type pendingPrompt struct {
	Kind   PromptKind
	Result chan any // kind's final result: bool for permission, JSON envelope string for ask
}

var (
	promptMu  sync.Mutex
	prompts   = make(map[string]*pendingPrompt)
	idCounter int
)

// nextPromptID mints a globally unique prompt id, namespaced by kind so the
// registry never mixes payload types ("perm-3", "ask-7").
func nextPromptID(kind PromptKind) string {
	promptMu.Lock()
	defer promptMu.Unlock()
	idCounter++
	return string(kind) + "-" + strconv.Itoa(idCounter)
}

// presentPrompt registers the pending entry, emits the payload on the UI
// channel, and returns the result channel the caller blocks on. The send must
// not block in practice: every consumer passes a buffered or actively drained
// channel (runtime channels are buffered; subagent relays drop + log when
// full). Callers mint the id with nextPromptID before calling this so the
// duplicate check below is meaningful.
func presentPrompt(kind PromptKind, id string, channel chan any, payload any) <-chan any {
	entry := &pendingPrompt{Kind: kind, Result: make(chan any, 1)}

	promptMu.Lock()
	if _, dup := prompts[id]; dup {
		promptMu.Unlock()
		log.Printf("prompt %s (%s): duplicate id ignored", id, kind)
		// Nothing registered: return a channel no one will ever send on so the
		// caller blocks visibly instead of silently continuing.
		return make(chan any)
	}
	prompts[id] = entry
	promptMu.Unlock()

	respondState(channel, "waiting")
	channel <- payload
	log.Printf("prompt %s (%s): waiting for UI response", id, kind)
	return entry.Result
}

// deliverPromptResult resolves one pending prompt with its final result.
// First delivery wins: the entry is removed under lock before the send, so a
// racing second resolver finds nothing and is dropped here instead of
// double-resolving or re-using the prompt. Returns whether this call won.
func deliverPromptResult(kind PromptKind, id string, result any) bool {
	promptMu.Lock()
	entry, ok := prompts[id]
	if !ok || entry.Kind != kind {
		promptMu.Unlock()
		log.Printf("prompt %s (%s): no pending request to resolve", id, kind)
		return false
	}
	delete(prompts, id)
	promptMu.Unlock()

	entry.Result <- result // buffered (1) with a single winner sender: cannot block
	return true
}

// promptPendingCount reports how many prompts of a kind are awaiting a
// response. AskUser uses it to keep at most one question form in flight (the
// model-facing contract); permissions may queue behind each other.
func promptPendingCount(kind PromptKind) int {
	promptMu.Lock()
	defer promptMu.Unlock()
	n := 0
	for _, e := range prompts {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Resolve entry points (exported for UI backends: TUI, Wails app). All prompt
// exits live here so the registry has a single surface.

// ResolvePermission delivers a permission decision to the blocked
// RequestPermission call. True approves, false denies; the tool layer reads
// "denied" back in the resulting message string (see RequestPermission).
func ResolvePermission(id string, allowed bool) {
	if deliverPromptResult(PromptPermission, id, allowed) {
		log.Printf("permission %s: resolved (allowed=%v)", id, allowed)
	}
}

// ResolveAsk delivers the user's answers (question ID -> chosen option value
// or typed text) to a blocked system-ask form. The envelope is built BEFORE
// delivery so a marshal failure can never strand the tool goroutine on an
// undeliverable result.
func ResolveAsk(id string, answers map[string]string) {
	out := askSuccessEnvelope(answers)
	if deliverPromptResult(PromptAsk, id, out) {
		log.Printf("system-ask: request %s resolved with %d answer(s)", id, len(answers))
	}
}

// CancelAsk unblocks a pending system-ask as cancelled. Called when the user
// dismisses the form or the surrounding run is aborted.
func CancelAsk(id string) {
	if deliverPromptResult(PromptAsk, id, askCancelledEnvelope()) {
		log.Printf("system-ask: request %s cancelled", id)
	}
}

// ---------------------------------------------------------------------------
// Result envelopes (ask kind): the exact JSON strings the tool layer hands to
// the model. Kept in one place so every UI backend produces identical output.

func askSuccessEnvelope(answers map[string]string) string {
	out, err := json.Marshal(map[string]any{
		"status":  "success",
		"answers": answers,
	})
	if err != nil {
		log.Printf("system-ask: failed to encode answers: %v", err)
		return `{"status":"error","error":"failed to encode answers"}`
	}
	return string(out)
}

func askCancelledEnvelope() string {
	return `{"status":"cancelled"}`
}
