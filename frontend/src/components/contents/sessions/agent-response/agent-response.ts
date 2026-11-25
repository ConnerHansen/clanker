import { Subscription } from "rxjs";
import { AppSettings, ToolCallEvent, ToolStartInfo } from "../../../../services/app-settings";
import { MarkdownViewer } from "@services/markdown-viewer";
import { Component } from "@components/component";
import { data } from "@wails/models";
import { ToolCallBlock } from "../tool-call-block/tool-call-block";
import './agent-response.css';

enum ThinkingVisibility { DEFAULT, HIDDEN, VISIBLE };

// The JSON envelope persisted for finished tool calls (agent.storedToolRow).
// `name` is required: parseStoredToolRow only returns a non-null row after
// validating typeof obj.name === 'string', so a parsed row always has one.
interface StoredToolRow {
  id?: string;
  name: string;
  success?: boolean;
  params?: Record<string, any>;
  result?: any;
}

function parseStoredToolRow(contents: string): StoredToolRow | null {
  if (!contents) return null;
  try {
    const obj = JSON.parse(contents);
    if (obj && typeof obj === 'object' && typeof obj.name === 'string') {
      return obj as StoredToolRow;
    }
  } catch {
    // Not the tool envelope shape — treat as plain text.
  }
  return null;
}

export class AgentResponse implements Component {
  private element: HTMLDivElement;
  private thinkingToggle: HTMLSpanElement;
  private thinkingOutput: HTMLDivElement;
  private textOutput: HTMLDivElement;

  private active: boolean = true;
  private bodyText: string = "";
  private collapsed: boolean = true;
  private markdownViewer = new MarkdownViewer();
  private showThinking = ThinkingVisibility.DEFAULT;
  private subscription?: Subscription;
  // Tool-event subscriptions (live mode only); all unsubscribed on destroy so
  // a finished response never receives the next turn's tool calls.
  private toolSubscriptions: Subscription[] = [];
  private thinkingText: string = "";

  // Augmented: View state & floating controls
  private viewMode: 'md' | 'raw' = 'md';
  private controls: HTMLDivElement;
  private toggleBtn: HTMLButtonElement;
  private copyBtn: HTMLButtonElement;

  // Live tool-call blocks, keyed by the agent's tool call id so a finished
  // summary can flip its pending block in place.
  private pendingToolBlocks = new Map<string, ToolCallBlock>();

  constructor(private message?: data.Message) {
    this.element = document.createElement('div');
    this.thinkingToggle = document.createElement('span');
    this.thinkingOutput = document.createElement('div');
    this.textOutput = document.createElement('div');

    // Add classes
    this.element.classList.add('agent-output', 'view');
    this.thinkingToggle.classList.add('toggle', 'hidden');
    this.thinkingOutput.classList.add('thinking', 'hidden');

    // Build floating controls container
    this.controls = document.createElement('div');
    this.controls.classList.add('response-controls');

    this.toggleBtn = document.createElement('button');
    this.toggleBtn.classList.add('view-toggle');
    this.toggleBtn.textContent = 'MD';
    this.toggleBtn.onclick = () => this.toggleViewMode();

    this.copyBtn = document.createElement('button');
    this.copyBtn.classList.add('copy-btn');
    this.copyBtn.innerHTML = '<i class="mdi mdi-content-copy"></i>';
    this.copyBtn.onclick = () => this.copyToClipboard();

    this.controls.append(this.toggleBtn, this.copyBtn);

    // All static elements go in the DOM before any message rendering so that
    // stored/live tool blocks can anchor to textOutput via insertBefore.
    this.element.append(this.controls, this.thinkingToggle, this.thinkingOutput, this.textOutput);

    if (this.message) {
      this.active = false;
      this.renderStoredMessage(this.message);
    } else {
      // Live response: hidden until the first chunk arrives.
      this.textOutput.classList.add('response', 'raw');
      this.element.classList.add('hidden');
      this.textOutput.classList.add('hidden');

      this.render();
      this.thinkingToggle.onclick = () => {
        this.showThinking = (this.showThinking == ThinkingVisibility.VISIBLE) ?
          ThinkingVisibility.HIDDEN : ThinkingVisibility.VISIBLE;
        this.render();
      }

      this.subscription = AppSettings.observables.onAgentMessage.subscribe(message => {
        if (this.collapsed) {
          this.collapsed = false;
          this.element.classList.remove('hidden');
        }
        if (message.eol) {
          this.active = false;
          this.destroy();
          this.updateViewMode();
        } else if (message.thinking) {
          if (this.thinkingText == "") {
            this.thinkingOutput.classList.remove('hidden');
            this.thinkingToggle.classList.remove('hidden');
          }
          this.thinkingText += message.data;
          this.thinkingOutput.textContent = this.thinkingText;
        } else {
          if (this.showThinking == ThinkingVisibility.DEFAULT && this.thinkingText.length > 0 && this.bodyText.length == 0) {
            this.showThinking = ThinkingVisibility.HIDDEN;
            this.render();
          }

          if (this.bodyText.length == 0) {
            this.textOutput.classList.remove('hidden');
          }

          this.bodyText += message.data;
          this.textOutput.textContent = this.bodyText;
        }
      });

      // Live tool-call lifecycle: a start event inserts a pending block, the
      // matching summary flips it to completed with the tool's self-summary.
      this.toolSubscriptions.push(
        AppSettings.observables.onToolStart.subscribe((info: ToolStartInfo) => {
          const block = new ToolCallBlock(info.name, info.arguments);
          if (info.id) {
            this.pendingToolBlocks.set(info.id, block);
            block.component().setAttribute('data-tool-id', info.id);
          }
          this.element.insertBefore(block.component(), this.textOutput);
        }),
        AppSettings.observables.onToolCall.subscribe((event: ToolCallEvent) => {
          const block = event.id ? this.pendingToolBlocks.get(event.id) : undefined;
          if (block) {
            this.markBlockComplete(block, event.success, event.resultPreview);
            this.pendingToolBlocks.delete(event.id);
          } else {
            // Missed the start (e.g. it arrived before this view mounted):
            // insert a completed block just before the text output.
            const late = new ToolCallBlock(event.name, event.arguments);
            if (event.id) {
              late.component().setAttribute('data-tool-id', event.id);
            }
            this.markBlockComplete(late, event.success, event.resultPreview);
            this.element.insertBefore(late.component(), this.textOutput);
          }
        })
      );
    }
  }

