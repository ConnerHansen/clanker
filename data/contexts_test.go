package data

import (
	"os"
	"path/filepath"
	"testing"
)

// withSystemMDFiles points the system prompt layers at fixture files. Keys:
// ".clanker/system.md" (local base), ".clanker/project.md" (local append layer)
// or anything else (global base); omitted keys point at nothing. Package vars
// are restored on cleanup.
func withSystemMDFiles(t *testing.T, paths map[string]string) {
	t.Helper()
	prevLocal, prevGlobal, prevProject := localSystemMD, globalSystemMD, localProjectMD
	localSystemMD = ""
	globalSystemMD = ""
	localProjectMD = ""
	for file, content := range paths {
		p := filepath.Join(t.TempDir(), file)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		switch file {
		case ".clanker/system.md":
			localSystemMD = p
		case ".clanker/project.md":
			localProjectMD = p
		default:
			globalSystemMD = p
		}
	}
	t.Cleanup(func() { localSystemMD, globalSystemMD, localProjectMD = prevLocal, prevGlobal, prevProject })
}

// withMissingSystemMDPaths points all three layers at nonexistent paths.
func withMissingSystemMDPaths(t *testing.T) {
	t.Helper()
	prevLocal, prevGlobal, prevProject := localSystemMD, globalSystemMD, localProjectMD
	localSystemMD = filepath.Join(t.TempDir(), "missing-local.md")
	globalSystemMD = filepath.Join(t.TempDir(), "missing-global.md")
	localProjectMD = filepath.Join(t.TempDir(), "missing-project.md")
	t.Cleanup(func() { localSystemMD, globalSystemMD, localProjectMD = prevLocal, prevGlobal, prevProject })
}

func TestSystemPromptContentLocalWins(t *testing.T) {
	withSystemMDFiles(t, map[string]string{
		".clanker/system.md": "local prompt",
		"global-system.md":   "global prompt",
	})

	if got := SystemPromptContent(); got != "local prompt" {
		t.Fatalf("SystemPromptContent() = %q, want the local file content", got)
	}
}

func TestSystemPromptContentGlobalFallback(t *testing.T) {
	withSystemMDFiles(t, map[string]string{"global-system.md": "global prompt"})

	if got := SystemPromptContent(); got != "global prompt" {
		t.Fatalf("SystemPromptContent() = %q, want the global file content", got)
	}
}

func TestSystemPromptContentAppendsProjectMD(t *testing.T) {
	withSystemMDFiles(t, map[string]string{
		"global-system.md":    "global prompt",
		".clanker/project.md": "project extras",
	})

	if got := SystemPromptContent(); got != "global prompt\nproject extras" {
		t.Fatalf("SystemPromptContent() = %q, want the global file with project.md appended", got)
	}
}

func TestSystemPromptContentLocalOverridePlusProject(t *testing.T) {
	withSystemMDFiles(t, map[string]string{
		".clanker/system.md":  "local prompt",
		"global-system.md":    "global prompt",
		".clanker/project.md": "project extras",
	})

	// Local system.md replaces global entirely; project.md still appends.
	want := "local prompt\nproject extras"
	if got := SystemPromptContent(); got != want {
		t.Fatalf("SystemPromptContent() = %q, want %q", got, want)
	}
}

func TestSystemPromptContentNeitherPresent(t *testing.T) {
	withMissingSystemMDPaths(t)

	if got := SystemPromptContent(); got != "" {
		t.Fatalf("SystemPromptContent() = %q, want empty when no file exists", got)
	}
}

func TestFullPromptAppendsContextPromptsAfterFile(t *testing.T) {
	withSystemMDFiles(t, map[string]string{".clanker/system.md": "file layer"})

	c := &Context{Prompts: []Prompt{{Name: "one", Content: "prompt one"}, {Name: "two", Content: "prompt two"}}}
	want := "file layer\nprompt one\nprompt two"
	if got := c.FullPrompt(); got != want {
		t.Fatalf("FullPrompt() = %q, want %q", got, want)
	}
}

func TestFullPromptEmptyContextYieldsEmpty(t *testing.T) {
	withMissingSystemMDPaths(t)

	if got := (&Context{}).FullPrompt(); got != "" {
		t.Fatalf("FullPrompt() = %q, want empty with no file and no prompts", got)
	}
}
