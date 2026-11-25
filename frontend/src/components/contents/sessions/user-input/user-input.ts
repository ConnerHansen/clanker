import { Observable, Subject } from 'rxjs';
import './user-input.css';
import { Component } from 'components/component';
import { MarkdownViewer } from 'services/markdown-viewer';
import { Interrupt } from '../../../../../wailsjs/go/main/App';
import { requestPrompt } from '@services/app-settings';
import { data } from '@wails/models';

export class UserInput implements Component {
  private element: HTMLDivElement;
  private textContainer: HTMLDivElement;
  private emitter = new Subject<void>();
  private content: string = '';
  private markdownViewer = new MarkdownViewer();

  // Controls & View State
  private controls: HTMLDivElement;
  private toggleBtn: HTMLButtonElement;
  private copyBtn: HTMLButtonElement;
  private replayBtn: HTMLButtonElement;
  private viewMode: 'md' | 'raw' = 'md';
  private editable: boolean = true;

  constructor(private message?: data.Message) {
    this.element = document.createElement('div');
    // this.element.setAttribute('placeholder', 'Invoke the Clankstar Runner');
    this.element.classList.add('user-input');

    this.textContainer = document.createElement('div');
    this.textContainer.setAttribute('placeholder', 'Invoke the Clankstar Runner');
    this.textContainer.classList.add('user-input-container');

    // Build floating controls container
    this.controls = document.createElement('div');
    this.controls.classList.add('user-controls');

    this.toggleBtn = document.createElement('button');
    this.toggleBtn.classList.add('view-toggle');
    this.toggleBtn.textContent = 'MD';
    this.toggleBtn.onclick = () => this.toggleViewMode();

    this.copyBtn = document.createElement('button');
    this.copyBtn.classList.add('copy-btn');
    this.copyBtn.innerHTML = '<i class="mdi mdi-content-copy"></i>';
    this.copyBtn.onclick = () => this.copyToClipboard();

    this.replayBtn = document.createElement('button');
    this.replayBtn.classList.add('replay-btn');
    this.replayBtn.innerHTML = '<i class="mdi mdi-rotate-right"></i>';
    this.replayBtn.onclick = () => this.replay();

    this.controls.append(this.toggleBtn, this.copyBtn, this.replayBtn);
    this.controls.classList.add('hidden');

    this.element.append(this.controls, this.textContainer);

    if (this.message) {
      this.editable = false;
      this.content = this.message.Contents;
      this.freeze();
    } else {
      this.textContainer.setAttribute('contenteditable', 'plaintext-only');
      this.textContainer.classList.add('active');

      this.textContainer.onkeydown = (ev: KeyboardEvent) => {
        if (this.isSubmitEvent(ev)) {
          let content = this.textContainer.innerText;
          if (content == "") {
            console.log(":(")
          } else {
            console.log('SEND TO THE MOTHERSHIP');
            this.freeze();
            requestPrompt(this.content);
          }
          ev.preventDefault();
        } else if (this.isInterruptEvent(ev)) {
          Interrupt()
            .then(() => console.log('interrupt'))
            .catch(err => console.log(err));
        }
      };
    }
  }

  public getText(): string {
    return this.textContainer.innerText || '';
  }

  public isEditable(): boolean {
    return this.editable;
  }

  public onFreeze(): Observable<void> {
    return this.emitter.asObservable();
  }

  public component(): HTMLElement {
    return this.element;
  }

  private freeze() {
    if (this.editable) {
      this.content = this.textContainer.innerText || '';
      this.textContainer.removeAttribute('contenteditable');
      this.textContainer.classList.remove('active');
      this.editable = false;
      this.textContainer.onkeydown = () => { };
      this.emitter.next();
    }

    this.controls.classList.remove('hidden');

    this.markdownViewer.renderMarkdown(this.content, this.textContainer);
    this.updateViewMode();
  }

  private toggleViewMode() {
    this.viewMode = this.viewMode === 'md' ? 'raw' : 'md';
    this.toggleBtn.textContent = this.viewMode.toUpperCase();
    this.updateViewMode();
  }

  private updateViewMode() {
    if (this.editable) return;

    if (this.viewMode === 'raw') {
      this.textContainer.classList.add('raw');
      this.textContainer.textContent = this.content;
    } else {
      this.textContainer.classList.remove('raw');
      this.markdownViewer.renderMarkdown(this.content, this.textContainer);
    }
  }

  private copyToClipboard() {
    navigator.clipboard.writeText(this.content).then(() => {
      this.copyBtn.classList.add('copied');
      this.copyBtn.innerHTML = '<i class="mdi mdi-check"></i>';
      setTimeout(() => {
        this.copyBtn.classList.remove('copied');
        this.copyBtn.innerHTML = '<i class="mdi mdi-content-copy"></i>';
      }, 1500);
    }).catch(console.error);
  }

  private replay() {
    // this.element.textContent = '';
    // this.element.setAttribute('contenteditable', 'plaintext-only');
    // this.element.classList.add('active');
    // this.element.classList.remove('raw');
    // this.isEditable = true;
    // this.viewMode = 'md';
    // this.toggleBtn.textContent = 'MD';
    // this.copyBtn.classList.remove('copied');
    // this.copyBtn.innerHTML = '<i class="mdi mdi-content-copy"></i>';
    // this.element.focus();
    this.emitter.next();
    requestPrompt("");
  }

  private isSubmitEvent(ev: KeyboardEvent): boolean {
    return ev.shiftKey && ev.key == 'Enter';
  }

  private isInterruptEvent(ev: KeyboardEvent): boolean {
    return ev.key == 'Escape';
  }
}
