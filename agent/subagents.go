package agent

import (
	"clankerv2/agent/tools"
	"clankerv2/data"
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"
)

// defaultSubagentDeadline is the timer applied when neither spec nor profile sets
// one. It starts at EXECUTION (dispatch), not queueing, so a queued subagent's
// deadline isn't eaten by waiting behind slower siblings.
var defaultSubagentDeadline = 10 * time.Minute

// SubagentMode selects whether a subagent gets its own persisted session or an
// ephemeral one.
type SubagentMode string

const (
	ModeStealth    SubagentMode = "stealth"    // dummy session (-1); nothing written to DB
	ModePersistent SubagentMode = "persistent" // real session row linked to its caller
)

// SubagentStatus is a subagent's lifecycle state.
type SubagentStatus string

const (
	StatusQueued    SubagentStatus = "queued"
	StatusRunning   SubagentStatus = "running"
	StatusCompleted SubagentStatus = "completed"
	StatusFailed    SubagentStatus = "failed"
	StatusKilled    SubagentStatus = "killed"
	StatusTimedOut  SubagentStatus = "timed_out"
)

// isTerminal reports whether a status will no longer change.
func (s SubagentStatus) isTerminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusKilled, StatusTimedOut:
		return true
	}
	return false
}

// SubagentSpec is the fully-resolved launch request for one subagent (profile +
// explicit args already merged by SpawnSubagent).
type SubagentSpec struct {
	Name         string
	Task         string
	ModelID      string // resolved: never empty at run time
	Managed      bool   // true if the model is locally managed (Clanker owns its process)
	Profile      string
	Tools        []data.Tool // effective, filtered tool set (finalize always included)
	Mode         SubagentMode
	SystemPrompt string
	Deadline     time.Duration
}

// SubagentResult is the final report a caller receives from await/status.
type SubagentResult struct {
	ID            string
	Name          string
	Status        SubagentStatus
	Output        string // finalize argument, or last assistant message as fallback
	Error         string
	LastActivity  string // what it was doing when it stopped (tool/fragment)
	PartialOutput string // bounded accumulated text (for death reports)
	DurationSec   float64
	SessionID     int64
}

// SubagentInfo is the registry record for one subagent. Fields are guarded by
// subagentsMu: Status, killRequested, runCtx/Cancel, done, LastActivity,
// PartialOutput, Result, StartedAt/EndedAt.
type SubagentInfo struct {
	ID       string
	CallerID string // run ID of the spawning agent ("main" for the interactive one)
	Spec     SubagentSpec

	Status        SubagentStatus
	killRequested bool
	runCtx        context.Context
	Cancel        context.CancelFunc
	done          chan struct{} // closed exactly once, when Status becomes terminal

	// Color/ThinkingColor are the per-subagent display colors assigned at spawn
	// (9-F hex channels; thinking is the dimmed same-hue variant) so both UIs
	// color the feed identically.
	Color         string
	ThinkingColor string

	StartedAt time.Time
	EndedAt   time.Time
	SessionID int64
	Result    *SubagentResult

	// Activity captured by the event relay (for status + death reports).
	LastActivity  string
	PartialOutput string

	// DispatchPos is the FIFO position a run was dispatched at, assigned under
	// subagentsMu while its queue slot is still held — true dispatch order even
	// though goroutines start after unlock. -1 = not yet dispatched (0 is valid).
	DispatchPos int
}

var (
	subagentsMu           sync.Mutex
	subagentInfos         = make(map[string]*SubagentInfo)
	subagentQueue         []*SubagentInfo // FIFO of queued, waiting for a free slot
	subagentSeq           int
	subagentDispatchSeq   int // monotonic counter assigning DispatchPos under subagentsMu (test observability)
)

// finalizeByRun records the system-subagent_finalize summary per run so the
// runner can deliver it as the subagent's output. Written by the tool handler
// (via AgentRuntime.Finalize), read after RunAgent returns.
var (
	finalizeMu    sync.Mutex
	finalizeByRun = make(map[string]string)
)

func recordSubagentFinalize(runID, summary string) {
	finalizeMu.Lock()
	finalizeByRun[runID] = summary
	finalizeMu.Unlock()
}

