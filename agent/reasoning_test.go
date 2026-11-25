package agent

import (
	"clankerv2/data"
	"testing"
)

// withReasoningModel installs an app config holding a single chat model carrying
// the given reasoning block, plus a main runtime bound to it. Returns the model.
func withReasoningModel(t *testing.T, id string, rc *data.ReasoningConfig) *data.ModelConfig {
	t.Helper()
	mc := &data.ModelConfig{ID: id, Name: id, Type: "chat", Reasoning: rc}

	prevCfg := GetAppConfig()
	SetAppConfig(&data.AppConfig{Models: []data.ModelConfig{*mc}})
	t.Cleanup(func() { SetAppConfig(prevCfg) })

	prevRT := GetMainRuntimeInternal()
	rt := NewAgentRuntime()
	rt.ModelID = id
	SetMainRuntime(rt)
	t.Cleanup(func() { SetMainRuntime(prevRT) })

	return mc
}

// resolveFor runs the pure model resolver, returning level and present flag.
func resolveFor(mc *data.ModelConfig, chosen string) (string, bool) {
	return resolveReasoningForModel(mc, chosen)
}

func TestResolveNoBlockOmitsKey(t *testing.T) {
	withReasoningModel(t, "m1", nil)
	if lvl, ok := resolveFor(GetAppConfig().GetModelByID("m1"), ""); ok || lvl != "" {
		t.Fatalf("no-block: got (%q,%v), want (\"\",false)", lvl, ok)
	}
	// A non-none choice still yields nothing when there is no block.
	if lvl, ok := resolveFor(GetAppConfig().GetModelByID("m1"), "high"); ok || lvl != "" {
		t.Fatalf("no-block with choice: got (%q,%v), want (\"\",false)", lvl, ok)
	}
}

func TestResolveDisabledAlwaysNone(t *testing.T) {
	withReasoningModel(t, "m2", &data.ReasoningConfig{Options: []string{"low", "high"}, Default: "high", Enabled: false})
	mc := GetAppConfig().GetModelByID("m2")
	if lvl, ok := resolveFor(mc, ""); !ok || lvl != "none" {
		t.Fatalf("disabled default: got (%q,%v), want (none,true)", lvl, ok)
	}
	// Even an explicit choice is overridden to none when disabled.
	if lvl, ok := resolveFor(mc, "low"); !ok || lvl != "none" {
		t.Fatalf("disabled with choice: got (%q,%v), want (none,true)", lvl, ok)
	}
}

func TestResolveDefaultNoneSendsNone(t *testing.T) {
	withReasoningModel(t, "m3", &data.ReasoningConfig{Options: []string{"low", "high"}, Default: "", Enabled: true})
	mc := GetAppConfig().GetModelByID("m3")
	// Empty default => none by default.
	if lvl, ok := resolveFor(mc, ""); !ok || lvl != "none" {
		t.Fatalf("default-none: got (%q,%v), want (none,true)", lvl, ok)
	}
	// An explicit real level passes through.
	if lvl, ok := resolveFor(mc, "high"); !ok || lvl != "high" {
		t.Fatalf("explicit high: got (%q,%v), want (high,true)", lvl, ok)
	}
}

func TestResolveUserLevelPassesThrough(t *testing.T) {
	withReasoningModel(t, "m4", &data.ReasoningConfig{Options: []string{"low", "medium", "xhigh"}, Default: "xhigh", Enabled: true})
	mc := GetAppConfig().GetModelByID("m4")
	if lvl, _ := resolveFor(mc, ""); lvl != "xhigh" {
		t.Fatalf("default = %q, want xhigh", lvl)
	}
	if lvl, _ := resolveFor(mc, "low"); lvl != "low" {
		t.Fatalf("chosen low = %q, want low", lvl)
	}
	if lvl, _ := resolveFor(mc, "none"); lvl != "none" {
		t.Fatalf("chosen none = %q, want none", lvl)
	}
}

func TestResolveInvalidLevelClampsToDefault(t *testing.T) {
	withReasoningModel(t, "m5", &data.ReasoningConfig{Options: []string{"low", "high"}, Default: "high", Enabled: true})
	mc := GetAppConfig().GetModelByID("m5")
	// "medium" is not an option and is non-none => clamp to default.
	if lvl, _ := resolveFor(mc, "medium"); lvl != "high" {
		t.Fatalf("invalid level = %q, want high (clamped)", lvl)
	}
	// A valid one passes.
	if lvl, _ := resolveFor(mc, "low"); lvl != "low" {
		t.Fatalf("valid low = %q, want low", lvl)
	}
}

