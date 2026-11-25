package data

import (
	"testing"
)

func TestReasoningParseFromModelsJSON(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	writeFixtures(t, global, map[string]string{
		"models.json": `[
			{"id": "with-block", "name": "W",
			 "reasoning": {"options": ["low", "medium", "xhigh"], "default": "xhigh", "enabled": true}},
			{"id": "no-block", "name": "N"}
		]`,
	})
	withLayeredRoots(t, global, project)

	cfg := LoadLayeredConfig()
	w := cfg.GetModelByID("with-block")
	if w == nil || w.Reasoning == nil {
		t.Fatalf("with-block model/reasoning missing: %+v", w)
	}
	if !w.ReasoningIsConfigurable() {
		t.Fatal("expected with-block to be configurable (enabled=true)")
	}
	if d := w.ReasoningDefaultLevel(); d != "xhigh" {
		t.Fatalf("default = %q, want xhigh", d)
	}

	n := cfg.GetModelByID("no-block")
	if n == nil || n.Reasoning != nil {
		t.Fatalf("no-block should have a nil reasoning block: %+v", n)
	}
	if n.ReasoningIsConfigurable() {
		t.Fatal("no-block must not be configurable")
	}
}

func TestReasoningSelectableLevelsNoneFirst(t *testing.T) {
	m := &ModelConfig{Reasoning: &ReasoningConfig{Options: []string{"low", "medium"}, Enabled: true}}
	got := m.ReasoningSelectableLevels()
	want := []string{"none", "low", "medium"}
	if len(got) != len(want) {
		t.Fatalf("levels = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("levels[%d] = %q, want %q (full %v)", i, got[i], want[i], got)
		}
	}

	// A model that already lists "none" must not duplicate it.
	m2 := &ModelConfig{Reasoning: &ReasoningConfig{Options: []string{"none", "high"}, Enabled: true}}
	if g := m2.ReasoningSelectableLevels(); len(g) != 2 || g[0] != "none" || g[1] != "high" {
		t.Fatalf("dedup levels = %v, want [none high]", g)
	}

	// No block: only "none".
	m3 := &ModelConfig{}
	if g := m3.ReasoningSelectableLevels(); len(g) != 1 || g[0] != "none" {
		t.Fatalf("no-block levels = %v, want [none]", g)
	}
}

func TestReasoningDefaultLevelEmptyMeansNone(t *testing.T) {
	m := &ModelConfig{Reasoning: &ReasoningConfig{Options: []string{"low"}, Enabled: true}}
	if d := m.ReasoningDefaultLevel(); d != "none" {
		t.Fatalf("empty default = %q, want none", d)
	}
	(&ModelConfig{}).ReasoningDefaultLevel() // must not panic
	m2 := &ModelConfig{Reasoning: &ReasoningConfig{Default: "high"}}
	if d := m2.ReasoningDefaultLevel(); d != "high" {
		t.Fatalf("default = %q, want high", d)
	}
}

func TestSetReasoningEffortRoundTrip(t *testing.T) {
	if _, err := OpenTestDB(t.TempDir() + "/db"); err != nil {
		t.Fatal(err)
	}
	defer CloseTestDB()

	s, err := NewSession("reasoning-test")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := SetReasoningEffort(s.ID, "xhigh"); err != nil {
		t.Fatalf("SetReasoningEffort: %v", err)
	}
	// ListSessions skips rows with no messages; add one so this session is listed.
	if _, err := SaveMessage(s, Message{Contents: "hi", Source: "user", Model: "m"}); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}

	listed, err := ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	var found *Session
	for i := range listed {
		if listed[i].ID == s.ID {
			found = &listed[i]
		}
	}
	if found == nil || found.ReasoningEffort != "xhigh" {
		t.Fatalf("reloaded ReasoningEffort = %+v, want xhigh", found)
	}

	// Clearing it back to "" (inherit) must round-trip too.
	if err := SetReasoningEffort(s.ID, ""); err != nil {
		t.Fatal(err)
	}
	listed, _ = ListSessions()
	for i := range listed {
		if listed[i].ID == s.ID && listed[i].ReasoningEffort != "" {
			t.Fatalf("cleared ReasoningEffort = %q, want empty", listed[i].ReasoningEffort)
		}
	}
}
