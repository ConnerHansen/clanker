package agent

import (
	"clankerv2/agent/structs"
	"clankerv2/agent/tools"
	"clankerv2/data"
	"clankerv2/memory"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type AgentEngine interface {
	processLine(line *string) engineResponse

	Init()
	ExtractEmbeddings(ctx context.Context, prompt string) ([]float32, error)
	ExtractNER(ctx context.Context, prompt string) (memory.ExtractionResult, error)
	// Stream runs the full turn loop against a provisioned endpoint and returns
	// the run's FINAL assistant text ("" when none); one-shot and subagent runs deliver it.
	Stream(ctx context.Context, rt *AgentRuntime, url string, headers map[string]string, payload map[string]any, channel chan any) (string, error)
}

type ActiveExecutor struct {
	ModelID       string
	Process       *os.Process
	ContainerName string
	Port          int
	StartTime     time.Time
	// Torn down only when its last owner releases, so one model can be shared
	// across several slots without killing the server mid-stream.
	RefCount int
	Owners   map[string]bool
	// Every slot name pointing at this executor; cleaned up on teardown/eviction so no slot names a dead instance.
	Aliases map[string]bool
	// Marks the chat model's server: its last release keeps it warm (zero owners)
	// so the next prompt skips a model load; only eviction or Cleanup() kills it.
	Persistent bool
}

// Process-wide agent state; per-conversation state lives in AgentRuntime so
// concurrent runs never share history.
type agentConfig struct {
	engine          AgentEngine
	appConfig       *data.AppConfig
	mainRuntime     *AgentRuntime // nil until first use
	mu              sync.Mutex    // guards mainRuntime
	subagents       map[string]data.SubagentProfile
	executorMu      sync.RWMutex // guards ActiveExecutors + OrphanedExecutors (sweep goroutine and prompts race on it)
	ActiveExecutors map[string]*ActiveExecutor // Key is the model 'type' (chat, ner, embeddings)
	// In-progress boots keyed by model ID; concurrent runs for the same model wait
	// on this instead of starting a duplicate server (the starter settles it once).
	StartingExecutors map[string]*pendingLaunch
	// Executors evicted from their slot while still owned (model switched mid-run):
	// unreachable via the slot map but in-flight streams still use them, so they must
	// outlive the eviction and be reaped by the last owner's release.
	OrphanedExecutors map[*ActiveExecutor]bool
}

var config = &agentConfig{
	ActiveExecutors:   make(map[string]*ActiveExecutor),
	StartingExecutors: make(map[string]*pendingLaunch),
	OrphanedExecutors: make(map[*ActiveExecutor]bool),
}

func SetAppConfig(cfg *data.AppConfig) {
	config.appConfig = cfg
	if cfg != nil {
		config.subagents = cfg.Subagents
		// Command tools read their run blocks from this config each call.
		tools.SetCommandSource(cfg.GetToolRun)
		// requestPermission-flagged tools gate on user approval before execution.
		tools.SetPermissionGated(cfg.ToolRequiresPermission)
		// Numbered reads default on; an explicit system-read block can switch them off.
		tools.SetReadNumbered(cfg.GetReadNumbered())
		// system-skills reads the skill set from this config each call (nil = none).
		tools.SetSkillSource(func() []data.Skill { return cfg.Skills })
	}
}

func GetAppConfig() *data.AppConfig {
	return config.appConfig
}

func SetStealth(stealth bool) {
	MainRuntime().Stealth = stealth
	log.Printf("Stealth mode set to: %v", stealth)
}

func GetStealth() bool {
	return MainRuntime().Stealth
}

func MainRuntimePendingUserMessages() int {
	return MainRuntime().PendingUserMessages()
}

// Queues user text picked up by the in-flight run at its next cycle boundary.
func EnqueueMainUserMessage(text string) bool {
	return MainRuntime().EnqueueUserMessage(text)
}

// Context window tokens for the main runtime; always normalizes to a supported option.
func GetContextSize() int {
	return data.NormalizeContextSize(MainRuntime().ContextSize)
}

// Sets the main runtime's context window immediately; persisted only for a real
// (non-stealth) session. Returns whether it was persisted.
func SetSessionContextSize(tokens int) (bool, error) {
	rt := MainRuntime()
	rt.ContextSize = data.NormalizeContextSize(tokens)

	s := rt.Session
	if s == nil || s.ID <= 0 {
		return false, nil
	}

	if err := data.SetContextSize(s.ID, tokens); err != nil {
		return false, err
	}
	s.ContextSize = rt.ContextSize
	log.Printf("Session %d context size set to %s", s.ID, data.FormatContextSize(rt.ContextSize))
	return true, nil
}

// GetReasoningLevelForModel returns a model's effective reasoning level for the
// main runtime's stored choice (the choice, or the model default when unset),
// or "" when the model has no reasoning block. Callers pass the LIVE selected
// model (not the runtime's ModelID, which is only set at prompt time) so a /model
// switch is reflected immediately.
func GetReasoningLevelForModel(modelID string) string {
	return ReasoningLevelForModel(modelID, MainRuntime().ReasoningEffort)
}

// ReasoningOptionsForModel returns a model's selectable reasoning levels (none
// first) or nil when the model has no configurable reasoning block — the web
// selector renders nothing in that case.
func ReasoningOptionsForModel(modelID string) []string {
	cfg := GetAppConfig()
	if cfg == nil {
		return nil
	}
	mc := cfg.GetModelByID(modelID)
	if mc == nil || !mc.ReasoningIsConfigurable() {
		return nil
	}
	return mc.ReasoningSelectableLevels()
}

// ReasoningLevelForModel resolves a model's effective level for an explicitly
// chosen value (the choice, else the model default), or "" when the model has
// no reasoning block.
func ReasoningLevelForModel(modelID, chosen string) string {
	cfg := GetAppConfig()
	if cfg == nil {
		return ""
	}
	mc := cfg.GetModelByID(modelID)
	lvl, ok := resolveReasoningForModel(mc, chosen)
	if !ok {
		return ""
	}
	return lvl
}

// pendingReasoning holds a reasoning level chosen out-of-band (web model switch
// or selector) that the next PromptWithContext applies to the main runtime,
// where it is validated against the selected model and persisted. Main-runtime
// writes are direct; this only bridges UI choices made before a run.
var (
	pendingReasoning   string
	pendingReasoningMu sync.Mutex
)

// SetPendingReasoning records an out-of-band level for the next prompt to apply.
func SetPendingReasoning(level string) {
	pendingReasoningMu.Lock()
	pendingReasoning = level
	pendingReasoningMu.Unlock()
}

func takePendingReasoning() string {
	pendingReasoningMu.Lock()
	v := pendingReasoning
	pendingReasoning = ""
	pendingReasoningMu.Unlock()
	return v
}

// SetReasoningEffort sets the main runtime's reasoning level (validated against
// modelID's block) and persists it for a real (non-stealth) session ONLY when it
// differs from that model's default (otherwise the override is cleared). Returns
// whether it was persisted.
func SetReasoningEffort(modelID, level string) (bool, error) {
	cfg := GetAppConfig()
	rt := MainRuntime()
	if cfg == nil {
		return false, fmt.Errorf("no config")
	}
	mc := cfg.GetModelByID(modelID)
	if mc == nil || !mc.ReasoningIsConfigurable() {
		return false, fmt.Errorf("reasoning cannot be configured for this model")
	}

	// Validate against the selectable set so stale levels from another model
	// never persist.
	if !data.ContainsString(mc.ReasoningSelectableLevels(), level) {
		return false, fmt.Errorf("reasoning level %q is not supported by this model", level)
	}

	rt.ReasoningEffort = level
	pendingReasoningMu.Lock()
	pendingReasoning = ""
	pendingReasoningMu.Unlock()
	s := rt.Session
	if s == nil || s.ID <= 0 {
		return false, nil
	}

	stored := ""
	if level != mc.ReasoningDefaultLevel() {
		stored = level // only record non-default overrides; default inherits
	}
	if err := data.SetReasoningEffort(s.ID, stored); err != nil {
		return false, err
	}
	s.ReasoningEffort = stored
	log.Printf("Session %d reasoning effort set to %s", s.ID, level)
	return true, nil
}

// Runs one interactive turn on the MAIN runtime; resp receives all run events
// plus a final {"eof"} sentinel, and the returned CancelFunc stops early.
func PromptWithContext(activeContext *data.Context, resp chan any, prompt string, model string) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	go func(ctx context.Context) {
		log.Println("Running agent using " + model)
		log.Println(prompt)

		rt := MainRuntime()
		if activeContext != nil {
			rt.PromptContext = activeContext
		}
		rt.ModelID = model

		// Apply a reasoning level chosen out-of-band (web) now that the model is
		// known: validated + persisted against it, then cleared.
		if pending := takePendingReasoning(); pending != "" {
			_, _ = SetReasoningEffort(model, pending)
		}

		// A run ending early must not leave interjections to fire at the next prompt.
		defer rt.DiscardQueuedMessages()

		// Mark busy for the whole run: thinking + pending system-ask forms write no
		// DB messages, so recency alone would let the summary engine sweep a live
		// conversation.
		var sessionID int64
		if rt.Session != nil {
			sessionID = rt.Session.ID
		}
		BeginSessionRun(sessionID)
		defer EndSessionRun(sessionID)

		_, err := RunAgent(ctx, rt, prompt, resp)
		if err != nil {
			log.Println(err)
		}

		respondState(resp, "idle")
		resp <- data.Response{Type: "eof", Message: ""}
		log.Println("Done with agent")
	}(ctx)

	return cancel
}

