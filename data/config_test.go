package data

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeFixtures writes files under root (creating directories as needed).
func writeFixtures(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
}

// withLayeredRoots points the layered loader at a temp global home and a temp
// project (cwd), restoring both on cleanup.
func withLayeredRoots(t *testing.T, globalHome, project string) {
	t.Helper()
	prevHome := os.Getenv("CLANKER_HOME")
	prevCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("CLANKER_HOME", globalHome); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Chdir(prevCwd)
		if prevHome == "" {
			os.Unsetenv("CLANKER_HOME")
		} else {
			os.Setenv("CLANKER_HOME", prevHome)
		}
	})
}

func TestGlobalConfigDirHonorsEnvVar(t *testing.T) {
	t.Setenv("CLANKER_HOME", "/tmp/custom-clanker")
	if got := GlobalConfigDir(); got != "/tmp/custom-clanker" {
		t.Fatalf("GlobalConfigDir() = %q, want /tmp/custom-clanker", got)
	}
}

func TestGlobalConfigDirDefaultsToHomeDotClanker(t *testing.T) {
	t.Setenv("CLANKER_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir in this environment")
	}
	want := filepath.Join(home, ".clanker")
	if got := GlobalConfigDir(); got != want {
		t.Fatalf("GlobalConfigDir() = %q, want %q", got, want)
	}
}

func TestLoadLayeredConfigAllMissing(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	withLayeredRoots(t, global, project)

	cfg := LoadLayeredConfig()
	if cfg == nil {
		t.Fatal("expected a non-nil config")
	}
	if len(cfg.Models) != 0 || len(cfg.Tools) != 0 || cfg.System != nil || len(cfg.Subagents) != 0 {
		t.Fatalf("expected empty merged config, got %+v", cfg)
	}
}

func TestLoadLayeredConfigLocalClobbersWholeSections(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	writeFixtures(t, global, map[string]string{
		"config.json": `{"system": {"defaultModel": "g-model"}, "subagents": {"a": {"prompt": "global a"}}}`,
		"models.json": `[{"id": "g-model", "name": "G"}]`,
	})
	writeFixtures(t, project, map[string]string{
		".clanker/config.json": `{"system": {"defaultModel": "l-model"}, "subagents": {"b": {"prompt": "local b"}}}`,
		".clanker/models.json": `[{"id": "l-model", "name": "L"}]`,
	})
	withLayeredRoots(t, global, project)

	cfg := LoadLayeredConfig()

	// Models are identity-merged: l-model is NEW (appended), g-model survives.
	if len(cfg.Models) != 2 {
		t.Fatalf("models = %+v, want both g-model and l-model", cfg.Models)
	}
	if cfg.System.DefaultModel != "l-model" {
		t.Fatalf("system.defaultModel = %q, want l-model", cfg.System.DefaultModel)
	}
	// Section-level: local subagents replace the map entirely.
	if _, ok := cfg.Subagents["a"]; ok {
		t.Fatal("global subagent 'a' should not survive a local subagents section")
	}
	if p, ok := cfg.Subagents["b"]; !ok || p.Prompt != "local b" {
		t.Fatalf("subagents = %+v, want only local 'b'", cfg.Subagents)
	}
}

func TestLoadLayeredConfigModelsMergeById(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	writeFixtures(t, global, map[string]string{
		"models.json": `[
			{"id": "shared", "name": "global shared", "type": "chat"},
			{"id": "global.only", "name": "g"}
		]`,
	})
	writeFixtures(t, project, map[string]string{
		".clanker/models.json": `[
			{"id": "shared", "name": "local shared", "type": "embeddings"},
			{"id": "local.only", "name": "l"}
		]`,
	})
	withLayeredRoots(t, global, project)

	cfg := LoadLayeredConfig()
	if len(cfg.Models) != 3 {
		t.Fatalf("models = %+v, want 3 entries (no duplicates)", cfg.Models)
	}
	sm := cfg.GetModelByID("shared")
	if sm == nil || sm.Name != "local shared" || sm.Type != "embeddings" {
		t.Fatalf("shared model = %+v, want the LOCAL definition to win wholesale", sm)
	}
	if g := cfg.GetModelByID("global.only"); g == nil {
		t.Fatal("global.only should survive")
	}
	if l := cfg.GetModelByID("local.only"); l == nil {
		t.Fatal("local.only should be appended")
	}
}

