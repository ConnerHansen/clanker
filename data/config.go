package data

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ContainerSettings defines settings for running a model in a container
type ContainerSettings struct {
	Image          string            `json:"image"`                     // The container image to use (e.g., ghcr.io/...)
	Args           []string          `json:"args,omitempty"`            // Additional podman run flags (--memory 16g, etc.)
	GPUBackend     string            `json:"gpu_backend,omitempty"`     // "cuda" or "vulkan"
	VolumeMappings map[string]string `json:"volume_mappings,omitempty"` // HostPath -> ContainerPath
}

// ModelConfig defines a model available in the system, driven by config.json
type ModelConfig struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	Type      string             `json:"type"`       // "chat", "ner", "embeddings"
	Managed   bool               `json:"managed"`    // true if Clanker manages the process
	Executor  string             `json:"executor"`   // "ollama" or "llama.cpp"
	Command   string             `json:"command"`    // binary to invoke
	Params    []string           `json:"params"`     // arguments to pass
	URL       string             `json:"url"`        // Base URL for persistent/remote models
	PortFlag  string             `json:"port_flag"`  // Flag to set the port (e.g. "--port")
	PortRange string             `json:"port_range"` // Range for random port (e.g. "8000-9000")
	Headers   map[string]string  `json:"headers"`    // Headers with ${VAR} templates
	Container *ContainerSettings `json:"container,omitempty"`
	// Reasoning configures the model's thinking effort. Absent means the
	// model has no configurable reasoning: payloads omit reasoning_effort
	// (provider default) and the UI offers no selector.
	Reasoning *ReasoningConfig `json:"reasoning,omitempty"`
}

// ReasoningConfig holds a model's configurable thinking-effort levels. Every
// such model implicitly inherits a leading "none" option that disables
// thinking entirely; Default names the level used when the session has not
// chosen one (empty = none). Enabled gates whether the level may be changed:
// false means /reasoning reports the model as unconfigurable and every call
// sends "none".
type ReasoningConfig struct {
	Options []string `json:"options"` // supported effort levels, in display order
	Default string   `json:"default"` // level used when none is selected (empty = none)
	Enabled bool     `json:"enabled"` // false = reasoning cannot be configured
}

// ReasoningIsConfigurable reports whether the model exposes a changeable
// reasoning level: present and enabled. Absent or disabled blocks are not.
func (m *ModelConfig) ReasoningIsConfigurable() bool {
	return m.Reasoning != nil && m.Reasoning.Enabled
}

// ReasoningSelectableLevels returns the model's selectable effort levels with
// "none" forced to the front (deduplicated against the configured options).
// Without a reasoning block it returns ["none"] only.
func (m *ModelConfig) ReasoningSelectableLevels() []string {
	opts := []string{"none"}
	for _, o := range m.ReasoningOptions() {
		if o != "none" && !containsString(opts, o) {
			opts = append(opts, o)
		}
	}
	return opts
}

// ReasoningDefaultLevel returns the model's default effort level: an empty
// default (or a nil block) means none.
func (m *ModelConfig) ReasoningDefaultLevel() string {
	if m.Reasoning == nil || m.Reasoning.Default == "" {
		return "none"
	}
	return m.Reasoning.Default
}

