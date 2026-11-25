package data

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Skill is one loaded OpenAI-style skill: a directory under <layer>/skills/<name>
// whose SKILL.md carries a YAML frontmatter block (name + description) above the
// workflow instructions. Dir is absolute so both UIs and subagents can reach it;
// Body is the file content below the frontmatter (the instructions themselves).
type Skill struct {
	Name        string // frontmatter name: defines the /<name> slash command
	Description string // frontmatter description: shown in system-skills listings
	Dir         string // absolute path to the skill directory
	Body        string // SKILL.md content below the frontmatter block
	// Subagent is the optional `subagent:` frontmatter flag controlling how
	// /<name> runs the skill. Nil (key absent) defaults to false — inline on the
	// invoking agent's own context. An explicit true forces a background stealth
	// subagent that the caller awaits.
	Subagent *bool
}

// systemSkillsToolDef is the function schema of the built-in system-skills
// tool, appended to AppConfig.Tools at load time when at least one skill exists.
// It embeds every loaded skill's name and description directly in the tool's
// description so the model sees the full roster without an extra list call —
// matching how Anthropic/Claude expose skills in their function schema. Skills
// flagged subagent:true are annotated, since invoking one is a long-running,
// blocking spawn-and-await rather than a simple inline load.
func systemSkillsToolDef(skills []Skill) Tool {
	var sb strings.Builder
	for _, s := range skills {
		if s.RunsAsSubagent() {
			fmt.Fprintf(&sb, "  - %s: %s [runs in a background subagent]\n", s.Name, s.Description)
		} else {
			fmt.Fprintf(&sb, "  - %s: %s\n", s.Name, s.Description)
		}
	}
	desc := "Load a skill's full workflow instructions by name. Available skills:\n" + sb.String() +
		"Pass the skill name to receive its complete SKILL.md instructions plus its directory path, so you can follow the workflow and read any scripts/, references/, or assets/ it ships with file tools. Skills marked [runs in a background subagent] instead spawn that skill in a stealth subagent and BLOCK until it finishes, returning its report — invoke them when the work is long-running or should stay out of your context."
	return Tool{
		Type: "function",
		Function: ToolFunctionDef{
			Name:        "system-skills",
			Description: desc,
			Parameters: map[string]any{
				"type":     "object",
				"required": []string{"name"},
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Skill name to load (see this tool's description for the available list).",
					},
					"args": map[string]any{
						"type":        "string",
						"description": "Optional goal/context to pass to the skill, forwarded verbatim. Required in practice only for subagent-flagged skills, which otherwise run with no context.",
					},
				},
			},
		},
	}
}

// ReservedSlashCommands are built-in TUI command names a skill may never claim.
// Keep in sync with tui/commands.go's static registry (enforced by test).
var ReservedSlashCommands = []string{
	"context", "model", "reasoning", "session", "rerun", "config",
	"stealth", "subagents", "discover", "compact", "exit", "log",
	"reload-skills",
}

// AddSkills merges skills into the config's loaded set, ordered by name: an
// incoming skill REPLACES any existing skill with the same name (local layer
// shadowing global), appends when the name is new.
func (cfg *AppConfig) AddSkills(newSkills ...Skill) {
	if cfg == nil {
		return
	}
	for _, s := range newSkills {
		replaced := false
		for i := range cfg.Skills {
			if cfg.Skills[i].Name == s.Name {
				cfg.Skills[i] = s
				replaced = true
				break
			}
		}
		if !replaced {
			cfg.Skills = append(cfg.Skills, s)
		}
	}
}

// GetSkill returns a copy of the loaded skill with the given name, or nil.
func (cfg *AppConfig) GetSkill(name string) *Skill {
	if cfg == nil || name == "" {
		return nil
	}
	for i := range cfg.Skills {
		if cfg.Skills[i].Name == name {
			s := cfg.Skills[i]
			return &s
		}
	}
	return nil
}