func TestLoadLayeredConfigInheritsMissingSections(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	writeFixtures(t, global, map[string]string{
		"config.json": `{"system": {"defaultModel": "g-model"}, "subagents": {"a": {"prompt": "global a"}}}`,
		"models.json": `[{"id": "g-model", "name": "G"}]`,
	})
	writeFixtures(t, project, map[string]string{
		".clanker/models.json": `[{"id": "l-model", "name": "L"}]`,
	})
	withLayeredRoots(t, global, project)

	cfg := LoadLayeredConfig()
	if len(cfg.Models) != 2 {
		t.Fatalf("models = %+v, want g-model inherited + l-model appended", cfg.Models)
	}
	if cfg.System == nil || cfg.System.DefaultModel != "g-model" {
		t.Fatalf("system should inherit from global, got %+v", cfg.System)
	}
	if p, ok := cfg.Subagents["a"]; !ok || p.Prompt != "global a" {
		t.Fatalf("subagents should inherit from global, got %+v", cfg.Subagents)
	}
}

func TestLoadLayeredConfigNoLocalDir(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	writeFixtures(t, global, map[string]string{
		"models.json": `[{"id": "g-model", "name": "G"}]`,
	})
	withLayeredRoots(t, global, project)

	cfg := LoadLayeredConfig()
	if len(cfg.Models) != 1 || cfg.Models[0].ID != "g-model" {
		t.Fatalf("models = %+v, want g-model inherited", cfg.Models)
	}
}

const toolJSONGlobal = `[
	{"type": "function", "function": {"name": "shared-tool", "description": "from global json"}},
	{"type": "function", "function": {"name": "global-only", "description": "global only"}}
]`

const toolJSONLocal = `[
	{"type": "function", "function": {"name": "shared-tool", "description": "from local json"}},
	{"type": "function", "function": {"name": "local-only", "description": "local only"}}
]`

func TestLoadLayeredConfigToolsDedupeAndOrder(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	writeFixtures(t, global, map[string]string{"tools.json": toolJSONGlobal})
	writeFixtures(t, project, map[string]string{".clanker/tools.json": toolJSONLocal})
	withLayeredRoots(t, global, project)

	cfg := LoadLayeredConfig()
	// global.only + local.only appended; shared-tool appears ONCE (local wins).
	if len(cfg.Tools) != 3 {
		t.Fatalf("tools = %d entries, want 3: %+v", len(cfg.Tools), cfg.Tools)
	}
	shared := cfg.ToolByName("shared-tool")
	if shared == nil || shared.Function.Description != "from local json" {
		t.Fatalf("shared-tool = %+v, want the LOCAL definition to win", shared)
	}
	if globalOnly := cfg.ToolByName("global-only"); globalOnly == nil {
		t.Fatal("global.only should survive")
	}
	if localOnly := cfg.ToolByName("local-only"); localOnly == nil {
		t.Fatal("local.only should be appended")
	}
}

func TestLoadLayeredConfigToolDirsReplaceByName(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	writeFixtures(t, global, map[string]string{
		"tools.json":                      toolJSONGlobal,
		"tools/extra/tool.json":           `{"type": "function", "function": {"name": "dir-global-extra", "description": "global dir"}}`,
		"tools/nested/deep/dir/tool.json": `{"type": "function", "function": {"name": "dir-nested", "description": "nested global dir"}}`,
	})
	writeFixtures(t, project, map[string]string{
		".clanker/tools/shared-tool/tool.json": `{
			"type": "function",
			"function": {"name": "shared-tool", "description": "local dir override"},
			"run": {"command": "python", "params": ["script.py"]}
		}`,
	})
	withLayeredRoots(t, global, project)

	cfg := LoadLayeredConfig()

	names := map[string]bool{}
	for _, t := range cfg.Tools {
		names[t.Function.Name] = true
	}
	for _, want := range []string{"shared-tool", "global-only", "dir-global-extra", "dir-nested"} {
		if !names[want] {
			t.Fatalf("missing tool %q in merged set: %v", want, names)
		}
	}
	if len(cfg.Tools) != 4 {
		t.Fatalf("tools = %d entries, want 4 (no duplicates)", len(cfg.Tools))
	}

	shared := cfg.ToolByName("shared-tool")
	if shared == nil || shared.Function.Description != "local dir override" {
		t.Fatalf("shared-tool = %+v, want local dir definition", shared)
	}
	if shared.Run == nil || shared.Run.Command != "python" || len(shared.Run.Params) != 1 {
		t.Fatalf("shared-tool run = %+v, want python [script.py]", shared.Run)
	}

	if r := cfg.GetToolRun("shared-tool"); r == nil || r.Command != "python" {
		t.Fatalf("GetToolRun(shared-tool) = %+v, want python", r)
	}
	if r := cfg.GetToolRun("global-only"); r != nil {
		t.Fatalf("GetToolRun(global.only) = %+v, want nil (no run block)", r)
	}
	if r := cfg.GetToolRun("missing"); r != nil {
		t.Fatalf("GetToolRun(missing) = %+v, want nil", r)
	}
}

