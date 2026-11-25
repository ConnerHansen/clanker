import { Component } from "@components/component";
import "@mdi/font/css/materialdesignicons.css";
import { data } from "@wails/models";
import "./context-editor.css";
import { ListPrompts } from "@wails/main/App";

export class ContextEditor implements Component {
  private container: HTMLDivElement;
  private nameInput!: HTMLInputElement;
  private thinkingCheckbox!: HTMLInputElement;
  private promptListContainer!: HTMLElement;
  private addDropdownContainer!: HTMLElement;
  private filterInput!: HTMLInputElement;
  private onSave: ((context: data.Context) => void);
  private onCancel: (() => void);
  private draggingIndex: number | null = null;
  private isEditing: boolean = false;
  private contextPrompts: data.Prompt[] = [];
  private availablePrompts: data.Prompt[] = [];
  private existingContext: data.Context | undefined;

  constructor(
    onSave: ((context: data.Context) => void),
    onCancel: (() => void)
  ) {
    this.onSave = onSave;
    this.onCancel = onCancel;
    this.container = document.createElement('div');
    this.container.className = 'context-editor view';
  }

  public render(existingContext?: data.Context): void {
    this.existingContext = existingContext;
    this.container.innerHTML = '';
    this.isEditing = !!existingContext;
    this.contextPrompts = existingContext ? existingContext.prompts : [];
    this.draggingIndex = null;

    // 1. Name Field
    const nameLabel = document.createElement('label');
    nameLabel.className = 'editor-label';
    nameLabel.textContent = 'Name';

    this.nameInput = document.createElement('input');
    this.nameInput.type = 'text';
    this.nameInput.className = 'editor-input';
    this.nameInput.placeholder = 'Untitled Context';
    nameLabel.appendChild(this.nameInput);

    // 2. Thinking Toggle
    const thinkingLabel = document.createElement('label');
    thinkingLabel.className = 'editor-label';
    thinkingLabel.textContent = 'Thinking';

    const toggleWrapper = document.createElement('div');
    toggleWrapper.className = 'toggle-wrapper';

    this.thinkingCheckbox = document.createElement('input');
    this.thinkingCheckbox.type = 'checkbox';
    this.thinkingCheckbox.id = 'thinking-toggle';
    this.thinkingCheckbox.className = 'toggle-input';

    const toggleSlider = document.createElement('span');
    toggleSlider.className = 'toggle-slider';
    toggleSlider.setAttribute('aria-hidden', 'true');

    toggleWrapper.appendChild(this.thinkingCheckbox);
    toggleWrapper.appendChild(toggleSlider);
    thinkingLabel.appendChild(toggleWrapper);

    // 3. Prompts List
    const promptsLabel = document.createElement('label');
    promptsLabel.className = 'editor-label';
    promptsLabel.textContent = 'Prompts';

    this.promptListContainer = document.createElement('div');
    this.promptListContainer.className = 'prompt-list';

    // 4. Add Button & Dropdown Container
    const addBtnWrapper = document.createElement('div');
    addBtnWrapper.style.position = 'relative';
    addBtnWrapper.style.display = 'inline-block';

    const addBtn = document.createElement('button');
    addBtn.className = 'btn btn-secondary';
    addBtn.innerHTML = '<span class="mdi mdi-plus"></span> Add Prompt';
    addBtn.addEventListener('click', () => this.toggleAddDropdown());

    this.addDropdownContainer = document.createElement('div');
    this.addDropdownContainer.className = 'add-prompt-dropdown';
    this.addDropdownContainer.style.display = 'none';
    this.closeAddDropdown();

    this.filterInput = document.createElement('input');
    this.filterInput.type = 'text';
    this.filterInput.className = 'editor-input';
    this.filterInput.placeholder = 'Filter prompts...';
    this.filterInput.style.marginBottom = '8px';
    this.filterInput.addEventListener('input', () => this.renderAvailablePrompts());

    const availableList = document.createElement('div');
    availableList.className = 'available-prompts-list';
    this.addDropdownContainer.appendChild(this.filterInput);
    this.addDropdownContainer.appendChild(availableList);

    addBtnWrapper.appendChild(addBtn);
    addBtnWrapper.appendChild(this.addDropdownContainer);

    // 5. Save/Cancel Buttons
    const buttonContainer = document.createElement('div');
    buttonContainer.className = 'editor-buttons';

    const saveBtn = document.createElement('button');
    saveBtn.className = 'btn btn-primary';
    saveBtn.textContent = 'Save';
    saveBtn.addEventListener('click', () => this.handleSave());

    const cancelBtn = document.createElement('button');
    cancelBtn.className = 'btn btn-secondary';
    cancelBtn.textContent = 'Cancel';
    cancelBtn.addEventListener('click', () => this.handleCancel());

    buttonContainer.appendChild(saveBtn);
    buttonContainer.appendChild(cancelBtn);

    // Assemble DOM
    this.container.appendChild(nameLabel);
    this.container.appendChild(thinkingLabel);
    this.container.appendChild(promptsLabel);
    this.container.appendChild(this.promptListContainer);
    this.container.appendChild(addBtnWrapper);
    this.container.appendChild(buttonContainer);

    // Populate fields if editing
    if (existingContext) {
      this.nameInput.value = existingContext.name || 'Untitled Context';
      this.thinkingCheckbox.checked = existingContext.thinking || false;
    } else {
      this.nameInput.value = '';
      this.thinkingCheckbox.checked = false;
    }

    this.renderPromptList();
  }