// noteSkillDiag records one skill-loading diagnostic (malformed file, collision,
// reserved-name rejection) on the config for later UI flush AND logs it to the
// session log immediately — loading happens before any UI channel exists.
func (cfg *AppConfig) noteSkillDiag(level, msg string) {
	cfg.SkillDiagnostics = append(cfg.SkillDiagnostics, fmt.Sprintf("[%s] %s", level, msg))
	log.Printf("skills: %s %s", level, msg)
}

// loadSkillsDir scans one config layer's skills directory: every direct child
// must be a skill dir containing SKILL.md. Malformed entries are skipped with a
// warning; two skills in THIS layer claiming the same name reject all of them
// with errors (identity merge across layers happens later in AddSkills).
func loadSkillsDir(dir string, cfg *AppConfig) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // no skills dir at this layer: fine
	}
	var loaded []Skill
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		skillDir := filepath.Join(dir, e.Name())
		mdPath := filepath.Join(skillDir, "SKILL.md")
		raw, err := os.ReadFile(mdPath)
		if err != nil {
			cfg.noteSkillDiag("warn", fmt.Sprintf("skill in directory %s is malformed (missing SKILL.md) and is not loaded", skillDir))
			continue
		}
		name, description, body, subagentFlag, ferr := parseSkillFrontmatter(string(raw))
		if ferr != nil {
			cfg.noteSkillDiag("warn", fmt.Sprintf("skill in directory %s is malformed (%s) and is not loaded", skillDir, ferr))
			continue
		}
		if abs, aerr := filepath.Abs(skillDir); aerr == nil {
			skillDir = abs
		}
		loaded = append(loaded, Skill{Name: name, Description: description, Dir: skillDir, Body: body, Subagent: subagentFlag})
	}
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].Name < loaded[j].Name })

	// Intra-layer collision pass: every name claimed by more than one skill in
	// this layer is rejected entirely (no winner among them); one error line per
	// colliding name, not one per duplicate entry.
	counts := make(map[string]int)
	for _, s := range loaded {
		counts[s.Name]++
	}
	reported := make(map[string]bool)
	var kept []Skill
	for _, s := range loaded {
		if counts[s.Name] > 1 {
			if !reported[s.Name] {
				reported[s.Name] = true
				cfg.noteSkillDiag("error", fmt.Sprintf("skill name collision in layer %s: %q claimed by %d skills; none of them is loaded", dir, s.Name, counts[s.Name]))
			}
			continue
		}
		kept = append(kept, s)
	}
	cfg.AddSkills(kept...)
}

// parseSkillFrontmatter splits a SKILL.md into its YAML frontmatter (name and
// description; both required, single-line or folded like >-) and the instruction
// body that follows it. Any structural violation is an error describing why.
func parseSkillFrontmatter(raw string) (name, description, body string, subagent *bool, err error) {
	text := strings.ReplaceAll(strings.TrimLeft(raw, "\ufeff"), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return "", "", "", nil, fmt.Errorf("missing YAML frontmatter header")
	}
	rest := text[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return "", "", "", nil, fmt.Errorf("unterminated YAML frontmatter block")
	}
	var fm struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Subagent    *bool  `yaml:"subagent"` // nil = key absent (default: subagent run)
	}
	if yerr := yaml.Unmarshal([]byte(rest[:end]), &fm); yerr != nil {
		return "", "", "", nil, fmt.Errorf("frontmatter is not valid YAML: %v", yerr)
	}
	if strings.TrimSpace(fm.Name) == "" {
		return "", "", "", nil, fmt.Errorf("frontmatter has no name")
	}
	if !ValidToolName(fm.Name) {
		return "", "", "", nil, fmt.Errorf("name %q is invalid: does not match the OpenAI tool-name schema ^[a-zA-Z0-9_-]{1,64}$", fm.Name)
	}
	if strings.TrimSpace(fm.Description) == "" {
		return "", "", "", nil, fmt.Errorf("frontmatter has no description")
	}
	body = rest[end+5:] // skip the "\n---\n" terminator
	return fm.Name, strings.TrimSpace(fm.Description), strings.TrimSpace(body), fm.Subagent, nil
}

