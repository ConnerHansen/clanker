import { ListAvailableModels, GetDefaultModel, IsSummarizing, PendingInputCount, GetGitBranch, GetCurrentDirectory, GetReasoningOptions, GetReasoningLevel, SetReasoningEffort } from "@wails/main/App";
import { data } from "@wails/models";
import { AppSettings, setContext, RunState, SubagentEvent } from "@services/app-settings";
import { Component } from "@components/component";
import { StatusPill } from "./status-pill/status-pill";
import { StatusSelect } from "./status-select/status-select";

// Terminal subagent statuses linger in the bar briefly so completions stay
// visible before fading (mirrors the TUI's linger window).
const SUBAGENT_LINGER_MS = 5000;
// Width reserved for the "+N more" overflow pill while trimming.
const MORE_PILL_RESERVE = 72;

type SubStatus = 'queued' | 'running' | 'completed' | 'failed' | 'killed' | 'timed_out';

interface SubagentEntry {
  id: string;
  name?: string;
  color?: string;
  status: SubStatus;
  state: string; // refined run state from stamped agent-state events
  startedAt: number | null;
  endedAt: number | null;
}

function isTerminal(status: SubStatus): boolean {
  return status === 'completed' || status === 'failed' || status === 'killed' || status === 'timed_out';
}

// label resolves the short state word shown per entry; a terminal status wins
// over the last run-state seen while it was live.
function subagentLabel(e: SubagentEntry): string {
  if (isTerminal(e.status)) {
    switch (e.status) {
      case 'completed': return 'done';
      case 'failed': return 'failed';
      case 'killed': return 'killed';
      case 'timed_out': return 'timeout';
    }
  }
  if (e.status === 'queued') return 'queued';
  switch (e.state) {
    case 'thinking': return 'thinking';
    case 'executing': return 'executing';
    case 'parsing': return 'parsing';
    case 'waiting': return 'waiting';
    default: return 'running';
  }
}

// cssClass maps an entry onto one of the statusbar's existing state classes so
// the colorization matches the main status pill.
function subagentCssClass(e: SubagentEntry): string {
  const label = subagentLabel(e);
  switch (label) {
    case 'queued': return 'queued';
    case 'done': return 'completed';
    case 'failed': case 'killed': case 'timeout': return 'error';
    case 'thinking': return 'thinking';
    case 'executing': return 'executing';
    case 'parsing': return 'parsing';
    case 'waiting': return 'waiting';
    default: return 'active';
  }
}

function formatSubagentElapsed(e: SubagentEntry): string {
  const start = e.startedAt;
  if (e.status === 'queued' || !start) return '';
  const end = e.endedAt ?? Date.now();
  let totalSeconds = Math.max(0, Math.floor((end - start) / 1000));
  const h = Math.floor(totalSeconds / 3600);
  totalSeconds -= h * 3600;
  const m = Math.floor(totalSeconds / 60);
  const s = totalSeconds - m * 60;
  let label = '';
  if (h > 0) label += `${h}h`;
  if (m > 0) label += `${m}m`;
  label += `${s}s`;
  return label;
}

// seqNum extracts the sequence number from a "sub-N" id for stable spawn-order
// sorting; non-matching ids sort last.
function seqNum(id: string): number {
  const m = /sub-(\d+)/.exec(id);
  return m ? parseInt(m[1], 10) : Number.MAX_SAFE_INTEGER;
}

export class Statusbar implements Component {
  private element: HTMLDivElement;
  private currentStatus: string | null = null;
  private subagentRow: HTMLDivElement;
  private subagents = new Map<string, SubagentEntry>();

  constructor(
    private statusPill = new StatusPill()
  ) {
    this.element = document.createElement('div');
    this.element.classList.add('statusbar');

    // The outer element is a column: the (normally hidden) subagent status row
    // sits above the main status line.
    this.subagentRow = document.createElement('div');
    this.subagentRow.classList.add('subagent-row', 'hidden');
    this.element.appendChild(this.subagentRow);

    const mainRow = this.buildMainRow();
    this.element.appendChild(mainRow);

    this.setupSubagents();
  }

  public component(): HTMLElement {
    return this.element;
  }

  // --- main status line ---

