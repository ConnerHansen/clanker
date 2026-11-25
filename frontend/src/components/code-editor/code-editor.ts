import * as monaco from 'monaco-editor';
import { Component } from "../component";
import './code-editor.css';

export class CodeEditor implements Component {
  private editor: monaco.editor.IStandaloneCodeEditor;
  private container: HTMLElement;

  constructor(private initialContent: string = '', private language: string = 'json') {
    this.container = document.createElement('div');
    this.container.className = 'code-editor__root';
    this.container.style.width = '100%';
    this.container.style.height = '100%';

    // Monaco requires the container to exist in the DOM before initialization
    document.body.appendChild(this.container);

    this.editor = monaco.editor.create(this.container, {
      value: this.initialContent,
      language: this.language,
      theme: 'vs-dark',
      automaticLayout: true,
      minimap: { enabled: false },
      // Enable JSON specific features
      suggestOnTriggerCharacters: true,
      quickSuggestions: true,
    });
  }

  public component(): HTMLElement {
    // Monaco manages its own DOM, so we return the root container
    return this.container;
  }

  public setContent(content: string): void {
    this.editor.setValue(content);
  }

  public getContent(): string {
    return this.editor.getValue();
  }
}