func takeSubagentFinalize(runID string) string {
	finalizeMu.Lock()
	defer finalizeMu.Unlock()
	s := finalizeByRun[runID]
	delete(finalizeByRun, runID)
	return s
}

// uiSink delivers subagent events to the frontend. Set at startup; nil falls
// back to logging (headless/tests). Carries lifecycle events and, via
// forwardToUI, blocking requests a subagent raises.
var (
	uiSinkMu sync.Mutex
	uiSink   func(any)
)

// SetUISink installs the function that receives subagent events. Pass nil to
// clear it.
func SetUISink(fn func(any)) {
	uiSinkMu.Lock()
	defer uiSinkMu.Unlock()
	uiSink = fn
}

func emitToUI(ev any) {
	uiSinkMu.Lock()
	fn := uiSink
	uiSinkMu.Unlock()
	if fn != nil {
		fn(ev)
		return
	}
	log.Printf("[subagent] (no UI sink) %v", ev)
}

// forwardToUI relays a blocking request from a subagent to the main UI so the
// user can still approve writes / answer; the subagent stays blocked on Resolve.
func forwardToUI(ev any) { emitToUI(ev) }

// classifySubagentStatus maps a run's ending cause onto its terminal status:
// kill (requested first) wins, then deadline expiry, then error, else complete.
func classifySubagentStatus(killRequested bool, ctxErr error, runErr error) SubagentStatus {
	switch {
	case killRequested:
		return StatusKilled
	case errors.Is(ctxErr, context.DeadlineExceeded):
		return StatusTimedOut
	case runErr != nil:
		return StatusFailed
	default:
		return StatusCompleted
	}
}

// subagentRunFn executes one dispatched subagent. A package var so tests can
// stub it and exercise cap/FIFO/kill logic without a model server; assigned in init() to avoid an initialization cycle.
var subagentRunFn func(*SubagentInfo)

// --- tool-facing backend (implemented here, injected into tools package) ---

type subagentToolBackend struct{}

// Spawn enqueues a subagent under the caller and returns its ID immediately
// (non-blocking). args keys: task, model?, profile?, tools?, mode?, deadlineSeconds?.
func (subagentToolBackend) Spawn(callerID, callerModel string, args map[string]any) (string, error) {
	id, _, err := SpawnSubagent(callerID, callerModel, args)
	return id, err
}

// Await blocks until the subagent reaches a terminal state or timeoutSeconds
// elapses (<=0 waits indefinitely). On wait-timeout it reports the subagent is
// still going so the model can poll status or kill instead of blocking forever.
func (subagentToolBackend) Await(id string, timeoutSeconds int) map[string]any {
	subagentsMu.Lock()
	info, ok := subagentInfos[id]
	if !ok {
		subagentsMu.Unlock()
		return subagentErrorMap("unknown subagent " + id)
	}
	done := info.done
	terminal := info.Status.isTerminal()
	name := info.Spec.Name
	subagentsMu.Unlock()

	if terminal {
		return subagentResultMap(info)
	}

	var timerC <-chan time.Time
	if timeoutSeconds > 0 {
		timerC = time.After(time.Duration(timeoutSeconds) * time.Second)
	} else {
		timerC = nil
	}

	if timerC == nil {
		<-done
		return subagentResultMap(info)
	}

	select {
	case <-done:
		return subagentResultMap(info)
	case <-timerC:
		subagentsMu.Lock()
		st := info.Status
		last := info.LastActivity
		subagentsMu.Unlock()
		return map[string]any{
			"status":  "pending",
			"id":      id,
			"name":    name,
			"running": string(st),
			"note":    fmt.Sprintf("subagent is still %s (last activity: %s). Use system-subagent_status to poll or system-kill_subagent to abort.", st, orDash(last)),
		}
	}
}

// List returns a snapshot of every subagent the caller spawned (all if
// callerID is empty).
func (subagentToolBackend) List(callerID string) []map[string]any {
	subagentsMu.Lock()
	defer subagentsMu.Unlock()
	out := make([]map[string]any, 0)
	for _, info := range subagentInfos {
		if callerID != "" && info.CallerID != callerID {
			continue
		}
		out = append(out, snapshotLocked(info))
	}
	return out
}

