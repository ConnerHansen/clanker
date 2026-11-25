package agent

import (
	"clankerv2/data"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFileForTest(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// systemContentOf returns the content of the first message in payload and a
// flag for whether it is a system block; non-system heads fail the test.
func systemContentOf(t *testing.T, payload map[string]any) (string, bool) {
	t.Helper()
	msgs, ok := payload["messages"].([]map[string]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf("payload messages missing or empty: %v", payload["messages"])
	}
	if msgs[0]["role"] != "system" {
		return "", false
	}
	content, _ := msgs[0]["content"].(string)
	return content, true
}

func TestRunPayloadSystemBlockResolution(t *testing.T) {
	prevLocal, prevGlobal, prevProject := data.LocalSystemMDForTest(), data.GlobalSystemMDForTest(), data.LocalProjectMDForTest()
	local := t.TempDir() + "/.clanker/system.md"
	global := t.TempDir() + "/system.md"
	if err := writeFileForTest(local, "file fallback prompt"); err != nil {
		t.Fatal(err)
	}
	data.SetSystemMDPathsForTest(local, global, t.TempDir()+"/missing-project.md")
	defer data.SetSystemMDPathsForTest(prevLocal, prevGlobal, prevProject)

	userMsg := map[string]any{"role": "user", "content": "hello"}

	// Every payload now carries the runtime Environment block (cwd, git
	// branch, session) appended after the base system content, so these
	// assertions check the base prompt as a prefix rather than exact equality.
	rt := &AgentRuntime{SystemPrompt: "explicit prompt", Messages: []map[string]any{userMsg}}
	p, sys := systemContentOf(t, assembleRunToolResponsePayload(rt))
	if !sys || !strings.HasPrefix(p, "explicit prompt\n\n# Environment") {
		t.Fatalf("explicit SystemPrompt lost: got %q, system=%v", p, sys)
	}

	// PromptContext appends its prompts after the file layer.
	rt = &AgentRuntime{
		PromptContext: &data.Context{Prompts: []data.Prompt{{Name: "extra", Content: "appended prompt"}}},
		Messages:      []map[string]any{userMsg},
	}
	p, sys = systemContentOf(t, assembleRunToolResponsePayload(rt))
	if !sys || !strings.HasPrefix(p, "file fallback prompt\nappended prompt\n\n# Environment") {
		t.Fatalf("context FullPrompt mismatch: got %q, system=%v", p, sys)
	}

	// Neither set -> effective system file (local wins over global here).
	rt = &AgentRuntime{Messages: []map[string]any{userMsg}}
	p, sys = systemContentOf(t, assembleRunToolResponsePayload(rt))
	if !sys || !strings.HasPrefix(p, "file fallback prompt\n\n# Environment") {
		t.Fatalf("file fallback missing from bare runtime payload: got %q, system=%v", p, sys)
	}

	// Neither set and no files at all -> the system block is ONLY the
	// Environment block (there is always at least cwd + a session line).
	data.SetSystemMDPathsForTest(t.TempDir()+"/missing-local.md", t.TempDir()+"/missing-global.md", t.TempDir()+"/missing-project.md")
	rt = &AgentRuntime{Messages: []map[string]any{userMsg}}
	p, sys = systemContentOf(t, assembleRunToolResponsePayload(rt))
	if !sys || !strings.HasPrefix(p, "# Environment") {
		t.Fatalf("expected only the Environment block when no file layer exists: got %q, system=%v", p, sys)
	}

	// Local project.md is appended after the effective base (local wins over
	// global here).
	projectMD := t.TempDir() + "/.clanker/project.md"
	if err := writeFileForTest(projectMD, "project extras"); err != nil {
		t.Fatal(err)
	}
	data.SetSystemMDPathsForTest(local, t.TempDir()+"/missing-global.md", projectMD)
	rt = &AgentRuntime{Messages: []map[string]any{userMsg}}
	p, sys = systemContentOf(t, assembleRunToolResponsePayload(rt))
	if !sys || !strings.HasPrefix(p, "file fallback prompt\nproject extras\n\n# Environment") {
		t.Fatalf("project.md append missing from bare runtime payload: got %q, system=%v", p, sys)
	}

	// Local missing -> global file is the floor.
	globalOnly := t.TempDir() + "/system.md"
	if err := writeFileForTest(globalOnly, "global floor prompt"); err != nil {
		t.Fatal(err)
	}
	data.SetSystemMDPathsForTest(t.TempDir()+"/missing-local.md", globalOnly, t.TempDir()+"/missing-project.md")
	rt = &AgentRuntime{Messages: []map[string]any{userMsg}}
	p, sys = systemContentOf(t, assembleRunToolResponsePayload(rt))
	if !sys || !strings.HasPrefix(p, "global floor prompt\n\n# Environment") {
		t.Fatalf("global fallback mismatch: got %q, system=%v", p, sys)
	}
}