  private buildMainRow(): HTMLDivElement {
    const mainRow = document.createElement('div');
    mainRow.classList.add('statusbar-main');

    mainRow.append(this.statusPill.component());

    // Background summary sweep indicator: plain pill like the others, only
    // visible while a sweep is in flight. State changes are rare (at most
    // once per 10m tick), so a light poll of the bound getter replaces event
    // plumbing.
    const summarizingPill = document.createElement('div');
    summarizingPill.className = 'status-pill';
    summarizingPill.style.display = 'none';
    mainRow.appendChild(summarizingPill);

    const pollSummarizing = () => {
      IsSummarizing().then(summarizing => {
        if (summarizing) {
          summarizingPill.textContent = 'summarizing...';
          summarizingPill.style.display = '';
        } else {
          summarizingPill.style.display = 'none';
        }
      });
    };
    pollSummarizing();
    window.setInterval(pollSummarizing, 2000);

    // Queued user messages: text submitted while a run is in flight waits on
    // the agent's queue for injection at the next cycle boundary. Visible only
    // when non-zero; polled like the sweep indicator (changes are rare).
    const pendingPill = document.createElement('div');
    pendingPill.className = 'status-pill';
    pendingPill.style.display = 'none';
    mainRow.appendChild(pendingPill);

    const pollPending = () => {
      PendingInputCount().then(count => {
        if (count > 0) {
          pendingPill.textContent = count === 1 ? '1 pending input' : `${count} pending inputs`;
          pendingPill.style.display = '';
        } else {
          pendingPill.style.display = 'none';
        }
      });
    };
    pollPending();
    window.setInterval(pollPending, 2000);

    // Model/context selects sit after the indicator pills; the environment
    // items and session timer are appended last so they render at the right
    // end of the line (timer last), matching the TUI's status line.
    const statusSelect = new StatusSelect('Model: ', [], 0, (newValue) => {
      AppSettings.model = newValue;
      refreshReasoning();
    });

    let availableContexts: data.Context[] = [];
    const contextSelect = new StatusSelect('Context: ', ['None'], 0, (newValue) => {
      let context = availableContexts.find(c => c.name == newValue);
      setContext(context);
    });

    // Reasoning selector: options are per-model, so they're fetched for the
    // current model on load and on every model switch. Hidden entirely when
    // the active model has no configurable reasoning block.
    const reasoningSelect = new StatusSelect('Reasoning: ', [''], 0, (newValue) => {
      SetReasoningEffort(newValue);
    });
    reasoningSelect.component().style.display = 'none';

    const refreshReasoning = () => {
      const model = AppSettings.model;
      if (!model) {
        reasoningSelect.component().style.display = 'none';
        return;
      }
      GetReasoningOptions(model).then(opts => {
        if (!opts || opts.length === 0) {
          reasoningSelect.component().style.display = 'none';
          return;
        }
        reasoningSelect.component().style.display = '';
        GetReasoningLevel(model).then(level => {
          const idx = Math.max(0, opts.indexOf(level));
          reasoningSelect.setOptions(opts, idx);
        });
      });
    };

    mainRow.appendChild(statusSelect.component());
    mainRow.appendChild(reasoningSelect.component());
    mainRow.appendChild(contextSelect.component());

    // Environment items: working directory + git branch, fetched once (both
    // are stable for the process lifetime). The branch pill is hidden when the
    // backend returns empty (not a repo / git unavailable).
    const dirPill = document.createElement('div');
    dirPill.className = 'status-pill';
    dirPill.style.display = 'none';
    mainRow.appendChild(dirPill);

    const branchPill = document.createElement('div');
    branchPill.className = 'status-pill';
    branchPill.style.display = 'none';
    mainRow.appendChild(branchPill);

    Promise.all([GetCurrentDirectory(), GetGitBranch()]).then(([dir, branch]) => {
      if (dir) {
        dirPill.textContent = dir;
        dirPill.style.display = '';
      }
      if (branch) {
        branchPill.textContent = branch;
        branchPill.style.display = '';
      }
    });

    // Session timer: elapsed time since the statusbar started, formatted
    // like the TUI's (Go duration style: "45s", "2m30s"), ticking every second.
    const sessionTimerPill = document.createElement('div');
    sessionTimerPill.className = 'status-pill';
    mainRow.appendChild(sessionTimerPill);

    const sessionStart = Date.now();
    const tickSessionTimer = () => {
      const totalSeconds = Math.floor((Date.now() - sessionStart) / 1000);
      const hours = Math.floor(totalSeconds / 3600);
      const minutes = Math.floor((totalSeconds % 3600) / 60);
      const seconds = totalSeconds % 60;
      let label = '';
      if (hours > 0) label += `${hours}h`;
      if (minutes > 0) label += `${minutes}m`;
      label += `${seconds}s`;
      sessionTimerPill.textContent = label + ' ';
    };
    tickSessionTimer();
    window.setInterval(tickSessionTimer, 1000);

    Promise.all([ListAvailableModels(), GetDefaultModel()]).then(([models, defaultModel]) => {
      models = models.sort();
      // Prefer the configured system.defaultModel; fall back to the first
      // available model when it is unset or not present in the list.
      let selected: string | undefined;
      if (defaultModel && models.includes(defaultModel)) {
        selected = defaultModel;
      } else if (models.length > 0) {
        selected = models[0];
      }

      const index = selected ? models.indexOf(selected) : -1;
      statusSelect.setOptions(models, index);

      if (selected && AppSettings.model !== selected) {
        AppSettings.model = selected;
      }
      refreshReasoning();
    });

    AppSettings.observables.onStatusChanged.subscribe(status => {
      this.updateStatus(status);
    });

    AppSettings.observables.onContextsLoaded.subscribe(contexts => {
      availableContexts = contexts;
      contextSelect.setOptions(['None'].concat(contexts.map(c => c.name)), 0);
    })

    return mainRow;
  }

