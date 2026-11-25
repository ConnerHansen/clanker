package data

import "testing"

func TestValidToolName(t *testing.T) {
	long64 := ""
	for i := 0; i < 64; i++ {
		long64 += "a"
	}

	valid := []string{"system-read", "jira-get_ticket", "bash-cmd", "go-fmt", "a", long64}
	for _, n := range valid {
		if !ValidToolName(n) {
			t.Errorf("ValidToolName(%q) = false, want true", n)
		}
	}

	invalid := []string{"", "system.read", "jira.get_ticket", "a.b.c", "foo/bar", "foo bar", long64 + "a"}
	for _, n := range invalid {
		if ValidToolName(n) {
			t.Errorf("ValidToolName(%q) = true, want false", n)
		}
	}
}

func TestCanonicalToolName(t *testing.T) {
	cases := map[string]string{
		"system.read":                  "system-read",
		"jira.get_ticket":              "jira-get_ticket",
		"system-register_compaction":   "system-register_compaction", // idempotent: dashes pass through
		"":                             "",
	}
	for in, want := range cases {
		if got := CanonicalToolName(in); got != want {
			t.Errorf("CanonicalToolName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAddToolsRejectsNonConformingNames(t *testing.T) {
	cfg := &AppConfig{}
	cfg.AddTools(
		Tool{Type: "function", Function: ToolFunctionDef{Name: "shared.tool", Description: "dotted"}},
		Tool{Type: "function", Function: ToolFunctionDef{Name: "shared-tool", Description: "ok"}},
	)
	if len(cfg.Tools) != 1 {
		t.Fatalf("tools = %d entries, want 1 (dotted name rejected): %+v", len(cfg.Tools), cfg.Tools)
	}
	if cfg.Tools[0].Function.Name != "shared-tool" {
		t.Fatalf("surviving tool = %q, want shared-tool", cfg.Tools[0].Function.Name)
	}
}
