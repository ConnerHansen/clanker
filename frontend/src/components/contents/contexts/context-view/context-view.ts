import { Component } from '@components/component';
import { ContextList } from '@components/contents/contexts/context-list/context-list';
import { PromptList } from '@components/contents/contexts/prompt-list/prompt-list';
import { ListContexts, ListPrompts } from '@wails/main/App';
import './context-view.css';

export class ContextView implements Component {
  container: HTMLElement;

  private contextListComponent = new ContextList();
  private promptListComponent = new PromptList();

  constructor() {
    this.container = document.createElement('div');
    this.container.className = 'context-view';

    this.refreshContexts();
    this.refreshPrompts();
    this.renderContent();
  }

  private renderContent(): void {
    const contentArea = document.createElement('div');
    contentArea.className = 'context-view-content';

    const contextSection = document.createElement('div');
    contextSection.className = 'section-wrapper';
    contextSection.appendChild(this.contextListComponent.component());

    const promptSection = document.createElement('div');
    promptSection.className = 'section-wrapper';
    promptSection.appendChild(this.promptListComponent.component());

    contentArea.appendChild(contextSection);
    contentArea.appendChild(promptSection);
    this.container.appendChild(contentArea);
  }

  private refreshContexts() {
    ListContexts().then(contexts => {
      console.log(contexts)
      this.contextListComponent.setContexts(contexts);
    });
  }

  private refreshPrompts() {
    ListPrompts().then(prompts => {
      this.promptListComponent.setPrompts(prompts);
    });
  }

  component(): HTMLElement {
    return this.container;
  }
}
