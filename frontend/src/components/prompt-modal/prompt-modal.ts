import { AppSettings, AskUserQuestion, AskUserRequest, PermissionRequest } from '@services/app-settings';
import { CancelAskRequest, ResolveAskRequest, ResolvePermissionRequest } from '@wails/main/App';
import './prompt-modal.css';

// PromptModal is the web counterpart of the TUI's prompt overlays: a dimmed,
// centered modal that renders one CARD PER QUESTION (ask requests) or one card
// per permission request, all stacked in a column. It subscribes to the
// 'ask-user-request' / 'permission-request' events emitted by app.go and
// resolves them through the same agent exits the TUI uses:
//   - submit    -> App.ResolveAskRequest(id, answers)  (TUI: ResolveAsk)
//   - cancel    -> App.CancelAskRequest(id)            (TUI: CancelAsk / esc)
//   - allow/deny-> App.ResolvePermissionRequest        (TUI: ResolvePermission)
//
// Multiple pending prompts queue in the column; resolving one removes just
// that group. Because the agent-side registry is first-response-wins, an
// answer from any client unblocks the run — the groundwork for later parallel
// UIs (CLI + web), where a broadcast of resolution events would dismiss the
// prompt everywhere without changing this component's logic.

type PendingGroup =
  | { kind: 'ask'; req: AskUserRequest; el: HTMLElement }
  | { kind: 'permission'; req: PermissionRequest; el: HTMLElement };

// Per-question UI state while the form is open. `picked` holds chosen option
// VALUES (not labels); `text` holds free-form entry, trimmed on submit.
interface QuestionState {
  picked: Set<string>;
  text: string;
}

export class PromptModal {
  private overlay: HTMLDivElement;
  private pane: HTMLDivElement;
  private groups: PendingGroup[] = [];

  constructor() {
    this.overlay = document.createElement('div');
    this.overlay.className = 'prompt-modal-overlay hidden';
    this.overlay.id = 'prompt-modal-overlay';

    this.pane = document.createElement('div');
    this.pane.className = 'prompt-modal-pane';
    this.overlay.appendChild(this.pane);
  }

  public run() {
    document.body.appendChild(this.overlay);

    AppSettings.observables.onAskRequest.subscribe(req => this.addAskGroup(req));
    AppSettings.observables.onPermissionRequest.subscribe(req => this.addPermissionGroup(req));

    // Esc dismisses a prompt, mirroring the TUI (cancel ask / deny
    // permission). It only acts while exactly one prompt is pending so a
    // queued batch can never be mass-dismissed by accident.
    document.addEventListener('keydown', event => {
      if (event.key !== 'Escape' || this.groups.length !== 1) return;
      this.dismissGroup(this.groups[0]);
    });
  }

  // ---------------------------------------------------------------------------
  // Group lifecycle

  private addAskGroup(req: AskUserRequest) {
    const el = document.createElement('div');
    el.className = 'prompt-group';
    el.dataset.promptId = req.id;

    el.appendChild(this.header('Agent Information Request'));

    const state = new Map<string, QuestionState>();
    for (const q of req.questions) {
      state.set(q.id, { picked: new Set(), text: '' });
      el.appendChild(this.askCard(q, state.get(q.id)!));
    }

    const actions = document.createElement('div');
    actions.className = 'prompt-group-actions';

    const cancelBtn = this.button('Cancel', 'btn-secondary');
    cancelBtn.addEventListener('click', () => {
      CancelAskRequest(req.id);
      this.removeGroup(req.id);
    });

    const submitBtn = this.button(
      `Submit ${req.questions.length} Answer${req.questions.length === 1 ? '' : 's'}`,
      'btn-primary',
    );
    submitBtn.addEventListener('click', () => {
      ResolveAskRequest(req.id, this.collectAnswers(req, state));
      this.removeGroup(req.id);
    });

    actions.append(cancelBtn, submitBtn);
    el.appendChild(actions);

    this.insertGroup({ kind: 'ask', req, el });
  }