func TestGetReasoningLevelForModel(t *testing.T) {
	withReasoningModel(t, "m6", &data.ReasoningConfig{Options: []string{"low", "high"}, Default: "high", Enabled: true})
	if got := GetReasoningLevelForModel("m6"); got != "high" {
		t.Fatalf("GetReasoningLevelForModel() = %q, want high (model default)", got)
	}
	MainRuntime().ReasoningEffort = "low"
	if got := GetReasoningLevelForModel("m6"); got != "low" {
		t.Fatalf("GetReasoningLevelForModel() after choice = %q, want low", got)
	}
	// Unknown model => empty.
	if got := GetReasoningLevelForModel("nope"); got != "" {
		t.Fatalf("unknown model = %q, want empty", got)
	}
}

func TestReasoningWebHelperFunctions(t *testing.T) {
	withReasoningModel(t, "m7", &data.ReasoningConfig{Options: []string{"low", "high"}, Default: "high", Enabled: true})

	if opts := ReasoningOptionsForModel("m7"); len(opts) != 3 || opts[0] != "none" {
		t.Fatalf("options = %v, want [none low high]", opts)
	}
	if lvl := ReasoningLevelForModel("m7", ""); lvl != "high" {
		t.Fatalf("level(empty) = %q, want high", lvl)
	}
	if lvl := ReasoningLevelForModel("m7", "low"); lvl != "low" {
		t.Fatalf("level(low) = %q, want low", lvl)
	}
	// Unknown model => no options, empty level (web hides the selector).
	if opts := ReasoningOptionsForModel("nope"); opts != nil {
		t.Fatalf("unknown model options = %v, want nil", opts)
	}
	if lvl := ReasoningLevelForModel("nope", "low"); lvl != "" {
		t.Fatalf("unknown model level = %q, want empty", lvl)
	}
}

func TestSetReasoningEffortPersistsOnlyNonDefault(t *testing.T) {
	if _, err := data.OpenTestDB(t.TempDir() + "/db"); err != nil {
		t.Fatal(err)
	}
	defer data.CloseTestDB()

	withReasoningModel(t, "m8", &data.ReasoningConfig{Options: []string{"low", "high"}, Default: "high", Enabled: true})
	rt := MainRuntime()
	s, err := data.NewSession("reasoning")
	if err != nil {
		t.Fatal(err)
	}
	rt.Session = s

	// Setting the default itself must NOT persist an override (stored == "").
	persisted, err := SetReasoningEffort("m8", "high")
	if err != nil || !persisted {
		t.Fatalf("set default: persisted=%v err=%v", persisted, err)
	}
	if s.ReasoningEffort != "" {
		t.Fatalf("default choice stored = %q, want empty (inherits)", s.ReasoningEffort)
	}

	// A non-default choice persists.
	persisted, err = SetReasoningEffort("m8", "low")
	if err != nil || !persisted {
		t.Fatalf("set low: persisted=%v err=%v", persisted, err)
	}
	if s.ReasoningEffort != "low" {
		t.Fatalf("non-default stored = %q, want low", s.ReasoningEffort)
	}

	// An unsupported level is rejected and does not mutate state.
	if _, err := SetReasoningEffort("m8", "medium"); err == nil {
		t.Fatal("expected error for unsupported level medium")
	}
	if rt.ReasoningEffort != "low" {
		t.Fatalf("state changed after rejected set: %q", rt.ReasoningEffort)
	}
}

func TestSetReasonInMemoryOnlyWhenNoSession(t *testing.T) {
	withReasoningModel(t, "m9", &data.ReasoningConfig{Options: []string{"low", "high"}, Default: "high", Enabled: true})
	persisted, err := SetReasoningEffort("m9", "low")
	if err != nil {
		t.Fatal(err)
	}
	if persisted {
		t.Fatal("no session => must not persist")
	}
	if got := GetReasoningLevelForModel("m9"); got != "low" {
		t.Fatalf("in-memory level = %q, want low", got)
	}
}

// TestResolveSubagentReasoning covers the launch-time inheritance rule: a
// subagent on the caller's model inherits its chosen level; any other model
// falls back to its own default ("", resolved per-model later).
func TestResolveSubagentReasoning(t *testing.T) {
	if got := resolveSubagentReasoning("m1", "m1", "low"); got != "low" {
		t.Fatalf("same model inherit = %q, want low", got)
	}
	// Same model with no caller choice still passes through (empty stays empty).
	if got := resolveSubagentReasoning("m1", "m1", ""); got != "" {
		t.Fatalf("same model no choice = %q, want empty", got)
	}
	// Different model never inherits the caller's level.
	if got := resolveSubagentReasoning("m2", "m1", "low"); got != "" {
		t.Fatalf("different model = %q, want empty (model default)", got)
	}
	// An empty subagent model is not a match.
	if got := resolveSubagentReasoning("", "m1", "low"); got != "" {
		t.Fatalf("empty subagent model = %q, want empty", got)
	}
}