// RunsAsSubagent reports how /<name> should execute this skill: false unless
// the frontmatter explicitly set subagent: true. Inline (default) runs in the
// caller's own context; a forced run spawns a background subagent to await.
func (s *Skill) RunsAsSubagent() bool {
	return s != nil && s.Subagent != nil && *s.Subagent
}

// SkillTaskPrompt is the self-contained task a skill's background subagent
// receives: it sees no conversation context, so it must read the SKILL.md
// itself and follow the workflow end-to-end, finishing with
// system-subagent_finalize. Lives in data so both launchers (the TUI slash
// path via agent.RunSkillTask and the model-invocation path via the
// system-skills tool) build an identical child task.
func (s *Skill) SkillTaskPrompt(userArgs string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "You are executing the %q skill.\n\n", s.Name)
	sb.WriteString("First read the full workflow instructions at ")
	fmt.Fprintf(&sb, "%s/SKILL.md with system-read and follow them exactly. The skill directory is %s — use it as the base path for any files the instructions reference (scripts/, references/, assets/).\n\n", s.Dir, s.Dir)
	if userArgs != "" {
		sb.WriteString("The user provided these arguments for this run:\n")
		sb.WriteString(userArgs)
		sb.WriteString("\n\nApply them to the workflow wherever relevant.\n")
	}
	sb.WriteString(`
When the workflow is complete, call system-subagent_finalize ONCE with a concise report of what you did and its outcome. If the skill's instructions specify an output format, follow that format in the finalize summary — the calling agent will parse your final message to deliver the result.`)
	return sb.String()
}

// SkillInlinePrompt is the prompt an INLINE (non-subagent) skill run injects
// into the invoking agent's own context: the full SKILL.md body plus any user
// args, framed so the model follows the workflow directly in its conversation.
func (s *Skill) SkillInlinePrompt(userArgs string) string {
	var sb strings.Builder
	sb.WriteString("You are executing the ")
	fmt.Fprintf(&sb, "%q skill. Follow these instructions exactly; the skill directory is %s — use it as the base path for any files the instructions reference (scripts/, references/, assets/).\n\n", s.Name, s.Dir)
	sb.WriteString(s.Body)
	if userArgs != "" {
		sb.WriteString("\n\nThe user provided these arguments for this run:\n")
		sb.WriteString(userArgs)
		sb.WriteString("\nApply them to the workflow wherever relevant.")
	}
	return sb.String()
}


// ReloadResult summarizes an in-place skill reload.
type ReloadResult struct {
	Added   []string
	Removed []string
	Updated []string
	Total   int
}

// loadSkillsLayer scans one config layer's skills/ directory and returns its
// surviving skills. Diagnostics (malformed files, intra-layer collisions) are
// appended to cfg. Reserved-name filtering is the caller's job.
func loadSkillsLayer(dir string, cfg *AppConfig) []Skill {
	entries, err := os.ReadDir(filepath.Join(dir, "skills"))
	if err != nil {
		return nil
	}
	var loaded []Skill
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		skillDir := filepath.Join(dir, "skills", e.Name())
		mdPath := filepath.Join(skillDir, "SKILL.md")
		raw, err := os.ReadFile(mdPath)
		if err != nil {
			cfg.noteSkillDiag("warn", fmt.Sprintf("skill in directory %s is malformed (missing SKILL.md) and is not loaded", skillDir))
			continue
		}
		name, description, body, subagentFlag, ferr := parseSkillFrontmatter(string(raw))
		if ferr != nil {
			cfg.noteSkillDiag("warn", fmt.Sprintf("skill in directory %s is malformed (%s) and is not loaded", skillDir, ferr))
			continue
		}
		if abs, aerr := filepath.Abs(skillDir); aerr == nil {
			skillDir = abs
		}
		loaded = append(loaded, Skill{Name: name, Description: description, Dir: skillDir, Body: body, Subagent: subagentFlag})
	}
	counts := make(map[string]int)
	for _, s := range loaded {
		counts[s.Name]++
	}
	reported := make(map[string]bool)
	var kept []Skill
	for _, s := range loaded {
		if counts[s.Name] > 1 {
			if !reported[s.Name] {
				reported[s.Name] = true
				cfg.noteSkillDiag("error", fmt.Sprintf("skill name collision in layer %s: %q claimed by %d skills; none of them is loaded", dir, s.Name, counts[s.Name]))
			}
			continue
		}
		kept = append(kept, s)
	}
	return kept
}