  private renderPromptList(): void {
    this.promptListContainer.innerHTML = '';
    if (this.contextPrompts.length === 0) {
      const emptyMsg = document.createElement('div');
      emptyMsg.className = 'empty-state';
      emptyMsg.textContent = 'No prompts attached. Click "+ Add Prompt" to start.';
      this.promptListContainer.appendChild(emptyMsg);
      return;
    }

    this.contextPrompts.forEach((prompt, index) => {
      const item = document.createElement('div');
      item.className = 'prompt-list-item';
      item.draggable = true;
      item.dataset.index = String(index);

      const handle = document.createElement('span');
      handle.className = 'drag-handle';
      handle.innerHTML = '<span class="mdi mdi-drag"></span>';

      const nameSpan = document.createElement('span');
      nameSpan.className = 'prompt-name';
      nameSpan.textContent = prompt.name;

      const removeBtn = document.createElement('button');
      removeBtn.className = 'btn-remove';
      removeBtn.innerHTML = '<span class="mdi mdi-close"></span>';
      removeBtn.title = 'Remove';
      removeBtn.addEventListener('click', (e) => {
        e.stopPropagation();
        this.contextPrompts.splice(index, 1);
        this.renderPromptList();
      });

      item.appendChild(handle);
      item.appendChild(nameSpan);
      item.appendChild(removeBtn);

      // Drag Events
      item.addEventListener('dragstart', (e) => {
        this.draggingIndex = index;
        item.classList.add('dragging');
        e.dataTransfer!.effectAllowed = 'move';
        e.dataTransfer!.setData('text/plain', String(index));
      });

      item.addEventListener('dragend', () => {
        item.classList.remove('dragging');
        this.draggingIndex = null;
        this.promptListContainer.querySelectorAll('.prompt-list-item').forEach(el => {
          el.classList.remove('drag-over');
        });
      });

      item.addEventListener('dragover', (e) => {
        e.preventDefault();
        e.dataTransfer!.dropEffect = 'move';
        item.classList.add('drag-over');
      });

      item.addEventListener('dragleave', () => {
        item.classList.remove('drag-over');
      });

      item.addEventListener('drop', (e) => {
        e.preventDefault();
        item.classList.remove('drag-over');
        const targetIndex = parseInt((e.currentTarget! as HTMLDivElement).dataset.index || '0', 10);

        if (this.draggingIndex === null || this.draggingIndex === targetIndex) return;

        const draggedPrompt = this.contextPrompts.splice(this.draggingIndex, 1)[0];
        this.contextPrompts.splice(targetIndex, 0, draggedPrompt);
        this.renderPromptList();
      });

      this.promptListContainer.appendChild(item);
    });
  }

  private async toggleAddDropdown(): Promise<void> {
    const isVisible = this.addDropdownContainer.style.display === 'block';
    if (isVisible) {
      this.closeAddDropdown();
      return;
    }

    this.addDropdownContainer.style.display = 'block';
    this.filterInput.value = '';
    this.filterInput.focus();

    // Fetch & Filter Available Prompts
    const allPrompts = await ListPrompts();
    this.availablePrompts = allPrompts.filter(p =>
      !this.contextPrompts.some(cp => cp.id === p.id)
    );
    this.renderAvailablePrompts();
  }

  private closeAddDropdown(): void {
    this.addDropdownContainer.style.display = 'none';
  }

  private renderAvailablePrompts(): void {
    const listContainer = this.addDropdownContainer.querySelector('.available-prompts-list') as HTMLElement;
    if (!listContainer) return;

    listContainer.innerHTML = '';
    const filter = this.filterInput.value.toLowerCase();
    const filtered = this.availablePrompts.filter(p => p.name.toLowerCase().includes(filter));

    if (filtered.length === 0) {
      const empty = document.createElement('div');
      empty.className = 'empty-state';
      empty.textContent = 'No available prompts';
      listContainer.appendChild(empty);
      return;
    }

    filtered.forEach(prompt => {
      const item = document.createElement('div');
      item.className = 'available-prompt-item';
      item.textContent = prompt.name;
      item.addEventListener('click', () => {
        this.contextPrompts.push(prompt);
        this.renderPromptList();
        this.closeAddDropdown();
      });
      listContainer.appendChild(item);
    });
  }

  private handleSave(): void {
    console.log("Saving...");
    const name = this.nameInput.value.trim() || 'Untitled Context';
    const thinking = this.thinkingCheckbox.checked;

    const savedContext = {
      id: this.isEditing ? this.existingContext?.id ?? 0 : 0,
      name,
      prompts: this.contextPrompts,
      thinking,
      insertedAt: this.isEditing ? new Date() : new Date(),
      updatedAt: new Date()
    } as data.Context;

    this.onSave(savedContext);
  }

  private handleCancel(): void {
    this.onCancel();
  }

  component(): HTMLElement {
    return this.container;
  }
}
