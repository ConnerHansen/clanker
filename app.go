package main

import (
	"clankerv2/agent"
	"clankerv2/data"
	"clankerv2/repl"
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ConfigPath is the PROJECT-LOCAL config document (see data.LoadLayeredConfig,
// which also reads the global layer at CLANKER_HOME|~/.clanker).
const ConfigPath = ".clanker/config.json"

type AgentConfig struct {
	responseChan chan any
	cancelFn     context.CancelFunc
}

type AppConfig struct {
	ActiveContext *data.Context
	Contexts      []data.Context
}

// App struct
type App struct {
	agentConfig *AgentConfig
	appConfig   *AppConfig
	ctx         context.Context
	session     *repl.ShellSession

	// runMu guards runActive: the JS bridge can call Prompt concurrently.
	runMu     sync.Mutex
	runActive bool // true while a run is in flight; new prompts enqueue instead of starting concurrent runs
}

// NewApp creates a new App application struct
func NewApp() *App {
	// Load the layered config: global (CLANKER_HOME|~/.clanker) first, then the
	// project-local .clanker/ layer clobbers/appends on top of it.
	appCfg := data.LoadLayeredConfig()
	agent.SetAppConfig(appCfg)

	log.Printf("Loaded config with %d models, %d tools, %d skills", len(appCfg.Models), len(appCfg.Tools), len(appCfg.Skills))

	promptContexts, err := data.LoadContexts()
	if err != nil {
		fmt.Println(err)
	}

	// Initialize the default engine (llama.cpp for now)
	// You could make this configurable too
	engine := &agent.LlamaCpp{}
	agent.SetEngine(engine)
	engine.Init()

	agentConfig := AgentConfig{
		responseChan: make(chan any),
	}
	return &App{
		agentConfig: &agentConfig,
		appConfig: &AppConfig{
			ActiveContext: nil,
			Contexts:      promptContexts,
		},
	}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	session, err := repl.NewShellSession(ctx)
	if err != nil {
		fmt.Println("Shit suxxors")
		os.Exit(1)
	}

	a.session = session

	// Subagent lifecycle events (queued/running/completed/failed/killed/
	// timed_out) and blocking UI requests forwarded from subagents arrive here
	// instead of the agent's response channel, so they reach the frontend on
	// their own event channel without interleaving with the main stream.
	agent.SetUISink(func(ev any) {
		switch e := ev.(type) {
		case data.SubagentEvent:
			runtime.EventsEmit(ctx, "subagent-event", e)
		default:
			// Permission/ask requests a subagent raised: deliver through the
			// normal response pipeline so existing UI handlers resolve them.
			a.agentConfig.responseChan <- ev
		}
	})

	go func() {
		for {
			msg := <-a.agentConfig.responseChan
			switch v := msg.(type) {
			case data.Response:
				// Stamped events from a subagent's relay go to the frontend on
				// dedicated channels so they can never bleed into the main chat stream.
				if v.AgentID != "" {
					runtime.EventsEmit(ctx, "subagent-response", v)
					continue
				}
				switch v.Type {
				case "debug":
					fmt.Print(v.Message)
					runtime.EventsEmit(ctx, "agent-response", map[string]any{
						"thinking": true,
						"data":     v.Message,
						"eol":      false,
					})
				case "response":
					fmt.Print(v.Message)
					runtime.EventsEmit(ctx, "agent-response", map[string]any{
						"thinking": false,
						"data":     v.Message,
						"eol":      false,
					})
				case "compaction":
					// A context compaction registered: the seed summary (the framed
					// user message that re-seeds the session) reaches the frontend on
					// its own event so it renders live, like it does on reload.
					runtime.EventsEmit(ctx, "agent-compaction", map[string]any{"message": v.Message})
				default:
					runtime.EventsEmit(ctx, "agent-response", map[string]any{
						"eol": true,
					})
				}
			// Tool-call lifecycle events reach the web UI on their own channels;
			// ResultPreview on the summary carries the tool's self-summary.
			case data.ToolStart:
				if v.AgentID != "" {
					runtime.EventsEmit(ctx, "subagent-tool-start", v)
					continue
				}
				runtime.EventsEmit(ctx, "agent-tool-start", v)
			case data.ToolCallSummary:
				if v.AgentID != "" {
					runtime.EventsEmit(ctx, "subagent-tool-call", v)
					continue
				}
				runtime.EventsEmit(ctx, "agent-tool-call", v)
			case data.PermissionRequest:
				runtime.EventsEmit(ctx, "permission-request", v)
			case data.AskUserRequest:
				runtime.EventsEmit(ctx, "ask-user-request", v)
			// Explicit run-state transitions (posting/ingest/thinking/active/
			// parsing/executing/waiting/idle/error); the web status bar drives
			// off these instead of inferring state from response events.
			case data.StateMsg:
				runtime.EventsEmit(ctx, "agent-state", v)

			default:
				runtime.EventsEmit(ctx, "agent-response", map[string]any{
					"eol": true,
				})
			}
		}
	}()

	go func() {
		time.Sleep(3000 * time.Millisecond)
		a.session.SendCommand("ls")
	}()
}

// Allow interruptions
func (a *App) Interrupt() {
	fmt.Println("Cancel!")
	if a.agentConfig.cancelFn != nil {
		a.agentConfig.cancelFn()
	}
}

// ResolvePermissionRequest delivers the user's decision on a permission
// prompt to the blocked tool goroutine — the web-UI counterpart of the TUI's
// allow/deny keys. It runs through the same agent.ResolvePermission exit the
// TUI uses, so both interfaces can never double-resolve: first response wins.
func (a *App) ResolvePermissionRequest(id string, allowed bool) {
	agent.ResolvePermission(id, allowed)
}

// ResolveAskRequest submits the user's answers to a pending system-ask form.
// Answers maps question ID -> chosen option value or typed text (multi-select
// values are comma-joined by the caller, matching the TUI's collection).
func (a *App) ResolveAskRequest(id string, answers map[string]string) {
	agent.ResolveAsk(id, answers)
}

// CancelAskRequest dismisses a pending system-ask form without submitting; the
// blocked goroutine receives a cancelled result. Mirrors the TUI's esc path.
func (a *App) CancelAskRequest(id string) {
	agent.CancelAsk(id)
}

// Prompt starts an interactive agent run. While a run is in flight the text
// is QUEUED on the main runtime rather than starting a second concurrent run:
// the turn loop injects it at the next cycle boundary (interjection), and the
// statusbar tracks the queue via PendingInputCount.
func (a *App) Prompt(prompt string, model string) {
	a.runMu.Lock()
	active := a.runActive
	if !active {
		a.runActive = true
	}
	a.runMu.Unlock()

	if active {
		if agent.EnqueueMainUserMessage(prompt) {
			log.Printf("Prompt enqueued while run active (%d pending)", agent.MainRuntimePendingUserMessages())
		}

		return
	}

	defer func() {
		a.runMu.Lock()
		a.runActive = false
		a.runMu.Unlock()
	}()
	a.agentConfig.cancelFn = agent.PromptWithContext(
		a.appConfig.ActiveContext,
		a.agentConfig.responseChan,
		prompt,
		model,
	)
}

// PendingInputCount reports how many user messages are queued for the in-flight
// run (0 when none). The frontend polls it for the pending-input statusbar
// indicator; changes are rare (one per submit/drain), so polling avoids event
// plumbing, same as IsSummarizing.
func (a *App) PendingInputCount() int {
	return agent.MainRuntimePendingUserMessages()
}

func (a *App) ListAvailableModels() []string {
	cfg := agent.GetAppConfig()
	models := make([]string, 0, len(cfg.Models))
	for _, m := range cfg.Models {
		models = append(models, m.ID)
	}

	return models
}

// GetDefaultModel returns the configured default model ID from system.defaultModel.
// It is empty when the config has no system block or no default set; the frontend
// uses it to preselect the active model after loading the list of models.
func (a *App) GetDefaultModel() string {
	cfg := agent.GetAppConfig()
	if cfg == nil {
		return ""
	}
	return cfg.GetDefaultModel()
}

// GetReasoningOptions returns the selectable reasoning levels for a model
// (none first) or an empty slice when it has no configurable reasoning block;
// the frontend hides its Reasoning selector in that case.
func (a *App) GetReasoningOptions(modelID string) []string {
	opts := agent.ReasoningOptionsForModel(modelID)
	if opts == nil {
		return []string{}
	}
	return opts
}

// GetReasoningLevel returns a model's default reasoning level (chosen value
// empty) so the frontend can initialize its selector when a model loads.
func (a *App) GetReasoningLevel(modelID string) string {
	return agent.ReasoningLevelForModel(modelID, "")
}

// SetReasoningEffort records an out-of-band reasoning level for the next prompt
// to apply and persist against its model. Web sends it without setting the
// runtime's model (model travels per-prompt), so it is staged as pending.
func (a *App) SetReasoningEffort(level string) {
	agent.SetPendingReasoning(level)
}

// IsSummarizing reports whether a background summary sweep is in flight. The
// frontend polls it (changes are rare; polling avoids event plumbing) and the
// statusbar shows "summarizing..." while true.
func (a *App) IsSummarizing() bool {
	return agent.IsSummarizing()
}

// GetGitBranch returns the current git branch name, or "" when unavailable
// (not a repository / git missing); the frontend hides its pill on empty.
func (a *App) GetGitBranch() string {
	return data.GitBranch()
}

// GetCurrentDirectory returns the process's working directory for the
// statusbar; falls back to "" when it cannot be determined.
func (a *App) GetCurrentDirectory() string {
	return data.WorkingDirectory()
}

func (a *App) DeleteSession(session *data.Session) error {
	err := data.DeleteSession(session)
	if err != nil {
		fmt.Println("Error while deleting session", err)
	}

	return err
}

func (a *App) ListSessions() []data.Session {
	sessions, err := data.ListSessions()

	if err != nil {
		fmt.Println("Error listing sessions", err)
		return nil
	} else {
		return sessions
	}
}

func (a *App) CreateSession() (*data.Session, error) {
	session, err := data.NewSession("")
	if err != nil {
		fmt.Println("Error creating session: ", err)
		return nil, err
	}

	agent.SetSession(session, make([]data.Message, 0))
	return session, err
}

func (a *App) LoadSession(session *data.Session) ([]data.Message, error) {
	messages, err := data.LoadSession(session)
	if err != nil {
		fmt.Println("Error loading messages", err)
		return nil, err
	}
	agent.SetSession(session, messages)

	return messages, nil
}

func (a *App) ListSupportingContextFiles() map[string]any {
	fmt.Println("Got a ListSupportingContextFiles request")
	var filler map[string]any
	return filler
}

func (a *App) SaveContext(context *data.Context) *data.Context {
	context, err := data.SaveContext(context)
	if err != nil {
		fmt.Println("Error while saving context", err)
		return nil
	}

	return context
}

func (a *App) DeleteContext(context *data.Context) {
	data.DeleteContext(context)
}

func (a *App) ListContexts() []data.Context {
	contexts, err := data.LoadContexts()
	if err != nil {
		fmt.Println("Failed to load contexts", err)
	}

	return contexts
}

func (a *App) LoadContext(context *data.Context) *data.Context {
	context, err := data.LoadContext(context.ID)
	if err != nil {
		fmt.Println("Failed to load context", err)
	}

	return context
}

func (a *App) LoadPrompt(prompt *data.Prompt) *data.Prompt {
	prompt, err := data.LoadPrompt(prompt.ID)
	if err != nil {
		fmt.Println("Failed to load prompt", err)
	}

	return prompt
}

func (a *App) GetActiveContext() *data.Context {
	return a.appConfig.ActiveContext
}

func (a *App) SetActiveContext(context *data.Context) {
	fmt.Println("Active context set", context)
	prompts, _ := data.LoadContextPrompts(context)
	context.Prompts = prompts
	if context != nil {
		fmt.Println(context.FullPrompt())
	}
	a.appConfig.ActiveContext = context
}

func (a *App) SavePrompt(prompt *data.Prompt) *data.Prompt {
	fmt.Println("Saving prompt")
	prompt, err := data.SavePrompt(prompt)
	if err != nil {
		fmt.Println("Error while saving prompt", err)
		return nil
	}

	return prompt
}

func (a *App) DeletePrompt(prompt *data.Prompt) error {
	return data.DeletePrompt(prompt)
}

func (a *App) ListPrompts() []data.Prompt {
	prompts, err := data.LoadPrompts()
	if err != nil {
		fmt.Println("Error while loading prompts", err)
		return nil
	}

	return prompts
}