  private updateStatus(status: RunState) {
    if (this.currentStatus)
      this.statusPill.component().classList.remove(this.currentStatus.toLowerCase());

    this.statusPill.setText(`Status: ${status}`);
    this.statusPill.component().classList.add(status.toLowerCase());
    this.currentStatus = status;
  }

  // --- subagent status row ---

  private setupSubagents() {
    AppSettings.observables.onSubagentEvent.subscribe(ev => {
      this.handleSubagentEvent(ev);
    });

    // Stamped run-state transitions refine an entry's live label.
    AppSettings.observables.onSubagentState.subscribe(ev => {
      const entry = ev.agent_id ? this.subagents.get(ev.agent_id) : undefined;
      if (entry) {
        entry.state = ev.state;
        this.renderSubagentRow();
      }
    });

    // 1s timer refreshes elapsed times and fades terminal entries out after
    // their linger window.
    window.setInterval(() => {
      const now = Date.now();
      let changed = false;
      for (const [id, entry] of this.subagents) {
        if (isTerminal(entry.status) && entry.endedAt !== null &&
            now - entry.endedAt > SUBAGENT_LINGER_MS) {
          this.subagents.delete(id);
          changed = true;
        }
      }
      if (changed || this.subagents.size > 0) {
        this.renderSubagentRow();
      }
    }, 1000);
  }

  private handleSubagentEvent(ev: SubagentEvent) {
    const entry: SubagentEntry = this.subagents.get(ev.id) ?? {
      id: ev.id,
      status: 'queued',
      state: '',
      startedAt: null,
      endedAt: null,
    };
    entry.name = ev.name;
    if (ev.color) entry.color = ev.color;
    entry.status = ev.status as SubStatus;
    if (ev.started_at) {
      const t = new Date(ev.started_at).getTime();
      if (!Number.isNaN(t)) entry.startedAt = t;
    }
    if (isTerminal(entry.status)) {
      const t = ev.ended_at ? new Date(ev.ended_at).getTime() : Date.now();
      entry.endedAt = Number.isNaN(t) ? Date.now() : t;
    }
    this.subagents.set(ev.id, entry);
    this.renderSubagentRow();
  }

  private renderSubagentRow() {
    const entries = [...this.subagents.values()].sort((a, b) => seqNum(a.id) - seqNum(b.id));

    if (entries.length === 0) {
      this.subagentRow.classList.add('hidden');
      this.subagentRow.innerHTML = '';
      return;
    }
    this.subagentRow.classList.remove('hidden');

    const pills: HTMLElement[] = entries.map(entry => {
      const pill = document.createElement('div');
      pill.className = `status-pill subagent-pill ${subagentCssClass(entry)}`;
      if (entry.name) pill.title = entry.name;
      if (entry.color) {
        const dot = document.createElement('span');
        dot.className = 'subagent-dot';
        dot.style.background = entry.color;
        pill.appendChild(dot);
      }
      let label = `${entry.id} ${subagentLabel(entry)}`;
      const elapsed = formatSubagentElapsed(entry);
      if (elapsed) label += ` ${elapsed}`;
      const text = document.createElement('span');
      text.textContent = ' ' + label;
      pill.appendChild(text);
      return pill;
    });

    this.subagentRow.innerHTML = '';
    for (const pill of pills) {
      this.subagentRow.appendChild(pill);
    }

    // Trim from the tail while the content overflows (keeping at least one
    // pill and reserving room for the overflow pill). Skipped when the row
    // has not been laid out yet.
    let hiddenCount = 0;
    if (this.subagentRow.clientWidth > 0) {
      while (this.subagentRow.scrollWidth > this.subagentRow.clientWidth - MORE_PILL_RESERVE &&
             this.subagentRow.children.length > 1) {
        this.subagentRow.removeChild(this.subagentRow.lastElementChild as HTMLElement);
        hiddenCount = pills.length - this.subagentRow.children.length;
      }
    }

    if (hiddenCount > 0) {
      const morePill = document.createElement('div');
      morePill.className = 'status-pill subagent-pill queued';
      morePill.textContent = `+${hiddenCount} more`;
      this.subagentRow.appendChild(morePill);
    }
  }
}
