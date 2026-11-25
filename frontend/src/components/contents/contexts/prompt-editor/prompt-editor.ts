import "@mdi/font/css/materialdesignicons.css";
import { data } from "@wails/models";
import { Component } from "../../../component";
import "./prompt-editor.css";

export class PromptEditor implements Component {
  container: HTMLElement;
  private nameInput: HTMLInputElement | undefined;
  private contentDiv: HTMLElement | undefined;
  private existingPrompt: data.Prompt | undefined;
  private onSave: ((prompt: data.Prompt) => void);
  private onCancel: (() => void);

  /**
   * @param onSave Callback when Save is clicked
   * @param onCancel Callback when Cancel is clicked
   * @param existingPrompt Optional prompt for editing mode
   */
  constructor(
    onSave: ((prompt: data.Prompt) => void),
    onCancel: (() => void)
  ) {
    this.onSave = onSave;
    this.onCancel = onCancel;
    this.container = document.createElement('div');
    this.container.className = 'prompt-editor view';
  }

  public render(existingPrompt?: data.Prompt): void {
    this.container.innerHTML = '';
    this.existingPrompt = existingPrompt;

    // Name Field
    const nameLabel = document.createElement('label');
    nameLabel.className = 'editor-label';
    nameLabel.textContent = 'Name';

    this.nameInput = document.createElement('input');
    this.nameInput.type = 'text';
    this.nameInput.className = 'editor-input';
    this.nameInput.placeholder = 'Untitled';

    // Content Field
    const contentLabel = document.createElement('label');
    contentLabel.className = 'editor-label';
    contentLabel.textContent = 'Content';

    this.contentDiv = document.createElement('div');
    this.contentDiv.className = 'editor-content';

    // Note: "plaintext-only" is the standard HTML attribute for plaintext editing
    this.contentDiv.contentEditable = 'plaintext-only';

    // Buttons
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
    nameLabel.appendChild(this.nameInput);
    contentLabel.appendChild(this.contentDiv);

    this.container.appendChild(nameLabel);
    this.container.appendChild(contentLabel);
    this.container.appendChild(buttonContainer);

    // Populate fields if editing an existing prompt
    if (existingPrompt) {
      this.nameInput.value = existingPrompt.name || 'Untitled';
      this.contentDiv.textContent = existingPrompt.content || '';
    } else {
      this.nameInput.value = '';
      this.contentDiv.textContent = '';
    }
  }

  private handleSave(): void {
    const name = this.nameInput!.value.trim();
    const content = this.contentDiv!.textContent?.trim() || '';

    if (this.existingPrompt) {
      this.onSave({
        name,
        content,
        id: this.existingPrompt.id,
        insertedAt: this.existingPrompt.insertedAt,
        updatedAt: this.existingPrompt.updatedAt
      } as data.Prompt);
    } else {
      this.onSave({
        name, content
      } as data.Prompt);
    }
  }

  private handleCancel(): void {
    this.onCancel();
  }

  component(): HTMLElement {
    return this.container;
  }
}