// Kill aborts a queued or running subagent. Returns false for unknown/terminal.
func (subagentToolBackend) Kill(id string) bool { return KillSubagent(id) }

// --- public registry API ---

// SpawnSubagent registers a subagent for the given caller and starts dispatch,
// returning the new ID, its resolved model, and an error only for hard config
// problems (unknown profile/model); at-cap spawns enqueue rather than error.
func SpawnSubagent(callerID, callerModel string, args map[string]any) (string, string, error) {
	if callerID == "" {
		callerID = "main"
	}

	spec, err := buildSubagentSpec(callerModel, MainRuntime().ModelID, args)
	if err != nil {
		return "", "", err
	}

	subagentsMu.Lock()
	subagentSeq++
	id := fmt.Sprintf("sub-%d", subagentSeq)

	info := &SubagentInfo{
		ID:            id,
		CallerID:      callerID,
		Spec:          spec,
		Status:        StatusQueued,
		done:          make(chan struct{}),
		DispatchPos:   -1, // not yet dispatched
		Color:         randomSubagentColor(),
		ThinkingColor: "",
	}
	info.ThinkingColor = dimmedSameHue(info.Color, 40)
	subagentInfos[id] = info
	subagentQueue = append(subagentQueue, info)
	subagentsMu.Unlock()

	emitTransition(info)
	log.Printf("Subagent %s (%q) queued for %s; caller=%s model=%s mode=%s", id, spec.Task, spec.Name, callerID, spec.ModelID, spec.Mode)

	tryDispatch()
	return id, spec.ModelID, nil
}

// buildSubagentSpec resolves a spawn request into a fully-populated spec from
// args + profile + config. Pure w.r.t. process state (no registry, goroutines,
// or DB) so it is directly unit-testable.
func buildSubagentSpec(callerModel, mainModel string, args map[string]any) (SubagentSpec, error) {
	var spec SubagentSpec

	task, _ := args["task"].(string)
	if strings.TrimSpace(task) == "" {
		return spec, errors.New("system-spawn_subagent: 'task' is required")
	}
	spec.Task = task

	modelArg, _ := args["model"].(string)
	profileName, _ := args["profile"].(string)
	modeRaw, _ := args["mode"].(string)
	var toolsAllow []string
	if raw, ok := args["tools"].([]any); ok {
		for _, t := range raw {
			if s, ok := t.(string); ok {
				toolsAllow = append(toolsAllow, s)
			}
		}
	}
	deadlineSec := 0
	if n, ok := toIntArg(args["deadlineSeconds"]); ok {
		deadlineSec = n
	}

	cfg := GetAppConfig()
	var profile *data.SubagentProfile
	if profileName != "" {
		profile = cfg.GetSubagentProfile(profileName)
		if profile == nil {
			return spec, fmt.Errorf("system-spawn_subagent: unknown profile %q", profileName)
		}
		spec.Profile = profileName
		spec.Name = profileName
	}
	if spec.Name == "" {
		spec.Name = "subagent"
	}

	// Model resolution: "model: null" inherits the caller's live model
	// (explicit arg -> profile -> caller -> app default), which also reuses its
	// running executor via the refcounted pool.
	modelID := modelArg
	if modelID == "" && profile != nil {
		modelID = profile.Model
	}
	if modelID == "" {
		modelID = callerModel
	}
	if modelID == "" {
		modelID = mainModel
	}
	if modelID == "" {
		modelID = cfg.GetDefaultModel()
	}
	if modelID == "" {
		return spec, errors.New("system-spawn_subagent: could not resolve a model (no arg, profile, or caller model)")
	}
	if cfg.GetModelByID(modelID) == nil {
		return spec, fmt.Errorf("system-spawn_subagent: unknown model %q", modelID)
	}
	spec.ModelID = modelID
	spec.Managed = cfg.GetModelByID(modelID).Managed

	switch modeRaw {
	case "persistent":
		spec.Mode = ModePersistent
	case "stealth", "":
		spec.Mode = ModeStealth
	default:
		return spec, fmt.Errorf("system-spawn_subagent: unknown mode %q (want stealth|persistent)", modeRaw)
	}

	spec.Tools = filterTools(cfg.Tools, toolsAllow, profile)

	deadline := defaultSubagentDeadline
	if deadlineSec > 0 {
		deadline = time.Duration(deadlineSec) * time.Second
	} else if profile != nil && profile.DeadlineSeconds > 0 {
		deadline = time.Duration(profile.DeadlineSeconds) * time.Second
	}
	spec.Deadline = deadline

	spec.SystemPrompt = defaultSubagentSystemPrompt
	if profile != nil && profile.Prompt != "" {
		spec.SystemPrompt = profile.Prompt
	}

	return spec, nil
}

