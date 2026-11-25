import { BehaviorSubject, distinctUntilChanged, Observable, Subject } from "rxjs";
import { EventsOn } from "../../wailsjs/runtime/runtime";
import { data } from "@wails/models";
import { ListContexts, LoadSession, SetActiveContext } from "@wails/main/App";

export enum InputMode { PROMPT, TERM }

// RunState mirrors data.StateMsg.state — explicit agent run-state transitions
// emitted on "agent-state". The status bar is driven from these events rather
// than inferred from the type of response message arriving.
export enum RunState {
  IDLE = 'idle',
  POSTING = 'posting',
  INGEST = 'ingest',
  ERROR = 'error',
  THINKING = 'thinking',
  ACTIVE = 'active',
  PARSING = 'parsing',
  EXECUTING = 'executing',
  WAITING = 'waiting'
}

// StateEvent mirrors data.StateMsg. agent_id is empty for the main (interactive)
// agent; subagent runs stamp theirs.
export interface StateEvent {
  state: string;
  agent_id?: string;
  agent_name?: string;
}

export interface AgentOutput {
  thinking: boolean
  data: string
  eol: boolean
}

// ToolStartInfo mirrors data.ToolStart — a tool call that has begun executing.
export interface ToolStartInfo {
  id: string;
  name: string;
  arguments?: Record<string, any>;
}

// ToolCallEvent mirrors data.ToolCallSummary — a finished tool call.
// resultPreview carries the tool's one-line self-summary (e.g. "Wrote 1234
// bytes to ./a.txt") produced by agent/tools.SummarizeTool.
export interface ToolCallEvent {
  id: string;
  name: string;
  success: boolean;
  arguments?: Record<string, any>;
  resultPreview?: string;
  // paramsPreview carries the one-line parameter summary (paths/counts only,
  // never content) computed server-side by agent/tools.SummarizeParams.
  paramsPreview?: string;
}

// CompactionEvent is emitted on "agent-compaction" when a context compaction
// registers: message carries the seeded summary — the framed user message that
// re-seeds the session (identical text to the persisted seed row shown on
// reload). The session view appends it as a frozen user bubble.
export interface CompactionEvent {
  message: string;
}

// SubagentEvent mirrors data.SubagentEvent — a lifecycle transition of one
// subagent. color/thinking_color are assigned at spawn (9-F hex channels) and
// let any UI color the feed consistently.
export interface SubagentEvent {
  id: string;
  name?: string;
  caller_id?: string;
  model_id?: string;
  status: string; // queued | running | completed | failed | killed | timed_out
  color?: string;
  thinking_color?: string;
  last_activity?: string;
  session_id?: number;
  started_at?: string;
  ended_at?: string;
}

// PermissionViewport mirrors data.PermissionViewport — the optional content
// payload a permission request displays (diff or assembled command). kind is
// absent for legacy/empty payloads; title falls back to a UI-derived header.
export interface PermissionViewport {
  kind?: string; // "diff" | "script"
  title?: string;
  content: string;
}

// PermissionRequest mirrors data.PermissionRequest — a tool asking for user
// approval before proceeding. Field names match the Go json tags because the
// Wails event carries the raw JSON payload (agent_id / agent_name, etc.).
// Resolve with App.ResolvePermissionRequest(id, allowed).
export interface PermissionRequest {
  id: string;
  tool: string;
  path: string;
  action: string; // "create" | "overwrite" | "update" | "delete"
  prompt?: string;
  level: string; // "info" | "warning" | "danger"
  viewport?: PermissionViewport;
  timestamp: string;
  agent_id?: string;
  agent_name?: string;
}

// AskOption mirrors data.AskOption — one selectable choice in a question.
export interface AskOption {
  label: string;
  value: string;
  style?: string;
}

// AskUserQuestion mirrors data.AskUserQuestion — a single system-ask question.
// allow_free_form is serialized with Go omitempty: the key is PRESENT only
// when free-form entry is enabled; absent means disabled.
export interface AskUserQuestion {
  id: string;
  type: string; // "confirm" | "select" | "multi-select" | "free-response"
  prompt: string;
  context?: string;
  placeholder?: string;
  options?: AskOption[];
  allow_free_form?: boolean;
}

// AskUserRequest mirrors data.AskUserRequest — the system-ask form payload
// (raw JSON again: snake_case optional fields). Resolve with
// App.ResolveAskRequest(id, answers) or dismiss with App.CancelAskRequest(id).
export interface AskUserRequest {
  id: string;
  questions: AskUserQuestion[];
  agent_id?: string;
  agent_name?: string;
}