// Single query flow shared by every run kind: engine selection, executor
// provisioning/join, and driving the stream/tool loop against rt so concurrent
// runs never share state. Returns the FINAL assistant response (interactive
// callers ignore it; one-shot/subagent runs deliver it).
func RunAgent(ctx context.Context, rt *AgentRuntime, prompt string, channel chan any) (string, error) {
	engine := GetEngineForModel(rt.ModelID)
	if engine == nil {
		log.Printf("No suitable engine found for model %q", rt.ModelID)
		return "", fmt.Errorf("no engine configured for model %s", rt.ModelID)
	}

	rt.ModelID = modelIDForRun(rt)

	// Provision before touching history: failures happen with no user message persisted.
	url, headers, err := EnsureExecutorForRun(ctx, rt, channel)
	if err != nil {
		return "", err
	}
	defer ReleaseRunExecutors(rt)

	payload := assembleRunPayload(rt, prompt)
	return engine.Stream(ctx, rt, url, headers, payload, channel)
}

func GetEngineForModel(modelID string) AgentEngine {
	cfg := GetAppConfig()
	if cfg == nil {
		return nil
	}
	modelCfg := cfg.GetModelByID(modelID)
	if modelCfg == nil {
		return nil
	}

	switch modelCfg.Executor {
	case "cloud":
		return &Cloud{}
	case "ollama":
		return &Ollama{}
	case "openai":
		return &OpenAIEngine{}
	case "llama.cpp":
		return &LlamaCpp{}
	default:
		log.Printf("Unknown executor %q for model %s", modelCfg.Executor, modelID)
		return nil
	}
}