// filterTools builds a subagent's effective tool set: an allowlist (args or
// profile) intersects with available tools, else all are kept. Orchestration
// tools are always excluded; system-subagent_finalize is always appended so every
// subagent can end its run.
func filterTools(available []data.Tool, argAllow []string, profile *data.SubagentProfile) []data.Tool {
	orchestration := map[string]bool{
		"system-spawn_subagent":  true,
		"system-await_subagent":  true,
		"system-subagent_status": true,
		"system-kill_subagent":   true,
	}

	var allow []string
	if len(argAllow) > 0 {
		allow = argAllow
	} else if profile != nil && len(profile.AvailableTools) > 0 {
		allow = profile.AvailableTools
	}

	var want map[string]bool
	if allow != nil {
		want = make(map[string]bool, len(allow))
		for _, n := range allow {
			want[n] = true
		}
	}

	out := make([]data.Tool, 0, len(available)+1)
	for _, t := range available {
		name := t.Function.Name
		if orchestration[name] || name == "system-subagent_finalize" {
			continue // finalize appended explicitly below
		}
		if want != nil && !want[name] {
			continue
		}
		out = append(out, t)
	}

	out = append(out, finalizeToolDef())
	return out
}

// finalizeToolDef is the system-subagent_finalize schema; filterTools always appends it
// so a subagent can never launch without a way to end its run.
func finalizeToolDef() data.Tool {
	return data.Tool{
		Type: "function",
		Function: data.ToolFunctionDef{
			Name: "system-subagent_finalize",
			Description: "Finalize your work and return a final answer to the calling agent. Call this exactly once when you are done; its summary becomes your deliverable. Do not call other tools after finalizing.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"summary": map[string]any{"type": "string", "description": "Your final answer / summary for the calling agent."},
				},
				"required": []string{"summary"},
			},
		},
	}
}

// tryDispatch promotes queued subagents to running while per-pool slots are
// free. Managed (local) and unmanaged (remote) subagents have independent caps
// from config. Scans the queue for the first subagent whose pool has a free
// slot (skip-ahead) so a blocked managed head doesn't starve unmanaged runs.
func tryDispatch() {
	for {
		subagentsMu.Lock()
		var runningManaged, runningUnmanaged int
		for _, info := range subagentInfos {
			if info.Status == StatusRunning {
				if info.Spec.Managed {
					runningManaged++
				} else {
					runningUnmanaged++
				}
			}
		}

		cfg := GetAppConfig()
		maxManaged := cfg.GetSubagentMaxManaged()
		maxUnmanaged := cfg.GetSubagentMaxUnmanaged()

		// Find the first queue entry whose pool has a free slot.
		dispatchIdx := -1
		for i, info := range subagentQueue {
			if info.Spec.Managed {
				if runningManaged < maxManaged {
					dispatchIdx = i
					break
				}
			} else {
				if runningUnmanaged < maxUnmanaged {
					dispatchIdx = i
					break
				}
			}
		}

		if dispatchIdx == -1 {
			subagentsMu.Unlock()
			return
		}

		info := subagentQueue[dispatchIdx]
		subagentQueue = append(subagentQueue[:dispatchIdx], subagentQueue[dispatchIdx+1:]...)

		ctx, cancel := context.WithTimeout(context.Background(), info.Spec.Deadline)
		info.runCtx = ctx
		info.Cancel = cancel
		info.Status = StatusRunning
		info.StartedAt = time.Now()
		info.DispatchPos = subagentDispatchSeq
		subagentDispatchSeq++
		subagentsMu.Unlock()

		emitTransition(info)
		log.Printf("Subagent %s dispatched (running, managed=%v); deadline in %s", info.ID, info.Spec.Managed, info.Spec.Deadline)
		go subagentRunFn(info)
	}
}