  private addPermissionGroup(req: PermissionRequest) {
    const el = document.createElement('div');
    el.className = 'prompt-group';
    el.dataset.promptId = req.id;

    el.appendChild(this.header('Permission Required'));

    const card = document.createElement('div');
    card.className = `prompt-card prompt-card-permission level-${req.level}`;

    const title = document.createElement('div');
    title.className = 'prompt-q-prompt permission-title';
    title.textContent = req.prompt || `${req.action} ${req.path}`;
    card.appendChild(title);

    if (req.agent_name) {
      const sub = document.createElement('div');
      sub.className = 'prompt-q-context';
      sub.textContent = `requested by ${req.agent_name}`;
      card.appendChild(sub);
    }

    if (req.viewport && req.viewport.content) {
      const vp = req.viewport;
      // Header: tool-provided title, else derived from kind (TUI parity).
      const label = vp.title || (vp.kind === 'script' ? 'Command' : 'Diff Contents');
      const heading = document.createElement('div');
      heading.className = 'prompt-q-context';
      heading.textContent = label;
      card.appendChild(heading);

      const diff = document.createElement('pre');
      diff.className = 'prompt-diff';
      diff.textContent = vp.content;
      card.appendChild(diff);
    }

    el.appendChild(card);

    const actions = document.createElement('div');
    actions.className = 'prompt-group-actions';

    const denyBtn = this.button('Deny', 'btn-secondary');
    denyBtn.addEventListener('click', () => {
      ResolvePermissionRequest(req.id, false);
      this.removeGroup(req.id);
    });

    const allowBtn = this.button('Allow', 'btn-primary');
    allowBtn.addEventListener('click', () => {
      ResolvePermissionRequest(req.id, true);
      this.removeGroup(req.id);
    });

    actions.append(denyBtn, allowBtn);
    el.appendChild(actions);

    this.insertGroup({ kind: 'permission', req, el });
  }

  private insertGroup(group: PendingGroup) {
    this.groups.push(group);
    this.pane.appendChild(group.el);
    this.overlay.classList.remove('hidden');
    // Newest prompt is the one waiting on us: bring it into view and focus it.
    group.el.scrollIntoView({ block: 'nearest' });
    const focusable = group.el.querySelector<HTMLElement>('.editor-input, .prompt-option-row, .btn-primary');
    focusable?.focus();
  }

  private removeGroup(id: string) {
    const idx = this.groups.findIndex(g => g.req.id === id);
    if (idx < 0) return; // already resolved elsewhere (e.g. another client answered it)
    this.groups.splice(idx, 1)[0].el.remove();
    if (this.groups.length === 0) {
      this.overlay.classList.add('hidden');
    }
  }

  // Esc path: cancel an ask form, deny a permission request.
  private dismissGroup(group: PendingGroup) {
    if (group.kind === 'ask') {
      CancelAskRequest(group.req.id);
    } else {
      ResolvePermissionRequest(group.req.id, false);
    }
    this.removeGroup(group.req.id);
  }

  // ---------------------------------------------------------------------------
  // Card builders

  private header(text: string): HTMLElement {
    const h = document.createElement('div');
    h.className = 'prompt-group-header';
    h.textContent = text;
    return h;
  }

  private button(label: string, kind: string): HTMLButtonElement {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = `btn ${kind}`;
    b.textContent = label;
    return b;
  }

