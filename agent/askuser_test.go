package agent

import (
	"strings"
	"testing"

	"clankerv2/data"
)

// askRoundTrip drives one question through the real AskUser entrypoint: it
// captures the AskUserRequest emitted on the channel, resolves it with a stub
// answer, and returns the request plus the tool-result envelope.
func askRoundTrip(t *testing.T, q map[string]any) (data.AskUserRequest, string) {
	t.Helper()

	// Buffer 2: presentPrompt emits StateMsg(waiting) + payload before blocking.
	ch := make(chan any, 2)
	resolved := make(chan string, 1)
	go func() { resolved <- AskUser(ch, []any{q}) }()

	var ev any
	for {
		ev = <-ch
		if _, isState := ev.(data.StateMsg); !isState {
			break
		}
	}
	req, ok := ev.(data.AskUserRequest)
	if !ok {
		t.Fatalf("expected data.AskUserRequest on channel, got %T", ev)
	}
	if len(req.Questions) != 1 {
		t.Fatalf("expected exactly 1 question in request, got %d", len(req.Questions))
	}

	ResolveAsk(req.ID, map[string]string{req.Questions[0].ID: "stub"})
	out := <-resolved
	return req, out
}

// TestAllowFreeFormDefaultsOn locks the model-facing contract: a free-text
// answer row is offered for select / multi-select questions UNLESS the model
// explicitly opts out. Omitted means true; only an explicit non-true value
// (false, null, …) turns it off.
func TestAllowFreeFormDefaultsOn(t *testing.T) {
	base := func(extra map[string]any) map[string]any {
		q := map[string]any{
			"id":      "q1",
			"type":    "select",
			"prompt":  "pick one",
			"options": []any{map[string]any{"label": "alpha", "value": "a"}},
		}
		for k, v := range extra {
			q[k] = v
		}
		return q
	}

	cases := []struct {
		name string
		extra map[string]any
		want  bool
	}{
		{"omitted defaults true", nil, true},
		{"explicit true", map[string]any{"allow_free_form": true}, true},
		{"explicit false disables", map[string]any{"allow_free_form": false}, false},
		{"null treated as off", map[string]any{"allow_free_form": nil}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, out := askRoundTrip(t, base(tc.extra))
			if got := req.Questions[0].AllowFreeForm; got != tc.want {
				t.Errorf("AllowFreeForm = %v, want %v", got, tc.want)
			}
			if !strings.Contains(out, `"status":"success"`) {
				t.Errorf("expected success envelope after resolve, got %s", out)
			}
		})
	}
}