// resolveSubagentReasoning returns the reasoning level a subagent should start
// with: it inherits the caller's chosen level only when running on the same
// model (so same-model work matches the operator's selection); any other model
// falls back to its own configured default ("", resolved per-model at payload time).
func resolveSubagentReasoning(modelID, callerModel, callerReasoning string) string {
	if modelID != "" && modelID == callerModel {
		return callerReasoning
	}
	return ""
}

// runSubagent executes one subagent end to end: builds its own runtime, streams
// through the shared RunAgent flow, and records a terminal result; a relay goroutine tracks activity for death reports.
func runSubagent(info *SubagentInfo) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Subagent %s panicked: %v", info.ID, r)
			completeSubagent(info, StatusFailed, fmt.Sprintf("panic: %v", r), "")
		}
	}()

	// A subagent running on the main runtime's model inherits that runtime's
	// chosen reasoning level (so it matches the operator's selection); any
	// other model defaults to its own configured default ("").
	rt := &AgentRuntime{
		ID:              info.ID,
		Role:            RoleSubagent,
		ModelID:         info.Spec.ModelID,
		Stealth:         info.Spec.Mode == ModeStealth,
		SystemPrompt:    info.Spec.SystemPrompt,
		Messages:        make([]map[string]any, 0),
		Tools:           info.Spec.Tools,
		ContextSize:     data.DefaultContextSize,
		ReasoningEffort: resolveSubagentReasoning(info.Spec.ModelID, MainRuntime().ModelID, MainRuntime().ReasoningEffort),
		Slot:            info.ID, // its own slot; cross-slot join shares the caller's executor if same model
		ParentID:        info.CallerID,
	}

	// Persistent subagents get a real session up front (so InferSession doesn't
	// recreate it); the parent link is the main runtime's session ID (0 when none).
	if info.Spec.Mode == ModePersistent {
		parentID := int64(0)
		if s := MainRuntime().Session; s != nil && s.ID > 0 {
			parentID = s.ID
		}
		if s, err := data.NewSession(info.Spec.Name + " (subagent)"); err == nil && s != nil {
			_ = data.SetSessionParent(s.ID, parentID)
			rt.Session = s
			subagentsMu.Lock()
			info.SessionID = s.ID
			subagentsMu.Unlock()
		}
	}

	internal := make(chan any, 512)
	go relaySubagentEvents(info, internal)

	ctx := info.runCtx
	finalText, runErr := RunAgent(ctx, rt, info.Spec.Task, internal)
	close(internal) // let the relay drain then exit

	subagentsMu.Lock()
	killRequested := info.killRequested
	subagentsMu.Unlock()

	st := classifySubagentStatus(killRequested, ctx.Err(), runErr)

	output := takeSubagentFinalize(info.ID) // system-subagent_finalize argument
	if output == "" {
		output = finalText                   // last assistant message / streamed text
	}

	errStr := ""
	if st == StatusFailed && runErr != nil {
		errStr = runErr.Error()
	}

	completeSubagent(info, st, errStr, output)
	log.Printf("Subagent %s finished: status=%s output=%d bytes", info.ID, st, len(output))
}

// completeSubagent performs the single terminal transition: sets status (once),
// closes done, builds the Result, and promotes the next queued run. Only the first call wins.
func completeSubagent(info *SubagentInfo, st SubagentStatus, errStr, output string) {
	subagentsMu.Lock()
	if info.Status.isTerminal() {
		subagentsMu.Unlock()
		return // already finished (e.g. killed after it errored)
	}
	info.Status = st
	info.EndedAt = time.Now()

	dur := 0.0
	if !info.StartedAt.IsZero() {
		dur = info.EndedAt.Sub(info.StartedAt).Seconds()
	}
	last, partial := info.LastActivity, info.PartialOutput
	sid := info.SessionID
	name := info.Spec.Name

	info.Result = &SubagentResult{
		ID:            info.ID,
		Name:          name,
		Status:        st,
		Output:        output,
		Error:         errStr,
		LastActivity:  last,
		PartialOutput: partial,
		DurationSec:   dur,
		SessionID:     sid,
	}
	cancel := info.Cancel
	close(info.done)
	subagentsMu.Unlock()

	if cancel != nil {
		cancel() // ensure the run's context/timer is released
	}

	emitTransition(info)
	tryDispatch()
}

