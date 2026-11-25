package data

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validSkillMD = `---
name: my-skill
description: >-
  A multi-line
  folded description.
---
Step one instructions here.
`

func TestParseSkillFrontmatterSimple(t *testing.T) {
	name, desc, body, _, err := parseSkillFrontmatter("---\nname: foo\ndescription: bar\n---\nDo the thing.\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "foo" || desc != "bar" || body != "Do the thing." {
		t.Errorf("got (%q, %q, %q), want (foo, bar, Do the thing.)", name, desc, body)
	}
}

func TestParseSkillFrontmatterFolded(t *testing.T) {
	name, desc, body, _, err := parseSkillFrontmatter(validSkillMD)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "my-skill" {
		t.Errorf("name = %q, want my-skill", name)
	}
	if !strings.Contains(desc, "A multi-line") || !strings.Contains(desc, "folded description.") {
		t.Errorf("folded description not joined: %q", desc)
	}
	if body != "Step one instructions here." {
		t.Errorf("body = %q", body)
	}
}

func TestParseSkillFrontmatterMalformed(t *testing.T) {
	cases := map[string]string{
		"no header":       "# Just markdown\nname: x\ndescription: y\n",
		"unterminated":    "---\nname: x\ndescription: y\n",
		"bad yaml":        "---\nname: [unclosed\ndescription: y\n---\nbody\n",
		"missing name":    "---\ndescription: y\n---\nbody\n",
		"missing desc":    "---\nname: x\n---\nbody\n",
		"name with slash": "---\nname: a/b\ndescription: y\n---\nbody\n",
		"dotted name":     "---\nname: jira.get_ticket\ndescription: y\n---\nbody\n",
	}
	for label, raw := range cases {
		if _, _, _, _, err := parseSkillFrontmatter(raw); err == nil {
			t.Errorf("%s: expected an error, got none", label)
		}
	}
}

func TestSkillsLocalShadowsGlobal(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	writeFixtures(t, global, map[string]string{
		"skills/foo/SKILL.md": "---\nname: foo\ndescription: global version\n---\nglobal body\n",
	})
	writeFixtures(t, project, map[string]string{
		".clanker/skills/foo/SKILL.md":  "---\nname: foo\ndescription: local version\n---\nlocal body\n",
		".clanker/skills/bar/SKILL.md":  "---\nname: bar\ndescription: local only\n---\nbar body\n",
	})
	withLayeredRoots(t, global, project)

	cfg := LoadLayeredConfig()
	if got := len(cfg.Skills); got != 2 {
		t.Fatalf("len(Skills) = %d, want 2 (shadowed foo + bar)", got)
	}
	foo := cfg.GetSkill("foo")
	if foo == nil || foo.Description != "local version" || !strings.Contains(foo.Body, "local body") {
		t.Errorf("foo not shadowed by local layer: %+v", foo)
	}
	if cfg.GetSkill("bar") == nil {
		t.Error("expected local-only skill bar to be loaded")
	}
}

func TestSkillsIntraLayerCollisionRejectsAll(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	writeFixtures(t, global, map[string]string{
		"skills/a/SKILL.md": "---\nname: dup\ndescription: first\n---\nx\n",
		"skills/b/SKILL.md": "---\nname: dup\ndescription: second\n---\ny\n",
	})
	withLayeredRoots(t, global, project)

	cfg := LoadLayeredConfig()
	if cfg.GetSkill("dup") != nil {
		t.Error("expected colliding skills to all be rejected")
	}
	foundError := false
	for _, d := range cfg.SkillDiagnostics {
		if strings.Contains(d, "collision") && strings.Contains(d, `dup`) {
			foundError = true
		}
	}
	if !foundError {
		t.Errorf("expected a collision diagnostic, got %v", cfg.SkillDiagnostics)
	}
}

func TestSkillsMalformedWarnsAndSkips(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	writeFixtures(t, project, map[string]string{
		".clanker/skills/good/SKILL.md": "---\nname: good\ndescription: fine\n---\nok\n",
		".clanker/skills/bad/SKILL.md":  "no frontmatter at all\n",
	})
	withLayeredRoots(t, global, project)

	cfg := LoadLayeredConfig()
	if cfg.GetSkill("good") == nil {
		t.Error("expected valid skill to load alongside a malformed one")
	}
	foundWarn := false
	for _, d := range cfg.SkillDiagnostics {
		if strings.Contains(d, "malformed") && strings.Contains(d, "bad") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("expected a malformed diagnostic for 'bad', got %v", cfg.SkillDiagnostics)
	}
}