  // One card per question (TUI parity: prompt + context, then options or a
  // text field). Option rows are buttons so they stay keyboard-accessible.
  private askCard(q: AskUserQuestion, state: QuestionState): HTMLElement {
    const card = document.createElement('div');
    card.className = 'prompt-card';

    const prompt = document.createElement('div');
    prompt.className = 'prompt-q-prompt';
    prompt.textContent = q.prompt;
    card.appendChild(prompt);

    if (q.context) {
      const ctx = document.createElement('div');
      ctx.className = 'prompt-q-context';
      ctx.textContent = q.context;
      card.appendChild(ctx);
    }

    const isMulti = q.type === 'multi-select';
    const hasOptions = (q.options ?? []).length > 0;
    // allow_free_form rides the wire with Go omitempty: key PRESENT means
    // enabled, absent means the model explicitly opted out. Free-form entry
    // is an option on choice questions only; free-response renders a bare
    // text field.
    const hasFreeForm = q.allow_free_form === true && (q.type === 'select' || q.type === 'confirm' || q.type === 'multi-select');

    let input: HTMLInputElement | undefined;
    let freeRow: HTMLButtonElement | undefined;

    // `markOptionState` re-renders the ○/● or □/■ marks and the "type your
    // own" highlight after any change to this question's answer.
    const markOptionState = () => {
      const rows = card.querySelectorAll<HTMLButtonElement>('.prompt-option-row');
      let i = 0;
      for (const opt of q.options ?? []) {
        const row = rows[i++];
        if (!row) continue;
        const picked = state.picked.has(opt.value);
        row.classList.toggle('selected', picked);
        row.querySelector('.prompt-option-mark')!.textContent = isMulti ? (picked ? '■' : '□') : (picked ? '●' : '○');
      }
      if (freeRow && input) {
        freeRow.classList.toggle('selected', hasOptions && input.value.trim() !== '');
      }
    };

    if (q.type === 'free-response' || hasFreeForm) {
      input = document.createElement('input');
      input.type = 'text';
      input.className = 'editor-input prompt-input';
      input.placeholder = q.placeholder || (hasFreeForm ? 'Type your own answer…' : 'Your answer');
      if (q.type !== 'free-response') {
        input.classList.add('hidden'); // revealed by the "type your own" row
      }
      input.addEventListener('input', () => {
        state.text = input!.value;
        // Typing a custom answer supersedes picked options on single-choice
        // questions (mirrors the TUI: entering free-form clears selection).
        if (!isMulti) state.picked.clear();
        markOptionState();
      });
      input.addEventListener('keydown', event => {
        // Enter commits the form (TUI: enter submits / reaches confirm).
        if (event.key === 'Enter') {
          event.preventDefault();
          card.closest('.prompt-group')?.querySelector<HTMLButtonElement>('.btn-primary')?.click();
        }
      });
    }

    for (const opt of q.options ?? []) {
      const row = document.createElement('button');
      row.type = 'button';
      row.className = 'prompt-option-row';

      const mark = document.createElement('span');
      mark.className = 'prompt-option-mark';
      row.appendChild(mark);

      const label = document.createElement('span');
      label.className = 'prompt-option-label';
      label.textContent = opt.label;
      row.appendChild(label);

      row.addEventListener('click', () => {
        if (isMulti) {
          // Multi-select: click toggles the option (TUI: enter toggles).
          state.picked.has(opt.value) ? state.picked.delete(opt.value) : state.picked.add(opt.value);
        } else if (state.picked.size === 1 && state.picked.has(opt.value)) {
          // Single choice: clicking the selected row toggles it off so the
          // user can switch to a typed answer.
          state.picked.clear();
        } else {
          state.picked.clear();
          state.picked.add(opt.value);
        }
        // Picking an option hands the answer back to the options list.
        if (input) input.classList.add('hidden');
        markOptionState();
      });

      card.appendChild(row);
    }

    if (hasFreeForm && input) {
      freeRow = document.createElement('button');
      freeRow.type = 'button';
      freeRow.className = 'prompt-option-row prompt-freeform-row';
      freeRow.textContent = '✎ Type your own answer…';
      freeRow.addEventListener('click', () => {
        input!.classList.remove('hidden');
        if (!isMulti) state.picked.clear();
        markOptionState();
        input!.focus();
      });
      card.appendChild(freeRow);
    }

    if (input) {
      card.appendChild(input);
      state.text = input.value;
    }

    markOptionState();
    return card;
  }

  // collectAnswers mirrors the TUI's AskState.collectAnswers exactly:
  // multi-select values are comma-joined in OPTION order with typed text
  // appended last; on single-choice a picked option wins over typed text and
  // an empty answer omits the key entirely; free-response is plain trimmed
  // text (always present).
  private collectAnswers(req: AskUserRequest, state: Map<string, QuestionState>): Record<string, string> {
    const answers: Record<string, string> = {};
    for (const q of req.questions) {
      const s = state.get(q.id)!;
      const text = s.text.trim();
      switch (q.type) {
        case 'multi-select': {
          const vals: string[] = [];
          for (const opt of q.options ?? []) {
            if (s.picked.has(opt.value)) vals.push(opt.value);
          }
          if (text !== '') vals.push(text);
          answers[q.id] = vals.join(',');
          break;
        }
        case 'select':
        case 'confirm': {
          let val = '';
          for (const opt of q.options ?? []) {
            if (s.picked.has(opt.value)) {
              val = opt.value;
              break;
            }
          }
          if (val === '') val = text;
          if (val !== '') answers[q.id] = val;
          break;
        }
        default: // free-response
          answers[q.id] = text;
      }
    }
    return answers;
  }
}