// relaySubagentEvents consumes a subagent's event channel: blocking requests
// (permission/ask) are forwarded to the main UI, tool + stream activity is
// captured for status/death reports. Non-blocking events are NOT merged into the
// main chat stream, keeping the interactive conversation clean.
func relaySubagentEvents(info *SubagentInfo, in <-chan any) {
	const partialCap = 4000
	for ev := range in {
		switch v := ev.(type) {
		case data.PermissionRequest:
			v.AgentID, v.AgentName = info.ID, info.Spec.Name
			subagentsMu.Lock()
			info.LastActivity = fmt.Sprintf("waiting for permission on %s (%s)", v.Path, v.Tool)
			subagentsMu.Unlock()
			forwardToUI(v)
		case data.AskUserRequest:
			v.AgentID, v.AgentName = info.ID, info.Spec.Name
			subagentsMu.Lock()
			info.LastActivity = "waiting for user answer"
			subagentsMu.Unlock()
			forwardToUI(v)
		case data.StateMsg:
			v.AgentID, v.AgentName = info.ID, info.Spec.Name
			forwardToUI(v)
		case data.ToolStart:
			v.AgentID, v.AgentName = info.ID, info.Spec.Name
			forwardToUI(v)
		case data.ToolCallSummary:
			v.AgentID, v.AgentName = info.ID, info.Spec.Name
			subagentsMu.Lock()
			info.LastActivity = fmt.Sprintf("%s%s", v.Name, truncateForReport(v.ResultPreview, 160))
			subagentsMu.Unlock()
			forwardToUI(v)
		case data.Response:
			if v.Type == "response" && v.Message != "" {
				subagentsMu.Lock()
				if len(info.PartialOutput)+len(v.Message) <= partialCap {
					info.PartialOutput += v.Message
				} else {
					info.PartialOutput = info.PartialOutput[:partialCap] + "… (truncated)"
				}
				subagentsMu.Unlock()
			}
			// Forward stream content to the UI for per-agent feeds: response text,
			// thinking/debug fragments, and console lines. Other types (usage,
			// eof markers) are dropped.
			switch v.Type {
			case "response", "debug", "console":
				v.AgentID, v.AgentName = info.ID, info.Spec.Name
				forwardToUI(v)
			}
		default:
			// usage metrics, eof — not surfaced.
		}
	}
}

// KillSubagent aborts a queued or running subagent: queued ones are removed and
// finished immediately; running ones get their context cancelled (surfaces as "killed"). False for unknown/terminal.
func KillSubagent(id string) bool {
	subagentsMu.Lock()
	info, ok := subagentInfos[id]
	if !ok || info.Status.isTerminal() {
		subagentsMu.Unlock()
		return false
	}

	switch info.Status {
	case StatusQueued:
		for i, q := range subagentQueue {
			if q.ID == id {
				subagentQueue = append(subagentQueue[:i], subagentQueue[i+1:]...)
				break
			}
		}
		subagentsMu.Unlock()
		log.Printf("Subagent %s killed (was queued)", id)
		completeSubagent(info, StatusKilled, "", "killed while queued")
	case StatusRunning:
		info.killRequested = true
		cancel := info.Cancel
		subagentsMu.Unlock()
		if cancel != nil {
			cancel()
		}
		log.Printf("Subagent %s kill requested (running)", id)
	default:
		subagentsMu.Unlock()
		return false
	}
	return true
}

// ListSubagents returns a snapshot for the caller (all if callerID empty).
func ListSubagents(callerID string) []map[string]any {
	return subagentToolBackend{}.List(callerID)
}

// killAllSubagents aborts every non-terminal subagent (app shutdown path).
func killAllSubagents(reason string) {
	subagentsMu.Lock()
	ids := make([]string, 0)
	for id, info := range subagentInfos {
		if !info.Status.isTerminal() {
			ids = append(ids, id)
		}
	}
	subagentsMu.Unlock()

	for _, id := range ids {
		KillSubagent(id)
	}
	if len(ids) > 0 {
		log.Printf("Aborted %d live subagent(s): %s", len(ids), reason)
	}
}

// --- snapshot / emit helpers ---