  private markBlockComplete(block: ToolCallBlock, success: boolean, resultPreview?: string) {
    block.complete(success, resultPreview);
    block.component().classList.remove('pending');
    block.component().classList.add('completed', success ? 'ok' : 'failed');
  }

  /**
   * Renders a stored message. Tool rows become completed ToolCallBlocks using
   * messages.summary (falling back to a clipped raw result for legacy rows) —
   * the same display live calls get; text rows render as markdown. An
   * assistant's requested tool calls are rendered by the paired stored tool
   * rows that follow in row order, so nothing is faked or duplicated here.
   */
  private renderStoredMessage(message: data.Message) {
    if (message.Source === 'tool') {
      const toolRow = parseStoredToolRow(message.Contents);

      if (toolRow) {
        // Legacy rows may predate messages.summary: fall back to a clipped raw
        // result so nothing renders blank.
        let preview = message.Summary || '';
        if (!preview && typeof toolRow.result === 'string' && toolRow.result.trim() !== '') {
          const clipped = toolRow.result.replace(/\s+/g, ' ').trim();
          preview = clipped.length > 200 ? clipped.slice(0, 199) + '…' : clipped;
        }

        const block = new ToolCallBlock(toolRow.name, toolRow.params);
        if (toolRow.id) {
          block.component().setAttribute('data-tool-id', toolRow.id);
        }
        this.markBlockComplete(block, toolRow.success !== false, preview);
        this.element.insertBefore(block.component(), this.textOutput);
      } else {
        // Unrecognized legacy content: bounded raw display.
        this.textOutput.classList.remove('hidden');
        this.textOutput.classList.add('response', 'raw');
        const clipped = message.Contents.replace(/\s+/g, ' ').trim();
        this.bodyText = clipped.length > 200 ? clipped.slice(0, 199) + '…' : clipped;
        this.textOutput.textContent = this.bodyText;
      }
      return;
    }

    // user / assistant rows: markdown text.
    this.textOutput.classList.remove('hidden');
    this.textOutput.classList.add('response');
    this.bodyText = message.Contents || '';
    if (this.bodyText.trim() !== '') {
      this.markdownViewer.renderMarkdown(this.bodyText, this.textOutput);
    }
  }

  // Syncs DOM to view mode
  private updateViewMode() {
    if (!this.textOutput) return;
    if (this.active) return;

    const isToolRow = !!this.message && this.message.Source === 'tool' &&
      parseStoredToolRow(this.message.Contents) !== null;
    // Tool rows have no text body to toggle.
    if (isToolRow || this.bodyText.trim() === '') return;

    if (this.viewMode === 'raw') {
      this.textOutput.classList.add('raw');
      this.textOutput.textContent = this.bodyText;
    } else {
      this.markdownViewer.renderMarkdown(this.bodyText, this.textOutput);
    }
  }

  // Toggles between MD and Raw modes
  private toggleViewMode() {
    this.viewMode = this.viewMode === 'md' ? 'raw' : 'md';
    this.toggleBtn.textContent = this.viewMode.toUpperCase();
    this.updateViewMode();
  }

  // Copies body text to clipboard
  private copyToClipboard() {
    navigator.clipboard.writeText(this.bodyText).then(() => {
      this.copyBtn.classList.add('copied');
      this.copyBtn.innerHTML = '<i class="mdi mdi-check"></i>';
      setTimeout(() => {
        this.copyBtn.classList.remove('copied');
        this.copyBtn.innerHTML = '<i class="mdi mdi-content-copy"></i>';
      }, 1500);
    }).catch(console.error);
  }

  public component(): HTMLElement {
    return this.element;
  }

  public destroy() {
    this.subscription?.unsubscribe();
    this.toolSubscriptions.forEach(s => s.unsubscribe());
    this.toolSubscriptions = [];
  }

  private render() {
    if (this.showThinking == ThinkingVisibility.VISIBLE) {
      this.thinkingToggle.textContent = '▾ [Hide Thought Details]';
      this.thinkingOutput.classList.remove('hidden');
    } else {
      this.thinkingToggle.textContent = '▸ [Show Thought Details]';
      this.thinkingOutput.classList.add('hidden');
    }
  }
}
