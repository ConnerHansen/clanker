package tui

import (
	"clankerv2/data"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
)

func TestSplitSkillCommand(t *testing.T) {
	cases := []struct {
		in   string
		name string
		args string
	}{
		{"/alpha", "alpha", ""},
		{"/alpha do the thing", "alpha", "do the thing"},
		{"/alpha  spaced   args ", "alpha", "spaced   args"}, // internal spaces kept, trailing trimmed
		{"/model x", "model", "x"},                            // reserved-name rejection is downstream (config load)
		{"", "", ""},
		{"/", "", ""},
	}
	for _, c := range cases {
		gotName, gotArgs := splitSkillCommand(c.in)
		if gotName != c.name || gotArgs != c.args {
			t.Errorf("splitSkillCommand(%q) = (%q, %q), want (%q, %q)", c.in, gotName, gotArgs, c.name, c.args)
		}
	}
}

// TestBuildAutocompleteItemsIncludesSkills guards against skills being loaded
// but missing from the typed /-command list: every registered command and
// every skill name must appear, sorted.
func TestBuildAutocompleteItemsIncludesSkills(t *testing.T) {
	cfg := &data.AppConfig{
		Skills: []data.Skill{
			{Name: "jira-get_ticket", Description: "d"},
			{Name: "zzz_last", Description: "d"},
			{Name: "aaa_first", Description: "d"},
		},
	}
	items := buildAutocompleteItems(cfg)

	wantStatic := make(map[string]bool, len(commandRegistry))
	for _, def := range commandRegistry {
		wantStatic[def.Key] = true
	}
	gotSkills := map[string]bool{}
	seen := make(map[string]bool, len(items))
	var prev string
	for i, it := range items {
		mi := it.(modelItem)
		if seen[mi.name] {
			t.Errorf("duplicate autocomplete item %q", mi.name)
		}
		seen[mi.name] = true
		if i > 0 && !(prev < mi.name) {
			t.Errorf("autocomplete items not sorted: %q after %q", mi.name, prev)
		}
		prev = mi.name
		switch {
		case wantStatic[mi.name]:
		default:
			gotSkills[strings.TrimPrefix(mi.name, "/")] = true
		}
	}
	if len(items) != len(commandRegistry)+3 {
		t.Fatalf("got %d autocomplete items, want %d (registry + 3 skills)", len(items), len(commandRegistry)+3)
	}
	for _, s := range cfg.Skills {
		if !gotSkills[s.Name] {
			t.Errorf("skill /%s missing from autocomplete items", s.Name)
		}
	}

	// nil config (agent not initialized) must still yield the static set.
	staticOnly := buildAutocompleteItems(nil)
	if len(staticOnly) != len(commandRegistry) {
		t.Errorf("nil-config items = %d, want %d", len(staticOnly), len(commandRegistry))
	}
}

// TestFilterCommandItems keeps a command listed while its params are typed:
// matching uses only the first whitespace-delimited token of the input.
func TestFilterCommandItems(t *testing.T) {
	items := []list.Item{
		modelItem{name: "/jira-get_ticket", data: "jira-get_ticket"},
		modelItem{name: "/context", data: "/context"},
		modelItem{name: "/compact", data: "/compact"},
	}

	filterNames := func(val string) []string {
		got := filterCommandItems(val, items)
		names := make([]string, len(got))
		for i, it := range got {
			names[i] = it.(modelItem).name
		}
		return names
	}
	contains := func(names []string, want string) bool {
		for _, n := range names {
			if n == want {
				return true
			}
		}
		return false
	}

	cases := []struct {
		val        string
		mustHave   []string
		mustNot    []string
	}{
		// bare "/" and empty list every item
		{val: "/", mustHave: []string{"/jira-get_ticket", "/context", "/compact"}},
		{val: "", mustHave: []string{"/jira-get_ticket", "/context", "/compact"}},
		// name-only prefix
		{val: "/jira", mustHave: []string{"/jira-get_ticket"}, mustNot: []string{"/context"}},
		// full name still matches once params follow (the feature)
		{val: "/jira-get_ticket SYC-1234", mustHave: []string{"/jira-get_ticket"}},
		{val: "/jira-get_ticket   ", mustHave: []string{"/jira-get_ticket"}},
	}
	for _, c := range cases {
		names := filterNames(c.val)
		for _, want := range c.mustHave {
			if !contains(names, want) {
				t.Errorf("filterCommandItems(%q) = %v, missing %q", c.val, names, want)
			}
		}
		for _, not := range c.mustNot {
			if contains(names, not) {
				t.Errorf("filterCommandItems(%q) = %v, unexpectedly has %q", c.val, names, not)
			}
		}
	}

	// A token that matches no name (before any space) clears the list.
	if got := filterCommandItems("/zzz-no-match", items); len(got) != 0 {
		t.Errorf("filterCommandItems with non-matching first token = %d items, want 0", len(got))
	}
	// ...but trailing text after a matched name never clears it (params don't
	// affect matching — the core of this feature).
	if got := filterCommandItems("/jira-get_ticket any params here at all", items); len(got) != 1 {
		t.Errorf("filterCommandItems with matched name + params = %d items, want 1", len(got))
	}
}

// TestReservedSlashCommandsInSyncWithRegistry keeps data.ReservedSlashCommands
// (used to reject skills claiming built-in command names at config load) in
// lockstep with this registry — either side changed without the other fails.
func TestReservedSlashCommandsInSyncWithRegistry(t *testing.T) {
	have := make(map[string]bool, len(commandRegistry))
	for _, def := range commandRegistry {
		have[strings.TrimPrefix(def.Key, "/")] = true
	}
	reserved := make(map[string]bool, len(data.ReservedSlashCommands))
	for _, r := range data.ReservedSlashCommands {
		reserved[r] = true
	}
	for name := range have {
		if !reserved[name] {
			t.Errorf("built-in command /%s is missing from data.ReservedSlashCommands", name)
		}
	}
	for name := range reserved {
		if !have[name] {
			t.Errorf("data.ReservedSlashCommands lists %q but the TUI registry has no such command", name)
		}
	}
}