// snapshotLocked builds a display map. Caller MUST hold subagentsMu.
func snapshotLocked(info *SubagentInfo) map[string]any {
	dur := 0.0
	if !info.StartedAt.IsZero() {
		end := info.EndedAt
		if end.IsZero() && !info.Status.isTerminal() {
			end = time.Now()
		}
		if !end.IsZero() {
			dur = end.Sub(info.StartedAt).Seconds()
		}
	}
	m := map[string]any{
		"id":             info.ID,
		"name":           info.Spec.Name,
		"status":         string(info.Status),
		"model":          info.Spec.ModelID,
		"mode":           string(info.Spec.Mode),
		"color":          info.Color,
		"thinking_color": info.ThinkingColor,
		"last_activity":  orDash(info.LastActivity),
		"duration_sec":   dur,
	}
	if info.Result != nil {
		m["output"] = info.Result.Output
		if info.Result.Error != "" {
			m["error"] = info.Result.Error
		}
		if info.Result.SessionID > 0 {
			m["session_id"] = info.Result.SessionID
		}
	}
	return m
}

func subagentResultMap(info *SubagentInfo) map[string]any {
	subagentsMu.Lock()
	defer subagentsMu.Unlock()
	return snapshotLocked(info)
}

func subagentErrorMap(msg string) map[string]any {
	return map[string]any{"status": "error", "error": msg}
}

// emitTransition publishes a lifecycle event to the UI sink. It snapshots under
// the lock so it never races the relay's writes.
func emitTransition(info *SubagentInfo) {
	subagentsMu.Lock()
	id, name, caller := info.ID, info.Spec.Name, info.CallerID
	model := info.Spec.ModelID
	st := string(info.Status)
	last := info.LastActivity
	started, ended := info.StartedAt, info.EndedAt
	sessionID := info.SessionID
	color, thinking := info.Color, info.ThinkingColor
	subagentsMu.Unlock()

	emitToUI(data.SubagentEvent{
		ID:            id,
		Name:          name,
		CallerID:      caller,
		ModelID:       model,
		Status:        st,
		Color:         color,
		ThinkingColor: thinking,
		LastActivity:  last,
		SessionID:     sessionID,
		StartedAt:     started,
		EndedAt:       ended,
	})
}

// toIntArg reads an int from a map value (JSON numbers decode as float64).
func toIntArg(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
		return 0, false
	case int64:
		return int(n), true
	}
	return 0, false
}

// truncateForReport bounds a string for display in death/status reports.
func truncateForReport(s string, max int) string {
	if s == "" {
		return ""
	}
	r := []rune(" " + s)
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max-1]) + "…"
}

// randomSubagentColor picks one bright accent per subagent: each RGB channel
// is a random 9-F hex digit, so the result sits in the high-brightness range
// where it reads clearly on dark UI surfaces.
func randomSubagentColor() string {
	d := "9ABCDEF"
	return fmt.Sprintf("#%c%c%c", d[rand.Intn(len(d))], d[rand.Intn(len(d))], d[rand.Intn(len(d))])
}

// dimmedSameHue scales a #rrggbb color's channels down (factor 0-100) so the
// hue is preserved at reduced brightness — used for subagent thinking text.
func dimmedSameHue(hex string, factor int) string {
	if len(hex) != 7 || hex[0] != '#' {
		return "#555"
	}
	var r, g, b uint8
	fmt.Sscanf(hex[1:], "%x%x%x", &r, &g, &b)
	return fmt.Sprintf("#%02x%02x%02x", r*uint8(factor)/100, g*uint8(factor)/100, b*uint8(factor)/100)
}

// orDash renders an empty string as a dash for display.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// defaultSubagentSystemPrompt is used when a profile does not supply one.
const defaultSubagentSystemPrompt = `You are a focused subagent. Complete the specific task you were given, using only the tools available to you. Do not ask the user questions and do not attempt to manage other agents. When you are done, call system-subagent_finalize exactly once with a concise summary of your findings or result. Be direct; the calling agent reads only your finalized summary.`

func init() {
	// Wire the subagent lifecycle into the tool layer so the model can drive it.
	tools.SetSubagentBackend(subagentToolBackend{})
	subagentRunFn = runSubagent
}