func TestSkillsReservedNameRejected(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	writeFixtures(t, project, map[string]string{
		".clanker/skills/model/SKILL.md": "---\nname: model\ndescription: tries to shadow /model\n---\nx\n",
	})
	withLayeredRoots(t, global, project)

	cfg := LoadLayeredConfig()
	if cfg.GetSkill("model") != nil {
		t.Error("expected a skill named after a built-in command to be rejected")
	}
	foundError := false
	for _, d := range cfg.SkillDiagnostics {
		if strings.Contains(d, "collides with the built-in /model") {
			foundError = true
		}
	}
	if !foundError {
		t.Errorf("expected a reserved-name diagnostic, got %v", cfg.SkillDiagnostics)
	}
}

func TestSystemSkillsToolInjectedOnlyWhenSkillsExist(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	writeFixtures(t, project, map[string]string{
		".clanker/skills/alpha/SKILL.md": "---\nname: alpha\ndescription: does things\n---\nx\n",
	})
	withLayeredRoots(t, global, project)

	cfg := LoadLayeredConfig()
	tt := cfg.ToolByName("system-skills")
	if tt == nil {
		t.Fatal("expected system-skills in the tool list when skills exist")
	}
	if !strings.Contains(tt.Function.Description, "alpha: does things") {
		t.Errorf("tool description should embed the skill roster:\n%s", tt.Function.Description)
	}

	// No skills at all: no injected tool.
	global2 := t.TempDir()
	project2 := t.TempDir()
	withLayeredRoots(t, global2, project2)
	cfg2 := LoadLayeredConfig()
	if cfg2.ToolByName("system-skills") != nil {
		t.Error("expected system-skills to be absent when no skills are loaded")
	}
}

func TestParseSkillFrontmatterSubagentFlag(t *testing.T) {
	name, _, _, sub, err := parseSkillFrontmatter("---\nname: foo\ndescription: bar\nsubagent: true\n---\nbody\n")
	if err != nil || name != "foo" {
		t.Fatalf("parse failed: %v (name=%q)", err, name)
	}
	if sub == nil || !*sub {
		t.Errorf("subagent: true should decode to non-nil true, got %v", sub)
	}

	_, _, _, sub, err = parseSkillFrontmatter("---\nname: foo\ndescription: bar\nsubagent: false\n---\nbody\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub == nil || *sub {
		t.Errorf("subagent: false should decode to non-nil false, got %v", sub)
	}

	_, _, _, sub, err = parseSkillFrontmatter("---\nname: foo\ndescription: bar\n---\nbody\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub != nil {
		t.Errorf("absent subagent key should decode to nil, got %v", sub)
	}
}

func TestRunsAsSubagentDefaultInline(t *testing.T) {
	b := func(v bool) *bool { return &v }
	if (&Skill{}).RunsAsSubagent() {
		t.Error("nil Subagent must default to inline")
	}
	if !(&Skill{Subagent: b(true)}).RunsAsSubagent() {
		t.Error("explicit true must run as subagent")
	}
	if (&Skill{Subagent: b(false)}).RunsAsSubagent() {
		t.Error("explicit false must stay inline")
	}
}

func TestSkillTaskPrompt(t *testing.T) {
	skill := &Skill{Name: "alpha", Dir: "/proj/.clanker/skills/alpha"}
	p := skill.SkillTaskPrompt("target repo")
	if !strings.Contains(p, "/proj/.clanker/skills/alpha/SKILL.md") {
		t.Error("task prompt must tell the subagent to read SKILL.md by absolute path")
	}
	if !strings.Contains(p, "system-subagent_finalize") {
		t.Error("task prompt must require a finalize call with the report")
	}
	if !strings.Contains(p, "target repo") {
		t.Errorf("user args must be forwarded into the task: %q", p)
	}

	noArgs := skill.SkillTaskPrompt("")
	if strings.Contains(noArgs, "arguments for this run") {
		t.Errorf("empty args should not produce an args section: %q", noArgs)
	}
}

func TestSkillInlinePrompt(t *testing.T) {
	skill := &Skill{Name: "alpha", Dir: "/s", Body: "Step 1. Do it."}
	p := skill.SkillInlinePrompt("my args")
	if !strings.Contains(p, "alpha") || !strings.Contains(p, "Step 1. Do it.") {
		t.Errorf("inline prompt must carry the name and full body: %q", p)
	}
	if !strings.Contains(p, "my args") {
		t.Errorf("user args must be included: %q", p)
	}
	if strings.Contains(p, "SKILL.md with system-read") {
		t.Error("inline prompt runs from the embedded body; it must not re-read SKILL.md")
	}

	plain := skill.SkillInlinePrompt("")
	if strings.Contains(plain, "arguments for this run") {
		t.Errorf("empty args should not produce an args section: %q", plain)
	}
}