func TestToolRequestPermissionFlag(t *testing.T) {
	cfg := &AppConfig{}
	cfg.AddTools(
		Tool{
			Type:              "function",
			Function:          ToolFunctionDef{Name: "dangerous-script", Description: "gated"},
			Run:               &ToolRun{Command: "python", Params: []string{"run.py"}},
			RequestPermission: true,
		},
		Tool{
			Type:     "function",
			Function: ToolFunctionDef{Name: "plain-script", Description: "not gated"},
			Run:      &ToolRun{Command: "python", Params: []string{"run.py"}},
		},
	)

	if !cfg.ToolRequiresPermission("dangerous-script") {
		t.Fatal("flagged tool should require permission")
	}
	if cfg.ToolRequiresPermission("plain-script") {
		t.Fatal("unflagged tool must not require permission")
	}
	if cfg.ToolRequiresPermission("missing") {
		t.Fatal("absent tools must not require permission")
	}

	// The flag is parsed from raw tool JSON.
	var tool Tool
	if err := json.Unmarshal([]byte(`{"type":"function","function":{"name":"x"},"run":{"command":"c"},"requestPermission":true}`), &tool); err != nil {
		t.Fatal(err)
	}
	if !tool.RequestPermission {
		t.Fatal("requestPermission flag did not parse")
	}

	nilCfg := (*AppConfig)(nil)
	if nilCfg.ToolRequiresPermission("x") {
		t.Fatal("nil config must be false")
	}
}

func TestParseConfigIgnoresModelsAndToolsKeys(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{"system": {"defaultModel": "m"}, "models": [{"id": "m"}], "tools": [{"type": "function", "function": {"name": "x"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.System == nil || cfg.System.DefaultModel != "m" {
		t.Fatalf("system = %+v", cfg.System)
	}
	if len(cfg.Models) != 0 {
		t.Fatalf("models should not be parsed from config.json, got %+v", cfg.Models)
	}
	if len(cfg.Tools) != 0 {
		t.Fatalf("tools should not be parsed from config.json, got %+v", cfg.Tools)
	}
}

func TestSystemPromptFileLocalOverridesGlobal(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	writeFixtures(t, global, map[string]string{"system.md": "global prompt"})
	writeFixtures(t, project, map[string]string{".clanker/system.md": "local prompt"})
	withLayeredRoots(t, global, project)

	prevLocal, prevGlobal := localSystemMD, globalSystemMD
	localSystemMD = ".clanker/system.md"
	globalSystemMD = filepath.Join(global, "system.md")
	t.Cleanup(func() { localSystemMD, globalSystemMD = prevLocal, prevGlobal })

	if got := SystemPromptFile(); got != ".clanker/system.md" {
		t.Fatalf("SystemPromptFile() = %q, want the local file", got)
	}

	// Local missing -> global.
	if err := os.Remove(filepath.Join(project, ".clanker/system.md")); err != nil {
		t.Fatal(err)
	}
	if got := SystemPromptFile(); got != filepath.Join(global, "system.md") {
		t.Fatalf("SystemPromptFile() = %q, want the global file", got)
	}

	c := &Context{}
	want := []byte("global prompt")
	got := c.FullPrompt()
	if string(got) != string(want) {
		t.Fatalf("FullPrompt() = %q, want %q", got, want)
	}
}
