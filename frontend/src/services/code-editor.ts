import * as monaco from 'monaco-editor';

export class CodeEditor2 {
  constructor() { }

  public render(container: HTMLElement, initialContent: string = '', language: string = 'json'): monaco.editor.IStandaloneCodeEditor {
    return monaco.editor.create(container, {
      value: initialContent,
      language: language,
      theme: 'vs-dark',
      automaticLayout: true,
      minimap: { enabled: false },

      // Enable JSON specific features
      suggestOnTriggerCharacters: true,
      quickSuggestions: true,
    });
  }
}