func TestReloadSkillsAddRemoveRename(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	writeFixtures(t, project, map[string]string{
		".clanker/skills/keep/SKILL.md":   "---\nname: keep\ndescription: v1\n---\nbody-v1\n",
		".clanker/skills/deleteme/SKILL.md": "---\nname: deleteme\ndescription: doomed\n---\nx\n",
	})
	withLayeredRoots(t, global, project)
	cfg := LoadLayeredConfig()
	if cfg.GetSkill("keep") == nil || cfg.GetSkill("deleteme") == nil {
		t.Fatal("initial skills should load")
	}

	// Edit, rename-ish (new skill), delete.
	os.WriteFile(filepath.Join(project, ".clanker/skills/keep/SKILL.md"), []byte("---\nname: keep\ndescription: v2\n---\nbody-v2\n"), 0o644)
	os.RemoveAll(filepath.Join(project, ".clanker/skills/deleteme"))
	writeFixtures(t, project, map[string]string{
		".clanker/skills/new-skill/SKILL.md": "---\nname: new-skill\ndescription: fresh\n---\nx\n",
	})

	res := cfg.ReloadSkills()
	if res == nil {
		t.Fatal("expected a reload result")
	}
	if !contains(res.Added, "new-skill") {
		t.Errorf("expected new-skill in Added: %v", res.Added)
	}
	if !contains(res.Removed, "deleteme") {
		t.Errorf("expected deleteme in Removed: %v", res.Removed)
	}
	if !contains(res.Updated, "keep") {
		t.Errorf("expected keep in Updated: %v", res.Updated)
	}
	if cfg.GetSkill("deleteme") != nil {
		t.Error("deleted skill should be gone")
	}
	if s := cfg.GetSkill("keep"); s == nil || s.Body != "body-v2" {
		t.Errorf("keep not refreshed: %+v", s)
	}
	// system-skills tool refreshed with new roster.
	tt := cfg.ToolByName("system-skills")
	if tt == nil {
		t.Fatal("system-skills tool should still exist")
	}
	if !strings.Contains(tt.Function.Description, "new-skill") {
		t.Errorf("tool description should list new skill: %s", tt.Function.Description)
	}
	if strings.Contains(tt.Function.Description, "deleteme") {
		t.Errorf("tool description should not list deleted skill: %s", tt.Function.Description)
	}
}

func TestReloadSkillsResetsDiagnosticsAndRejectsReserved(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	writeFixtures(t, project, map[string]string{
		".clanker/skills/model/SKILL.md": "---\nname: model\ndescription: shadows built-in\n---\nx\n",
	})
	withLayeredRoots(t, global, project)
	cfg := LoadLayeredConfig()
	if len(cfg.SkillDiagnostics) == 0 {
		t.Fatal("expected at least one diagnostic at load")
	}
	// Edit the malformed-reserved skill's description — reload should reset and re-emit.
	res := cfg.ReloadSkills()
	if res == nil {
		t.Fatal("expected a reload result")
	}
	if len(cfg.SkillDiagnostics) == 0 {
		t.Error("expected reserved-name diagnostic after reload")
	}
	found := false
	for _, d := range cfg.SkillDiagnostics {
		if strings.Contains(d, "collides with the built-in /model") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected reserved-name diagnostic, got %v", cfg.SkillDiagnostics)
	}
}

func TestLoadSkillFromDiskReadsFreshContent(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	dir := filepath.Join(project, ".clanker/skills/alpha")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: alpha\ndescription: v1\n---\nbody-v1\n"), 0o644)
	withLayeredRoots(t, global, project)
	cfg := LoadLayeredConfig()
	s := cfg.GetSkill("alpha")
	if s == nil {
		t.Fatal("expected alpha")
	}
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: alpha\ndescription: v2\n---\nbody-v2\n"), 0o644)
	fresh, err := LoadSkillFromDisk(s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fresh.Body != "body-v2" || fresh.Description != "v2" {
		t.Errorf("fresh skill = %+v, want body-v2", fresh)
	}
	// Error path: missing file returns original + error.
	os.Remove(filepath.Join(dir, "SKILL.md"))
	bad, err := LoadSkillFromDisk(fresh)
	if err == nil {
		t.Error("expected error for missing file")
	}
	if bad == nil {
		t.Error("expected stale skill returned on error")
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