// ReloadSkills re-scans both config layers and replaces the skill set on cfg.
// It resets SkillDiagnostics (diagnostics are from this reload), rebuilds the
// injected system-skills tool from the new set, and returns the diff. Reserved
// built-in names are rejected (with a diagnostic) and local layers shadow
// global layers by identity.
func (cfg *AppConfig) ReloadSkills() *ReloadResult {
	if cfg == nil {
		return nil
	}
	oldByName := make(map[string]Skill, len(cfg.Skills))
	for _, s := range cfg.Skills {
		oldByName[s.Name] = s
	}

	cfg.SkillDiagnostics = nil
	global := loadSkillsLayer(GlobalConfigDir(), cfg)
	local := loadSkillsLayer(".clanker", cfg)

	byName := make(map[string]Skill, len(global)+len(local))
	for _, s := range global {
		byName[s.Name] = s
	}
	for _, s := range local {
		byName[s.Name] = s
	}

	var final []Skill
	for name, s := range byName {
		if reservedSlashCommand(name) {
			cfg.noteSkillDiag("error", fmt.Sprintf("skill %q is rejected: it collides with the built-in /%s command and is not loaded", name, name))
			continue
		}
		final = append(final, s)
	}
	sort.Slice(final, func(i, j int) bool { return final[i].Name < final[j].Name })

	res := &ReloadResult{}
	newByName := make(map[string]Skill, len(final))
	for _, s := range final {
		newByName[s.Name] = s
		old, ok := oldByName[s.Name]
		if !ok {
			res.Added = append(res.Added, s.Name)
			continue
		}
		if old.Body != s.Body || old.Description != s.Description || subagentChanged(old, s) {
			res.Updated = append(res.Updated, s.Name)
		}
	}
	for name := range oldByName {
		if _, ok := newByName[name]; !ok {
			res.Removed = append(res.Removed, name)
		}
	}
	sort.Strings(res.Added)
	sort.Strings(res.Removed)
	sort.Strings(res.Updated)
	res.Total = len(final)

	cfg.Skills = final

	filtered := make([]Tool, 0, len(cfg.Tools))
	for _, t := range cfg.Tools {
		if t.Function.Name == "system-skills" {
			continue
		}
		filtered = append(filtered, t)
	}
	cfg.Tools = filtered
	if len(final) > 0 {
		cfg.AddTools(systemSkillsToolDef(final))
	}

	return res
}

func subagentChanged(old, new Skill) bool {
	if old.Subagent == nil && new.Subagent == nil {
		return false
	}
	if old.Subagent == nil || new.Subagent == nil {
		return true
	}
	return *old.Subagent != *new.Subagent
}

// LoadSkillFromDisk re-reads <skill.Dir>/SKILL.md and returns a fresh Skill.
// On error the original (stale) skill is returned along with the error.
func LoadSkillFromDisk(skill *Skill) (*Skill, error) {
	if skill == nil {
		return nil, fmt.Errorf("skill is nil")
	}
	raw, err := os.ReadFile(filepath.Join(skill.Dir, "SKILL.md"))
	if err != nil {
		return skill, err
	}
	name, description, body, subagentFlag, ferr := parseSkillFrontmatter(string(raw))
	if ferr != nil {
		return skill, ferr
	}
	return &Skill{Name: name, Description: description, Dir: skill.Dir, Body: body, Subagent: subagentFlag}, nil
}
