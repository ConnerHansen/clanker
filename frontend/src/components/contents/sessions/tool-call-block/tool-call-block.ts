import { Component } from "@components/component";
import './tool-call-block.css';

// Bounded one-line preview for parameter values, matching the TUI's
// maxPreviewStringLen clipping so live and terminal views read alike.
const PARAM_VALUE_LIMIT = 160;

function formatParamValue(v: any): string {
  if (typeof v === 'string') {
    const escaped = JSON.stringify(v.replace(/\n/g, '\\n'));
    return escaped.length <= PARAM_VALUE_LIMIT ? escaped : escaped.slice(0, PARAM_VALUE_LIMIT - 1) + '…';
  }
  if (v === null || v === undefined) {
    return String(v);
  }
  try {
    const s = JSON.stringify(v);
    return s.length <= PARAM_VALUE_LIMIT ? s : s.slice(0, PARAM_VALUE_LIMIT - 1) + '…';
  } catch {
    return String(v);
  }
}

function formatParamsPreview(args?: Record<string, any>): string {
  if (!args || Object.keys(args).length === 0) return '';
  return Object.entries(args)
    .map(([k, v]) => `${k}=${formatParamValue(v)}`)
    .join(', ');
}

// ToolCallBlock renders one tool call the way the TUI's toolCallBlock does:
//   Tool: <name>          (green/red by success)
//    ├ Status: succeeded | failed | running…
//    ├ Parameters: k=v, … (bounded preview)
//    └ Results: <one-line self-summary>
// It starts in the pending state (from a tool-start event) and flips to the
// completed state when its matching tool-call summary arrives.
function canonicalName(name: string): string {
  // Legacy dotted names (pre-rebrand) are normalized for display.
  return name.includes('.') ? name.replace(/\./g, '-') : name;
}

export class ToolCallBlock implements Component {
  private element: HTMLDivElement;
  private statusEl: HTMLSpanElement;
  private resultsRowEl: HTMLDivElement | null = null;
  private resultsEl: HTMLSpanElement;

  constructor(name: string, args?: Record<string, any>) {
    this.element = document.createElement('div');
    this.element.classList.add('tool-call-block', 'pending');

    const title = document.createElement('div');
    title.classList.add('tc-title', 'running');
    title.textContent = `Tool: ${canonicalName(name)}`;
    this.element.appendChild(title);

    const statusRow = document.createElement('div');
    statusRow.classList.add('tc-row');
    statusRow.innerHTML = ' ├ Status: <span class="tc-status running">running…</span>';
    this.statusEl = statusRow.querySelector('.tc-status')!;
    this.element.appendChild(statusRow);

    if (args && Object.keys(args).length > 0) {
      const paramsRow = document.createElement('div');
      paramsRow.classList.add('tc-row', 'tc-params');
      paramsRow.textContent = ' ├ Parameters: ' + formatParamsPreview(args);
      this.element.appendChild(paramsRow);
    }

    this.resultsRowEl = document.createElement('div');
    this.resultsRowEl.classList.add('tc-row', 'hidden');
    this.resultsEl = document.createElement('span');
    this.resultsEl.classList.add('tc-results');
    this.resultsRowEl.appendChild(this.resultsEl);
    this.element.appendChild(this.resultsRowEl);
  }

  component(): HTMLElement {
    return this.element;
  }

  /** Flip from pending to completed with the summary line. */
  complete(success: boolean, resultPreview?: string) {
    const title = this.element.querySelector('.tc-title')!;
    title.classList.remove('running');
    title.classList.add(success ? 'ok' : 'failed');

    this.statusEl.classList.remove('running');
    this.statusEl.classList.add(success ? 'ok' : 'failed');
    this.statusEl.textContent = success ? 'succeeded' : 'failed';

    const preview = (resultPreview || '').trim();
    if (preview && this.resultsRowEl) {
      this.resultsEl.textContent = preview;
      this.resultsRowEl.classList.remove('hidden');
    }
  }

  destroy() {}
}
