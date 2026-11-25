package data

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ProjectMap markers delimit the auto-generated project-structure section of
// .clanker/project.md. /discover (agent.RunDiscoverTask) replaces ONLY the text
// between them, so hand-written knowledge in the rest of the file survives
// re-runs.
const (
	ProjectMapStartMarker = "<!-- project-map:start -->"
	ProjectMapEndMarker   = "<!-- project-map:end -->"
)

type Context struct {
	ID         int64     `db:"id" json:"id"`
	Name       string    `db:"name" json:"name"`
	Prompts    []Prompt  `db:"-" json:"prompts"`
	Thinking   bool      `db:"thinking" json:"thinking"`
	InsertedAt time.Time `db:"inserted_at" json:"insertedAt"`
	UpdatedAt  time.Time `db:"updated_at" json:"updatedAt"`
}

type Prompt struct {
	ID         int64     `db:"id" json:"id"`
	Name       string    `db:"name" json:"name"`
	Content    string    `db:"content" json:"content"`
	InsertedAt time.Time `db:"inserted_at" json:"insertedAt"`
	UpdatedAt  time.Time `db:"updated_at" json:"updatedAt"`
}

// localSystemMD, globalSystemMD and localProjectMD are the layers of the
// system prompt. Tests may repoint them at fixtures; boot uses the defaults
// below (the global layer honoring CLANKER_HOME via GlobalConfigDir).
var (
	localSystemMD  = ".clanker/system.md"
	globalSystemMD = func() string {
		d := GlobalConfigDir()
		if d == "" {
			return ""
		}
		return filepath.Join(d, "system.md")
	}()
	localProjectMD = ".clanker/project.md"
)

// readLayer returns the trimmed content of one prompt-layer file, or "" when it
// is missing/unreadable. A present-but-unreadable file is logged.
func readLayer(path string) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		log.Printf("System prompt layer %q unreadable: %v", path, err)
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SystemPromptFile returns the EFFECTIVE base system prompt path: the local
// project file when it exists, else the global one. A missing local file falls
// back to global; a present one REPLACES global entirely (no concatenation).
func SystemPromptFile() string {
	if _, err := os.Stat(localSystemMD); err == nil {
		return localSystemMD
	}
	return globalSystemMD
}

// SystemPromptContent returns the assembled base system prompt: the effective
// system.md (see SystemPromptFile: local project file wins, then global) with
// the local .clanker/project.md APPENDED when present. It returns "" when no
// layer exists; a present-but-unreadable file is logged and treated as missing.
func SystemPromptContent() string {
	var layers []string
	if base := readLayer(SystemPromptFile()); base != "" {
		layers = append(layers, base)
	}
	if extra := readLayer(localProjectMD); extra != "" {
		layers = append(layers, extra)
	}
	return strings.Join(layers, "\n")
}

// FullPrompt joins the effective system prompt content (see
// SystemPromptContent) with the context's attached prompts. The base layer is
// the effective system.md plus any local project.md; context prompts always
// APPEND after it. A missing file or an empty result contributes nothing, so
// an empty context yields "".
func (c *Context) FullPrompt() string {
	contents := make([]string, 0)

	if sp := SystemPromptContent(); sp != "" {
		contents = append(contents, sp)
	}

	for _, p := range c.Prompts {
		contents = append(contents, p.Content)
	}

	return strings.Join(contents, "\n")
}