const contextsLoaded = new BehaviorSubject<data.Context[]>([]);
const terminalMessage = new Subject<string>();
const agentMessage = new Subject<AgentOutput>();
const toolStartMessage = new Subject<ToolStartInfo>();
const toolCallMessage = new Subject<ToolCallEvent>();
const compactionMessage = new Subject<CompactionEvent>();
const permissionRequestMessage = new Subject<PermissionRequest>();
const askRequestMessage = new Subject<AskUserRequest>();
const subagentEvent = new Subject<SubagentEvent>();
// Stamped (agent_id set) run-state transitions: the per-subagent status bar is
// driven from these; main-agent states never land here.
const subagentState = new Subject<StateEvent>();
const statusMessage = new BehaviorSubject<RunState>(RunState.IDLE);
const sessionMessage = new Subject<{ session: data.Session, messages: data.Message[] }>();
const viewRequested = new Subject<HTMLElement | undefined>();
const promptRequested = new Subject<string>();

EventsOn('terminal-output', (data: string) => {
  console.log(data);
  terminalMessage.next(data);
});

EventsOn('agent-state', (data: StateEvent) => {
  if (!data.agent_id) {
    statusMessage.next(data.state as RunState);
  } else {
    // Subagent run states feed the per-agent status bar instead of the
    // global one.
    subagentState.next(data);
  }
});

EventsOn('agent-response', (data: AgentOutput) => {
  agentMessage.next(data);
});

EventsOn('agent-tool-start', (data: ToolStartInfo) => {
  toolStartMessage.next(data);
});

EventsOn('agent-tool-call', (data: ToolCallEvent) => {
  toolCallMessage.next(data);
});

EventsOn('agent-compaction', (data: CompactionEvent) => {
  compactionMessage.next(data);
});

EventsOn('permission-request', (data: PermissionRequest) => {
  permissionRequestMessage.next(data);
});

EventsOn('ask-user-request', (data: AskUserRequest) => {
  askRequestMessage.next(data);
});

EventsOn('subagent-event', (data: SubagentEvent) => {
  subagentEvent.next(data);
});

ListContexts().then(contexts => {
  contextsLoaded.next(contexts || []);
});

export async function replayPrompt(message: data.Message) {
  promptRequested.next(message.Contents);
}

export async function requestPrompt(prompt: string) {
  promptRequested.next(prompt);
}

export async function requestView(view: HTMLElement | undefined) {
  viewRequested.next(view);
}

export async function setContext(context: data.Context | undefined) {
  return SetActiveContext(context!)
}

export async function loadSession(session: data.Session): Promise<data.Message[]> {
  const messages = await LoadSession(session)
  sessionMessage.next({ session, messages: messages || [] })
  return messages;
}

export type ApplicationSettings = {
  activeContext: data.Context | undefined,
  inputMode: InputMode,
  model: string,
  observables: {
    onAgentMessage: Observable<AgentOutput>,
    onContextsLoaded: Observable<data.Context[]>,
    onPromptRequested: Observable<string>,
    onSessionLoaded: Observable<{ session: data.Session, messages: data.Message[] }>,
    onStatusChanged: Observable<RunState>,
    onTerminalMessage: Observable<string>,
    onViewRequested: Observable<HTMLElement | undefined>,
    onToolStart: Observable<ToolStartInfo>,
    onToolCall: Observable<ToolCallEvent>,
    onCompaction: Observable<CompactionEvent>,
    onPermissionRequest: Observable<PermissionRequest>,
    onAskRequest: Observable<AskUserRequest>,
    onSubagentEvent: Observable<SubagentEvent>,
    onSubagentState: Observable<StateEvent>,
  }
}

export const AppSettings = {
  activeContext: undefined,
  inputMode: InputMode.PROMPT,
  model: '', // Resolved on startup from system.defaultModel (see statusbar)
  observables: {
    onAgentMessage: agentMessage.asObservable(),
    onContextsLoaded: contextsLoaded.asObservable(),
    onPromptRequested: promptRequested.asObservable(),
    onSessionLoaded: sessionMessage.asObservable(),
    onStatusChanged: statusMessage.pipe(distinctUntilChanged()),
    onTerminalMessage: terminalMessage.asObservable(),
    onViewRequested: viewRequested.asObservable(),
    onToolStart: toolStartMessage.asObservable(),
    onToolCall: toolCallMessage.asObservable(),
    onCompaction: compactionMessage.asObservable(),
    onPermissionRequest: permissionRequestMessage.asObservable(),
    onAskRequest: askRequestMessage.asObservable(),
    onSubagentEvent: subagentEvent.asObservable(),
    onSubagentState: subagentState.asObservable(),
  }
} as ApplicationSettings;
