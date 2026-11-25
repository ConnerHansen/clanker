package tools

import (
	"clankerv2/agent/structs"
	"clankerv2/data"
	"fmt"
	"sort"
)

// handleSkills implements system-skills: execute one skill by name. INLINE
// skills (the default) return the SKILL.md body plus directory path and the
// model follows them directly in its own context. Skills flagged
// subagent:true are FORCED into a background stealth subagent: this handler
// spawns it and BLOCKS until it reaches a terminal state (like
// system-await_subagent), returning the child's final report as the tool
// result — deterministic, no model cooperation required. Forced skills can
// only be launched from the main agent: a subagent has no await tools and
// could never collect a grandchild it spawned.
func handleSkills(ctx *HandlerCtx, name string) structs.ToolCallResult {
	if skillSource == nil {
		return skillError(name, "no skills are loaded")
	}

	skillName, _ := ctx.args["name"].(string)
	if skillName == "" {
		return skillError(name, "'name' is required (see the tool description for available skills)")
	}

	var skill *data.Skill
	for _, s := range skillSource() {
		if s.Name == skillName {
			c := s
			skill = &c
			break
		}
	}
	if skill == nil {
		return skillError(name, fmt.Sprintf("unknown skill %q (available: %v)", skillName, availableSkillNames()))
	}

	// Live re-read: SKILL.md on disk is authoritative at invocation time.
	live, err := data.LoadSkillFromDisk(skill)
	if err != nil {
		return skillError(name, fmt.Sprintf("failed to read %s/SKILL.md: %v", skill.Dir, err))
	}
	skill = live

	args, _ := ctx.args["args"].(string)

	if !skill.RunsAsSubagent() {
		return structs.ToolCallResult{
			Success: true,
			Result: map[string]any{
				"skill":        skillName,
				"instructions": skill.Body,
				"path":         skill.Dir,
			},
			Summary: fmt.Sprintf("Loaded skill %s from %s", skillName, skill.Dir),
		}
	}

	// Subagent-forced path: main-agent only.
	if ctx.rt == nil || ctx.rt.RunID() != "main" {
		rid := "unknown"
		if ctx.rt != nil {
			rid = ctx.rt.RunID()
		}
		return skillError(name, fmt.Sprintf("skill %q runs in a background subagent and can only be invoked from the main agent (this run is %q)", skillName, rid))
	}
	if subagentBackend == nil {
		return skillError(name, "subagents are not available in this context")
	}

	id, err := subagentBackend.Spawn(ctx.rt.RunID(), ctx.rt.OwnerModel(), map[string]any{
		"task": skill.SkillTaskPrompt(args),
		"name": skill.Name,
		"mode": "stealth",
	})
	if err != nil {
		return skillError(name, fmt.Sprintf("failed to launch skill subagent: %v", err))
	}

	// Block until the child reaches a terminal state. Await(id, 0) has no
	// timeout (same as system-await_subagent with no timeoutSeconds); esc
	// cancels the run around this blocking call the same way it does there.
	report := subagentBackend.Await(id, 0)
	status, _ := report["status"].(string)

	res := make(map[string]any, len(report)+1)
	for k, v := range report {
		res[k] = v
	}
	res["skill"] = skillName
	return structs.ToolCallResult{
		Success: status == "completed",
		Result:  res,
		Summary: fmt.Sprintf("Ran skill %s in subagent %s: %s", skillName, id, status),
	}
}

func skillError(toolName, msg string) structs.ToolCallResult {
	return structs.ToolCallResult{Success: false, Result: map[string]any{"error": fmt.Sprintf("%s: %s", toolName, msg)}}
}

func availableSkillNames() []string {
	var names []string
	if skillSource != nil {
		for _, s := range skillSource() {
			names = append(names, s.Name)
		}
	}
	sort.Strings(names)
	return names
}

func init() { registerTool("system-skills", handleSkills) }