func GetEngine() AgentEngine {
	return config.engine
}

func SetEngine(engine AgentEngine) {
	config.engine = engine
}

func SetSession(session *data.Session, sessionMessages []data.Message) {
	MainRuntime().SetSession(session, sessionMessages)
}

func SetPromptContext(context *data.Context) {
	MainRuntime().PromptContext = context
}

func AppendMessage(source string, model string, content string) {
	MainRuntime().AppendMessage(source, model, content)
}

func AppendAgentMessage(model string, content string, toolCalls []structs.ToolCallResult) {
	MainRuntime().AppendAgentMessage(model, content, toolCalls)
}

// Overrides the subagent profile set (presets for spawn_subagent's "profile").
func SetSubagents(subagents map[string]data.SubagentProfile) {
	config.subagents = subagents
}

// Blocks on the shared prompt registry until the UI resolves. Level derives
// from action: create=info, delete=danger, else warning. viewport carries the
// optional display payload (diff or assembled command); nil prompts with no
// content pane.
func RequestPermission(channel chan any, tool, path, action, prompt string, viewport *data.PermissionViewport) string {
	id := nextPromptID(PromptPermission)

	level := "warning"
	switch action {
	case "create":
		level = "info"
	case "delete":
		level = "danger"
	}

	if prompt == "" {
		prompt = fmt.Sprintf("%s %s", strings.ToUpper(action)+":", path)
	}

	resultCh := presentPrompt(PromptPermission, id, channel, data.PermissionRequest{
		ID:        id,
		Tool:      tool,
		Path:      path,
		Action:    action,
		Prompt:    prompt,
		Level:     level,
		Viewport:  viewport,
		Timestamp: time.Now(),
	})

	allowed, _ := (<-resultCh).(bool)
	respondState(channel, "executing")
	if allowed {
		return "Operation approved by the user"
	}
	return "Permission denied by the user"
}