// ReasoningOptions returns the configured option list; nil when absent.
func (m *ModelConfig) ReasoningOptions() []string {
	if m.Reasoning == nil {
		return nil
	}
	return m.Reasoning.Options
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ContainsString reports whether s is present in list.
func ContainsString(list []string, s string) bool { return containsString(list, s) }

// ToolRun is a tool's invocation spec: the command binary plus its argument
// list (argv, executed directly without a shell). A $NAME / ${NAME} entry in
// params expands to the named tool argument at call time: array values expand
// element-by-element, strings whitespace-split (a parameter literally named
// `script` substitutes verbatim as one argv entry); when no value is passed an
// exact-placeholder entry is dropped. See tools.resolveRun.
// TimeoutSeconds caps one execution (0 = the default CommandTimeout).
type ToolRun struct {
	Command        string   `json:"command"`
	Params         []string `json:"params,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
}

// ToolFunctionDef is one tool's function schema: what the model sees in its
// tool list, plus (optionally) how the tool executes when called. Named so
// every construction site shares one definition instead of duplicating an
// anonymous struct.
type ToolFunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Executor    string         `json:"executor,omitempty"`
}

type Tool struct {
	Type     string          `json:"type"`
	Function ToolFunctionDef `json:"function"`
	Run      *ToolRun        `json:"run,omitempty"` // how to execute the tool (nil = not command-backed)
	// RequestPermission opts a command-backed tool into a permission prompt
	// before every execution: the UI shows "Tool <name> requests permission to
	// run `<full assembled command>`" (final argv, parameters already expanded).
	// Absent/false runs unflagged — only system.* tools and explicitly flagged
	// configs gate on user approval.
	RequestPermission bool `json:"requestPermission,omitempty"`
}

// SubagentProfile is a named SUBAGENT PROFILE: a reusable launch preset
// referenced by system-spawn_subagent's "profile" argument. availableTools
// restricts the child's tool set (empty = inherit everything); prompt becomes
// the child's system instructions; model/deadlineSeconds are launch defaults
// (empty model inherits the calling agent's model, which implicitly reuses its
// running executor). Explicit spawn arguments override every profile field.
type SubagentProfile struct {
	Name            string   `json:"name,omitempty"`
	AvailableTools  []string `json:"availableTools"`
	Prompt          string   `json:"prompt,omitempty"`
	Model           string   `json:"model,omitempty"`           // "" = inherit caller's model
	DeadlineSeconds int      `json:"deadlineSeconds,omitempty"` // 0 = launch-time default
}

// ReadSettings holds the system-read tool's preferences from the config's
// "system.read" block. A nil Numbered (key absent) means default-on: numbering
// only turns off when explicitly set to false.
type ReadSettings struct {
	Numbered *bool `json:"numbered,omitempty"` // prefix every returned line with its absolute 1-based line number
}

// SystemSettings holds application-wide system preferences
type SystemSettings struct {
	DefaultModel      string `json:"defaultModel"`      // Model ID selected on application start-up
	DefaultSummarizer string `json:"defaultSummarizer"` // Model ID used for session summaries (upcoming feature)
	// DefaultDiscoverySubagent names the subagents profile (from AppConfig.Subagents) that
	// /discover launches its chunk-mapping subagents with: its tools, prompt, model and
	// deadline act as the launch preset; empty fields fall back to built-in discover defaults.
	DefaultDiscoverySubagent string `json:"defaultDiscoverySubagent"`
	// DiscoverFilesPerChunk caps how many files a single /discover subagent chunk may
	// contain. Default 100. The chunking logic allows up to 50% over this limit when
	// keeping a directory whole avoids splitting it across multiple agents.
	DiscoverFilesPerChunk int `json:"discoverFilesPerChunk,omitempty"`
	// DiscoverMaxAgents caps how many /discover subagents may run concurrently.
	// Default 4 (overrides the global FIFO cap of 2 for discover work).
	DiscoverMaxAgents int `json:"discoverMaxAgents,omitempty"`
	// DiscoverMaxDepth limits directory traversal depth during /discover.
	// Default 32. Prevents runaway scans on deeply nested trees or symlink loops.
	DiscoverMaxDepth int `json:"discoverMaxDepth,omitempty"`
	// SubagentMaxManaged caps concurrent subagents running on managed (local)
	// models. Default 1 — protects the main agent's context window.
	SubagentMaxManaged int `json:"subagentMaxManaged,omitempty"`
	// SubagentMaxUnmanaged caps concurrent subagents running on unmanaged
	// (remote/cloud) models. Default 5.
	SubagentMaxUnmanaged int           `json:"subagentMaxUnmanaged,omitempty"`
	Read                 *ReadSettings `json:"read,omitempty"` // system-read tool behavior (nil = all defaults)
}

// AppConfig is the merged application config. system/subagents come from
// config.json (a local top-level key replaces the whole global section; absent
// sections inherit); models and tools live in their own dedicated files
// (models.json, tools.json, tools/**/tool.json) and are identity-merged across
// layers — see AddModels/AddTools. Global layer: CLANKER_HOME|~/.clanker.
type AppConfig struct {
	Models    []ModelConfig              `json:"-"`
	Tools     []Tool                     `json:"-"`
	Skills    []Skill                    `json:"-"` // OpenAI-style skills from <layer>/skills (see skills.go)
	// SkillDiagnostics collects human-readable skill-loading issues (warnings for
	// malformed files, errors for collisions) at config load: the TUI flushes them
	// to its console at start-up because loading precedes any UI channel.
	SkillDiagnostics []string             `json:"-"`
	System           *SystemSettings      `json:"system,omitempty"`
	Subagents map[string]SubagentProfile `json:"subagents"` // named subagent profiles (launch presets)
}

// AddModels merges model configs by IDENTITY: an incoming model REPLACES any
// existing model with the same id (whole definition, not field-wise), appends
// when the id is new.
func (cfg *AppConfig) AddModels(newModels ...ModelConfig) {
	if cfg == nil {
		return
	}
	for _, m := range newModels {
		replaced := false
		for i := range cfg.Models {
			if cfg.Models[i].ID == m.ID {
				cfg.Models[i] = m
				replaced = true
				break
			}
		}
		if !replaced {
			cfg.Models = append(cfg.Models, m)
		}
	}
}

// toolNamePattern is the OpenAI function-name constraint every tool and skill
// name must satisfy to be loadable.
var toolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// ValidToolName reports whether name conforms to the OpenAI tool-name schema
// (^[a-zA-Z0-9_-]{1,64}$). Non-conforming tools are rejected at load.
func ValidToolName(name string) bool {
	return toolNamePattern.MatchString(name)
}

// AddTools merges tools into the configured set, ordered by name: an incoming
// tool REPLACES any existing tool with the same name, appends when it is new.
// Tools whose names violate the OpenAI schema are skipped with a warning.
func (cfg *AppConfig) AddTools(newTools ...Tool) {
	if cfg == nil {
		return
	}
	for _, t := range newTools {
		if !ValidToolName(t.Function.Name) {
			log.Printf("tools: skipping %q — name does not match the OpenAI tool-name schema ^[a-zA-Z0-9_-]{1,64}$", t.Function.Name)
			continue
		}
		replaced := false
		for i := range cfg.Tools {
			if cfg.Tools[i].Function.Name == t.Function.Name {
				cfg.Tools[i] = t
				replaced = true
				break
			}
		}
		if !replaced {
			cfg.Tools = append(cfg.Tools, t)
		}
	}
}

// ToolByName returns a copy of the tool with the given name, or nil.
func (cfg *AppConfig) ToolByName(name string) *Tool {
	if cfg == nil || name == "" {
		return nil
	}
	for i := range cfg.Tools {
		t := cfg.Tools[i]
		if t.Function.Name == name {
			return &t
		}
	}
	return nil
}

type Job struct {
	ID         int64     `db:"id"`
	InsertedAt time.Time `db:"inserted_at"`
	InputPath  string    `db:"input"`
}

// GlobalConfigDir returns the global Clanker config directory. The CLANKER_HOME
// environment variable takes precedence; otherwise ~/.clanker.
func GlobalConfigDir() string {
	if dir := os.Getenv("CLANKER_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".clanker")
}

// LoadLayeredConfig builds the application config from two layers, global first:
//
//   - global: <CLANKER_HOME|~/.clanker>/{config.json, models.json, tools.json, tools/**/tool.json, skills/**}
//   - local:  .clanker/{config.json, models.json, tools.json, tools/**/tool.json, skills/**} (cwd)
//
// For config.json the merge is SHALLOW and section-level: a top-level key present
// in the local file replaces the whole global section; absent sections inherit.
// Models and tools come only from their dedicated files — each layer's sources
// are merged in that order, identity-colliding entries (model id / tool name)
// later (local) replacing earlier ones (global). system.md is NOT handled here:
// data.Context.FullPrompt assembles it per context (effective system.md
// plus the local .clanker/project.md appended).
//
// Missing files are fine — every layer is optional. Malformed JSON is fatal:
// a broken config should fail loudly at boot-up rather than load half-applied.
func LoadLayeredConfig() *AppConfig {
	global := loadLayer(GlobalConfigDir())
	local := loadLayer(".clanker")

	merged := &AppConfig{
		Models:           global.Models,
		System:           global.System,
		Subagents:        global.Subagents,
		Tools:            global.Tools,
		Skills:           global.Skills,
		SkillDiagnostics: global.SkillDiagnostics,
	}
	if local.System != nil {
		merged.System = local.System
	}
	if local.Subagents != nil {
		merged.Subagents = local.Subagents
	}
	merged.AddModels(local.Models...)
	merged.AddTools(local.Tools...)
	// Local skills shadow globals by name (identity merge, like tools/models).
	merged.AddSkills(local.Skills...)
	merged.SkillDiagnostics = append(merged.SkillDiagnostics, local.SkillDiagnostics...)

	// Skills merged by identity (AddSkills): a local skill with the same name
	// SHADOWS the global one. Reserved built-in command names are rejected on
	// the final set, and system-skills is appended to the tool list when any
	// skill survived so every agent runtime can discover and load them.
	var survivors []Skill
	for _, s := range merged.Skills {
		if reservedSlashCommand(s.Name) {
			merged.noteSkillDiag("error", fmt.Sprintf("skill %q is rejected: it collides with the built-in /%s command and is not loaded", s.Name, s.Name))
			continue
		}
		survivors = append(survivors, s)
	}
	merged.Skills = survivors
	if len(merged.Skills) > 0 {
		merged.AddTools(systemSkillsToolDef(merged.Skills))
	}
	return merged
}

// loadLayer reads one config root (global home dir or project .clanker). A
// missing directory yields an empty layer. Within a layer, models.json and
// tools.json are bare arrays; tools/**/tool.json adds further tools in sorted
// walk order. AddModels/AddTools dedupe by identity, so later files at the same
// level override earlier ones.
func loadLayer(dir string) *AppConfig {
	cfg := &AppConfig{}
	if _, err := os.Stat(dir); err != nil {
		return cfg
	}

	readJSON := func(path string, v any) {
		b, err := os.ReadFile(path)
		if err != nil {
			return // missing file = empty piece; only present-but-broken files matter
		}
		if err := json.Unmarshal(b, v); err != nil {
			log.Fatalf("failed to parse %s: %v", path, err)
		}
	}

	readJSON(filepath.Join(dir, "config.json"), cfg)

	// models.json is a BARE ARRAY of model definitions (no wrapping key).
	var modelsFile []ModelConfig
	readJSON(filepath.Join(dir, "models.json"), &modelsFile)
	cfg.AddModels(modelsFile...)

	// tools.json is a BARE ARRAY of tool definitions (no wrapping key).
	var toolsFile []Tool
	readJSON(filepath.Join(dir, "tools.json"), &toolsFile)
	cfg.AddTools(toolsFile...)

	walkToolsDir(filepath.Join(dir, "tools"), cfg)
	loadSkillsDir(filepath.Join(dir, "skills"), cfg)
	return cfg
}

// reservedSlashCommand reports whether a skill name would shadow a built-in TUI
// slash command. Names are compared case-insensitively (command dispatch is).
func reservedSlashCommand(name string) bool {
	lower := strings.ToLower(name)
	for _, r := range ReservedSlashCommands {
		if lower == r {
			return true
		}
	}
	return false
}

// walkToolsDir collects every <dir>/**/tool.json: each file is ONE tool (the
// OpenAI function schema plus a "run" block). Sorted by path for determinism.
func walkToolsDir(dir string, cfg *AppConfig) {
	var paths []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip
		}
		if !d.IsDir() && d.Name() == "tool.json" {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var t Tool
		if err := json.Unmarshal(b, &t); err != nil {
			log.Fatalf("failed to parse tool definition %s: %v", p, err)
		}
		cfg.AddTools(t)
	}
}

// ParseConfig unmarshals one config.json document (system/subagents — models
// and tools are no longer part of it; they live in models.json / tools.json /
// tools/**). Tests use it for isolated fixtures; boot goes through
// LoadLayeredConfig.
func ParseConfig(data []byte) (*AppConfig, error) {
	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// GetDefaultModel returns the configured default model ID ("" when unset).
// It is nil-safe: callers may hold a config without a system block.
func (cfg *AppConfig) GetDefaultModel() string {
	if cfg == nil || cfg.System == nil {
		return ""
	}
	return cfg.System.DefaultModel
}

// GetDefaultSummarizer returns the model ID used to generate session
// summaries ("" when unset; the summary engine falls back to DefaultModel).
func (cfg *AppConfig) GetDefaultSummarizer() string {
	if cfg == nil || cfg.System == nil {
		return ""
	}
	return cfg.System.DefaultSummarizer
}

// GetDefaultDiscoverySubagent returns the name of the subagent profile that
// /discover uses as its launch preset ("" when unset = built-in defaults).
func (cfg *AppConfig) GetDefaultDiscoverySubagent() string {
	if cfg == nil || cfg.System == nil {
		return ""
	}
	return cfg.System.DefaultDiscoverySubagent
}

// GetReadNumbered reports whether the system-read tool should prefix lines with their
// absolute line numbers. Defaults to true when the config, its system block,
// the read block, or the key is absent — numbering only turns off explicitly.
func (cfg *AppConfig) GetReadNumbered() bool {
	if cfg == nil || cfg.System == nil || cfg.System.Read == nil || cfg.System.Read.Numbered == nil {
		return true
	}
	return *cfg.System.Read.Numbered
}

// GetDiscoverFilesPerChunk returns the max files per /discover chunk.
// Default 100 when unset; values ≤ 0 are treated as unset.
func (cfg *AppConfig) GetDiscoverFilesPerChunk() int {
	if cfg == nil || cfg.System == nil || cfg.System.DiscoverFilesPerChunk <= 0 {
		return 100
	}
	return cfg.System.DiscoverFilesPerChunk
}

// GetDiscoverMaxAgents returns the max concurrent /discover subagents.
// Default 4 when unset; values ≤ 0 are treated as unset.
func (cfg *AppConfig) GetDiscoverMaxAgents() int {
	if cfg == nil || cfg.System == nil || cfg.System.DiscoverMaxAgents <= 0 {
		return 4
	}
	return cfg.System.DiscoverMaxAgents
}

// GetDiscoverMaxDepth returns the max directory depth for /discover scanning.
// Default 32 when unset; values ≤ 0 are treated as unset.
func (cfg *AppConfig) GetDiscoverMaxDepth() int {
	if cfg == nil || cfg.System == nil || cfg.System.DiscoverMaxDepth <= 0 {
		return 32
	}
	return cfg.System.DiscoverMaxDepth
}

// GetSubagentMaxManaged returns the max concurrent managed (local) subagents.
// Default 1 when unset; values ≤ 0 are treated as unset.
func (cfg *AppConfig) GetSubagentMaxManaged() int {
	if cfg == nil || cfg.System == nil || cfg.System.SubagentMaxManaged <= 0 {
		return 1
	}
	return cfg.System.SubagentMaxManaged
}

// GetSubagentMaxUnmanaged returns the max concurrent unmanaged (remote) subagents.
// Default 5 when unset; values ≤ 0 are treated as unset.
func (cfg *AppConfig) GetSubagentMaxUnmanaged() int {
	if cfg == nil || cfg.System == nil || cfg.System.SubagentMaxUnmanaged <= 0 {
		return 5
	}
	return cfg.System.SubagentMaxUnmanaged
}

// GetModelsByExecutor returns all model configs filtered by executor type
func (cfg *AppConfig) GetModelsByExecutor(executor string) []ModelConfig {
	var result []ModelConfig
	for _, m := range cfg.Models {
		if m.Executor == executor {
			result = append(result, m)
		}
	}
	return result
}

// GetModelByID returns a single model config by its ID, or nil if not found
func (cfg *AppConfig) GetModelByID(id string) *ModelConfig {
	for _, m := range cfg.Models {
		if m.ID == id {
			return &m
		}
	}
	return nil
}

// GetToolRun returns the invocation spec for a tool by name, or nil when the
// config is nil, the tool is absent, or it carries no run block at all.
func (cfg *AppConfig) GetToolRun(name string) *ToolRun {
	t := cfg.ToolByName(name)
	if t == nil || t.Run == nil || t.Run.Command == "" {
		return nil
	}
	run := *t.Run
	return &run
}

// GetSubagentProfile returns a subagent profile by name, or nil when unknown.
func (cfg *AppConfig) GetSubagentProfile(name string) *SubagentProfile {
	if cfg == nil || name == "" {
		return nil
	}
	w, ok := cfg.Subagents[name]
	if !ok {
		return nil
	}
	return &w
}

// ToolRequiresPermission reports whether a tool's config flagged requestPermission.
// Nil-safe; absent tools return false (only explicit opt-ins gate).
func (cfg *AppConfig) ToolRequiresPermission(name string) bool {
	t := cfg.ToolByName(name)
	return t != nil && t.RequestPermission
}
