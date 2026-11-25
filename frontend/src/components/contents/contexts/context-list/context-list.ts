import "@mdi/font/css/materialdesignicons.css";
import { SaveContext, DeleteContext, LoadContext } from "@wails/main/App";
import { data } from "@wails/models";
import { Component } from "../../../component";
import "./context-list.css";
import { ContextEditor } from "../context-editor/context-editor";
import { requestView } from "@services/app-settings";
import { TreeView } from "../tree-view/tree-view";

export class ContextList implements Component {
  private wrapper: HTMLElement;
  private listContainer: HTMLElement;
  private activeContext: data.Context | undefined;
  private contexts: data.Context[] = [];
  private contextRows = new Map<data.Context, HTMLDivElement>();
  private editor = new ContextEditor((context) => { this.saveContext(context) }, () => { requestView(undefined); });

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
    createBtn.id = 'create-context';

    const icon = document.createElement('i');
    icon.className = 'mdi mdi-plus-circle create-item-icon';

    const text = document.createElement('span');
    text.className = 'create-item-text';
    text.textContent = 'New Context';

    createBtn.appendChild(icon);
    createBtn.appendChild(text);

    createBtn.addEventListener('click', () => {
      this.editContext();
    });

    header.append(createBtn);

    this.wrapper.insertBefore(header, this.listContainer);
  }

  private async saveContext(context: data.Context) {
    console.log("Saving", context);
    const existing = context.id === undefined ? false : true;
    SaveContext(context)
      .then(context => {
        if (!existing) {
          this.pushContext(context);
        }

        this.loadContext(context);
      })
  }

  public editContext(context?: data.Context) {
    console.log(context);
    this.editor.render(context);
    requestView(this.editor.component());
  }

  public loadContext(context: data.Context) {
    if (this.activeContext) {
      this.contextRows.get(this.activeContext)?.classList.remove('active');
    }
    this.contextRows.get(context)?.classList.add('active');
    LoadContext(context)
      .then(context => {
        this.activeContext = context;
        this.editContext(context);
      });
  }

  public async deleteContext(context: data.Context) {
    await DeleteContext(context);
    this.contextRows.get(context)?.remove();
    this.contextRows.delete(context);
  }

  public pushContext(context: data.Context) {
    this.contexts.unshift(context);
    this.setContexts(this.contexts);
    this.loadContext(context);
  }

  public setContexts(contexts: data.Context[]): void {
    this.render(contexts);
  }

  public component(): HTMLElement {
    return this.wrapper;
  }

  private render(contexts: data.Context[]): void {
    this.listContainer.innerHTML = '';
    this.contexts = contexts;

    const treeView = new TreeView();
    treeView.processList(this.contexts.map(context => {
      return {
        name: context.name, callback: () => {
          console.log(context);
          this.loadContext(context);
        }
      };
    }));

    this.listContainer.append(treeView.component());

    // this.contextRows.clear();

    // for (const context of contexts) {
    //   const row = document.createElement('div');
    //   row.classList.add('context-row', 'left-btn');

    //   const idCell = document.createElement('div');
    //   idCell.classList.add('id');
    //   idCell.textContent = String(context.id);

    //   const nameCell = document.createElement('div');
    //   nameCell.classList.add('name');
    //   nameCell.textContent = context.name || 'Untitled';

    //   this.contextRows.set(context, row);

    //   row.appendChild(idCell);
    //   row.appendChild(nameCell);

    //   const trashIcon = document.createElement("span");
    //   trashIcon.classList.add("trash-icon", "mdi", "mdi-trash-can");
    //   trashIcon.setAttribute("title", "Delete context");
    //   trashIcon.addEventListener("click", (e) => {
    //     e.stopPropagation();
    //     this.deleteContext(context);
    //   });
    //   row.appendChild(trashIcon);

    //   row.addEventListener('click', () => this.loadContext(context));
    //   this.listContainer.appendChild(row);
    // }
  }
}
