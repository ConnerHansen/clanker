import { SelectableComponent } from 'components/selectable-component';
import { Component } from 'components/component';
import './tree-view.css';
import { Observable, Subject } from 'rxjs';

export interface ContextNode {
  [key: string]: (() => void) | ContextNode
}

export class TreeView implements Component, SelectableComponent<void> {
  container: HTMLElement;
  private rootNodes: ContextNode | undefined;
  private activeNode: { node: ContextNode, element: HTMLElement } | undefined;

  private selectedEntry = new Subject<void>();
  public readonly onSelection: Observable<void> = this.selectedEntry.asObservable();

  constructor() {
    this.container = document.createElement('div');
    this.container.className = 'tree-view';

    // Initial empty state
    this.renderEmptyState();
  }

  clearSelection(): void {
    this.activeNode?.element.classList.remove('active');
    this.activeNode = undefined;
  }

  processList(data: { name: string, callback: () => void }[]) {
    const tree: any = {};
    data.forEach(({ name, callback }) => {
      let root = tree;
      const pieces = name.split('.');
      for (let i = 0; i < pieces.length; i++) {
        if (i == pieces.length - 1) {
          root[pieces[i]] = callback;
        } else {
          root[pieces[i]] = root[pieces[i]] ?? {}
          root = root[pieces[i]];
        }
      }
    });

    this.loadNodes(tree);
  }

  loadNodes(data: ContextNode): void {
    this.rootNodes = { '.': data };
    // Clear existing content except for the root container itself
    this.container.innerHTML = '';
    this.renderTree(this.container, this.rootNodes);
  }

  private renderEmptyState(): void {
    this.container.innerHTML = '';
    const empty = document.createElement('div');
    empty.className = 'context-tree-empty';
    empty.textContent = 'No supporting context files.';
    this.container.appendChild(empty);
  }

  private renderTree(container: HTMLElement, node: ContextNode): void {
    if (!node) return;

    // Iterate through the keys of the current node
    for (const [key, value] of Object.entries(node)) {
      if (typeof value === 'function') {
        // It's a file
        console.log("File", value);
        const fileEl = this.renderFile(node, key, value);
        container.appendChild(fileEl);
      } else if (typeof value === 'object' && value !== null) {
        // It's a folder
        console.log("Folder", value);
        const folderEl = this.renderFolder(node, key, value);
        container.appendChild(folderEl);
      }
    }
  }

  private renderFile(node: ContextNode, filename: string, callback: () => void): HTMLElement {
    const fileEl = document.createElement('div');
    fileEl.className = 'context-file-item left-btn';

    const icon = document.createElement('i');
    icon.className = 'mdi mdi-file-document-outline context-icon';

    const name = document.createElement('span');
    name.className = 'context-file-name';
    name.textContent = filename;

    fileEl.addEventListener('click', () => {
      this.select(node, fileEl);
      callback();
    });

    fileEl.appendChild(icon);
    fileEl.appendChild(name);
    return fileEl;
  }

  private renderFolder(node: ContextNode, folderName: string, children: ContextNode): HTMLElement {
    console.log("Rendering folder: ", folderName);
    const folderContainer = document.createElement('div');
    folderContainer.className = 'context-folder';

    // Folder Header (Click to toggle)
    const folderHeader = document.createElement('div');
    folderHeader.className = 'context-folder-header left-btn';

    // Folders default to open
    const icon = document.createElement('i');
    icon.className = 'mdi mdi-folder-open context-folder-icon';

    const name = document.createElement('span');
    name.className = 'context-folder-name';
    name.textContent = folderName;

    folderHeader.appendChild(icon);
    folderHeader.appendChild(name);

    // Folder Content (Recursive children)
    const folderContent = document.createElement('div');
    folderContent.className = 'context-folder-content';

    // Render children recursively
    this.renderTree(folderContent, children);

    // Re-doing the logic for cleaner DOM structure:
    folderHeader.addEventListener('click', () => {
      const isOpen = folderContent.style.display !== 'none';
      folderContent.style.display = isOpen ? 'none' : 'block';

      this.select(node, folderHeader);

      const folderIcon = folderHeader.querySelector('i');
      if (folderIcon) {
        folderIcon.className = isOpen
          ? 'mdi mdi-folder context-folder-icon'
          : 'mdi mdi-folder-open context-folder-icon';
      }
    });

    folderContainer.appendChild(folderHeader);
    folderContainer.appendChild(folderContent);

    return folderContainer;
  }

  private select(node: ContextNode, element: HTMLElement) {
    if (this.activeNode?.element != element) {
      this.selectedEntry.next();
      this.activeNode?.element.classList.remove('active');
      this.activeNode = { node, element };
      this.activeNode.element.classList.add('active');
    }
  }

  component(): HTMLElement {
    return this.container;
  }
}