// In-flight run count per session; the summary engine consults IsSessionBusy to
// leave live conversations alone (pending calls/forms write no DB rows).
var activeRuns = make(map[int64]int)
var runMu sync.Mutex

// Marks one in-flight run; IDs <= 0 (stealth/dummy) are ignored — no row to protect.
func BeginSessionRun(sessionID int64) {
	if sessionID <= 0 {
		return
	}
	runMu.Lock()
	activeRuns[sessionID]++
	runMu.Unlock()
}

// Clears one in-flight run; safe to call more times than Begin (clamps at zero)
// so cancel paths can't leak a permanent busy state.
func EndSessionRun(sessionID int64) {
	if sessionID <= 0 {
		return
	}
	runMu.Lock()
	defer runMu.Unlock()
	activeRuns[sessionID]--
	if activeRuns[sessionID] <= 0 {
		delete(activeRuns, sessionID)
	}
}

func IsSessionBusy(sessionID int64) bool {
	if sessionID <= 0 {
		return false
	}
	runMu.Lock()
	defer runMu.Unlock()
	return activeRuns[sessionID] > 0
}

// Tears down every managed executor still alive at shutdown (active + orphans).
func Cleanup() {
	// Kill subagent runs first so they release executor ownership before teardown.
	killAllSubagents("app shutdown")

	// Snapshot under a read lock so teardown doesn't hold the map lock for the whole cleanup.
	config.executorMu.RLock()
	snapshot := make(map[string]*ActiveExecutor, len(config.ActiveExecutors))
	for k, e := range config.ActiveExecutors {
		snapshot[k] = e
	}
	orphans := make([]*ActiveExecutor, 0, len(config.OrphanedExecutors))
	for e := range config.OrphanedExecutors {
		orphans = append(orphans, e)
	}
	config.executorMu.RUnlock()

	for modelType, executor := range snapshot {
		if executor.Process != nil {
			log.Printf("Shutting down managed executor for %s (%s)...", modelType, executor.ModelID)
			if err := executor.Process.Kill(); err != nil {
				log.Printf("Error killing process for %s: %v", modelType, err)
			}
		} else if executor.ContainerName != "" {
			log.Printf("Shutting down containerized executor for %s (%s)...", modelType, executor.ModelID)
			stopCmd := exec.Command("podman", "rm", "-f", executor.ContainerName)
			if err := stopCmd.Run(); err != nil {
				log.Printf("Error removing container %s: %v", executor.ContainerName, err)
			}
		}
	}

	for _, e := range orphans {
		stopManagedExecutor(e)
	}
	if len(orphans) > 0 {
		log.Printf("Reaped %d orphaned executor(s) at shutdown", len(orphans))
	}
}
