package agent

import (
	"clankerv2/data"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBuildSkillMergePrompt(t *testing.T) {
	p := buildSkillMergePrompt(&data.Skill{Name: "alpha"}, "sub-7", "")
	if !strings.Contains(p, "sub-7") {
		t.Error("merge prompt must name the subagent id to await")
	}
	if !strings.Contains(p, "system-await_subagent") {
		t.Error("merge prompt must instruct awaiting the subagent")
	}
}

// TestRunSkillTaskSeams stubs both spawn seams so the whole pipeline runs
// without a live model: the spawner records its args, the main-task fn records
// the task it would hand the agent.
func TestRunSkillTaskSeams(t *testing.T) {
	skill := &data.Skill{Name: "alpha", Dir: "/s"}

	var spawnArgs map[string]any
	mainDone := make(chan struct{}, 1)
	origSpawner, origMainFn := skillSpawner, skillMainTaskFn
	skillSpawner = func(callerID, model string, args map[string]any) (string, string, error) {
		spawnArgs = args
		return "sub-9", model, nil
	}
	var mainTask string
	skillMainTaskFn = func(ch chan any, task, model string) context.CancelFunc {
		mainTask = task
		mainDone <- struct{}{}
		return func() {}
	}
	t.Cleanup(func() { skillSpawner, skillMainTaskFn = origSpawner, origMainFn })

	resp := make(chan any, 8)
	RunSkillTask(resp, skill, "my args", "model-x")

	select {
	case <-mainDone:
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline never reached the main-task phase")
	}

	if spawnArgs == nil {
		t.Fatal("spawner was never called")
	}
	if task, _ := spawnArgs["task"].(string); !strings.Contains(task, "/s/SKILL.md") {
		t.Errorf("spawn task should reference the skill dir: %q", task)
	}
	if mode, _ := spawnArgs["mode"].(string); mode != "stealth" {
		t.Errorf("skill subagent should be stealth, got %q", mode)
	}
	if !strings.Contains(mainTask, "sub-9") {
		t.Errorf("merge prompt should await the spawned id: %q", mainTask)
	}
}

// TestRunSkillTaskSpawnError surfaces a spawn failure as a console error and an
// inert cancel func.
func TestRunSkillTaskSpawnError(t *testing.T) {
	skill := &data.Skill{Name: "alpha", Dir: "/s"}
	origSpawner, origMainFn := skillSpawner, skillMainTaskFn
	skillSpawner = func(callerID, model string, args map[string]any) (string, string, error) {
		return "", "", errors.New("err-spawn-boom")
	}
	mainCalled := make(chan struct{}, 1)
	skillMainTaskFn = func(ch chan any, task, model string) context.CancelFunc {
		mainCalled <- struct{}{}
		return func() {}
	}
	t.Cleanup(func() { skillSpawner, skillMainTaskFn = origSpawner, origMainFn })

	resp := make(chan any, 8)
	cancelFn := RunSkillTask(resp, skill, "", "m")

	select {
	case ev := <-resp:
		r, ok := ev.(data.Response)
		if !ok || r.Type != "console" || !strings.Contains(r.Message, "err-spawn-boom") {
			t.Fatalf("expected console error about the spawn failure, got %#v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected a console event on spawn failure")
	}

	cancelFn() // must not block or panic when the run already failed
	select {
	case <-mainCalled:
		t.Error("main task must not run after a spawn error")
	default:
	}
}
