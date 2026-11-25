import "@mdi/font/css/materialdesignicons.css";
import { DeletePrompt, LoadPrompt, SavePrompt } from "@wails/main/App";
import { data } from "@wails/models";
import { Component } from "@components/component";
import "./prompt-list.css";
import { PromptEditor } from "../prompt-editor/prompt-editor";
import { requestView } from "@services/app-settings";
import { TreeView } from "../tree-view/tree-view";

export class PromptList implements Component {
  private wrapper: HTMLElement;
  private listContainer: HTMLElement;
  private activePrompt: data.Prompt | undefined;
  private prompts: data.Prompt[] = [];
  private promptRows = new Map<data.Prompt, HTMLDivElement>();
  private editor = new PromptEditor((prompt) => { this.savePrompt(prompt) }, () => { requestView(undefined); });

  constructor() {
    this.wrapper = document.createElement('div');
    this.wrapper.classList.add('context-wrapper');
    this.wrapper.style.height = '100%';

    this.listContainer = document.createElement('div');
    this.listContainer.classList.add('scrollable-list');
    this.listContainer.style.overflowY = 'auto';
    this.wrapper.appendChild(this.listContainer);

    this.renderCreateButton();
  }

  private renderCreateButton(): void {
    const header = document.createElement('div');
    header.className = 'left-view-header';

    const createBtn = document.createElement('div');
    createBtn.className = 'create-item-btn';
    createBtn.id = 'create-prompt';

    const icon = document.createElement('i');
    icon.className = 'mdi mdi-plus-circle create-item-icon';

    const text = document.createElement('span');
    text.className = 'create-item-text';
    text.textContent = 'New Prompt';

    createBtn.appendChild(icon);
    createBtn.appendChild(text);

    createBtn.addEventListener('click', () => {
      this.editPrompt({ name: 'example.prompt', content: '# New Prompt' } as data.Prompt);
    });

    header.append(createBtn);

    this.wrapper.insertBefore(header, this.listContainer);
  }

  private async savePrompt(prompt: data.Prompt) {
    const existing = prompt.id === undefined ? false : true;
    SavePrompt(prompt)
      .then(prompt => {
        if (!existing)
          this.pushPrompt(prompt);
      })
  }

  public editPrompt(prompt: data.Prompt) {
    this.editor.render(prompt);
    requestView(this.editor.component());
  }


  public loadPrompt(prompt: data.Prompt) {
    if (this.activePrompt) {
      this.promptRows.get(this.activePrompt)?.classList.remove('active');
    }
    this.promptRows.get(prompt)?.classList.add('active');
    this.activePrompt = prompt;

    LoadPrompt(prompt)
      .then(prompt => {
        this.editPrompt(prompt);
      });
  }

  public async deletePrompt(prompt: data.Prompt) {
    await DeletePrompt(prompt);
    this.promptRows.get(prompt)?.remove();
    this.promptRows.delete(prompt);
  }

  public pushPrompt(prompt: data.Prompt) {
    this.prompts.unshift(prompt);
    this.setPrompts(this.prompts);
    this.loadPrompt(prompt);
  }

  public setPrompts(prompts: data.Prompt[]): void {
    console.log(prompts)
    this.render(prompts);
  }

  public component(): HTMLElement {
    return this.wrapper;
  }

  private render(prompts: data.Prompt[]): void {
    this.listContainer.innerHTML = '';
    this.prompts = prompts;

    this.listContainer.innerHTML = '';
    this.prompts = prompts;

    const treeView = new TreeView();
    treeView.processList(this.prompts.map(prompt => {
      return {
        name: prompt.name, callback: () => {
          console.log(prompt);
          this.loadPrompt(prompt);
        }
      };
    }));

    this.listContainer.append(treeView.component());

    // this.promptRows.clear();

    // for (const prompt of prompts) {
    //   const row = document.createElement('div');
    //   row.classList.add('prompt-row', 'left-btn');

    //   const idCell = document.createElement('div');
    //   idCell.classList.add('id');
    //   idCell.textContent = String(prompt.id);

    //   const nameCell = document.createElement('div');
    //   nameCell.classList.add('name');
    //   nameCell.textContent = prompt.name || 'Untitled Prompt';

    //   this.promptRows.set(prompt, row);

    //   row.appendChild(idCell);
    //   row.appendChild(nameCell);

    //   const trashIcon = document.createElement("span");
    //   trashIcon.classList.add("trash-icon", "mdi", "mdi-trash-can");
    //   trashIcon.setAttribute("title", "Delete prompt");
    //   trashIcon.addEventListener("click", (e) => {
    //     e.stopPropagation();
    //     this.deletePrompt(prompt);
    //   });
    //   row.appendChild(trashIcon);

    //   row.addEventListener('click', () => this.loadPrompt(prompt));
    //   this.listContainer.appendChild(row);
    // }
  }
}
