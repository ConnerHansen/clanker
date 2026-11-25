package agent

import (
	"clankerv2/data"
	"context"
	"fmt"
	"log"
	"strings"
)

// RunSkillTask runs a subagent-flagged skill: spawn a stealth subagent on the
// given model to execute the SKILL.md workflow, then hand the MAIN agent a
// follow-up turn that collects the subagent's result (system-await_subagent)
// and processes it.
//
// It RETURNS IMMEDIATELY — both phases run on a background goroutine streaming
// through resp, exactly like RunDiscoverTask. The returned CancelFunc stops the
// main-agent part; if fired before that turn has started (during spawn), it is
// deferred and takes effect as soon as the turn begins. Spawned subagents are
// never auto-killed by cancellation — they finish on their own (/subagents
// lists them).
func RunSkillTask(resp chan any, skill *data.Skill, userArgs, model string) context.CancelFunc {
	cancelCh := make(chan context.CancelFunc, 1)
	go func() {
		var cf context.CancelFunc
		id, spawnModel, err := skillSpawner("main", model, buildSkillSpawnArgs(skill, userArgs))
		if err != nil {
			consoleError(resp, fmt.Sprintf("/%s: failed to launch skill subagent: %v", skill.Name, err))
			cf = func() {}
			cancelCh <- cf
			return
		}
		log.Printf("skill %q launched as subagent %s (model=%s)", skill.Name, id, spawnModel)
		resp <- data.Response{
			Type:    "console",
			Message: fmt.Sprintf("/%s: running skill in subagent %s on %s — the main agent will process its result when it finishes.", skill.Name, id, spawnModel),
		}
		cf = skillMainTaskFn(resp, buildSkillMergePrompt(skill, id, userArgs), model)
		cancelCh <- cf
	}()

	return func() {
		go func() {
			if cf, ok := <-cancelCh; ok && cf != nil {
				cf()
			}
		}()
	}
}

// buildSkillSpawnArgs assembles the spawn arguments for a skill's subagent:
// stealth mode and the FULL tool set (no allowlist) so skills can use any file,
// search, or command-backed tool their workflow needs. The task text is shared
// with the model-invocation path via data.Skill.SkillTaskPrompt.
func buildSkillSpawnArgs(skill *data.Skill, userArgs string) map[string]any {
	return map[string]any{
		"task": skill.SkillTaskPrompt(userArgs),
		"name": skill.Name,
		"mode": "stealth",
	}
}

// skillSpawner is the seam tests use to observe (or stub) the spawn call.
// Production behavior: register under caller "main" so the main agent's
// system-await_subagent tool can collect the result.
var skillSpawner = func(callerID, model string, args map[string]any) (string, string, error) {
	return SpawnSubagent(callerID, model, args)
}

// skillMainTaskFn is the seam that hands the collection+processing task to the
// main agent. Production behavior is exactly one interactive turn; tests
// replace it to capture the task without running a live model stream.
var skillMainTaskFn = func(channel chan any, task, model string) context.CancelFunc {
	return PromptWithContext(nil, channel, task, model)
}

// buildSkillMergePrompt is the task handed to the MAIN agent after spawning:
// await the skill subagent and process its returned result for the user.
func buildSkillMergePrompt(skill *data.Skill, id, userArgs string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "The %q skill was just launched in background subagent %s", skill.Name, id)
	if userArgs != "" {
		sb.WriteString(" with the user's arguments:\n")
		sb.WriteString(userArgs)
	}
	sb.WriteString(`

Do the following:
1. Collect its result with system-await_subagent (pass the id; poll with system-subagent_status and await again while it reports status "pending"). If it fails or times out, report that to the user and stop — do not retry automatically.
2. Process the subagent's final report according to what the user asked: present its result faithfully (formatting it for readability where helpful), and take any follow-up actions the user explicitly requested beyond running the skill itself.`)
	return sb.String()
}

// ReloadSkills re-scans both config layers on the live AppConfig, refreshes
// the main runtime's tool set with the new roster, and returns the diff.
// Diagnostics are reset and re-emitted; the caller (TUI) flushes them.
func ReloadSkills() *data.ReloadResult {
	cfg := GetAppConfig()
	if cfg == nil {
		return nil
	}
	res := cfg.ReloadSkills()
	// Refresh the main runtime's effective tool set in place so the current
	// session sees the new roster (HasTool reads rt.Tools; DefaultTools
	// copies cfg.Tools fresh each call).
	rt := MainRuntime()
	if rt != nil {
		rt.Tools = DefaultTools()
	}
	return res
}
