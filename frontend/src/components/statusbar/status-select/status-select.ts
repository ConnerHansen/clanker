import { Component } from "../../component";
import "./status-select.css";

export class StatusSelect implements Component {
  private root: HTMLElement;
  private button!: HTMLButtonElement;
  private popup!: HTMLElement;
  private optionsList!: HTMLElement;
  private options: string[];
  private selectedIndex: number;
  private expanded: boolean = false;
  private onChange?: (value: string) => void;
  private cleanup?: () => void;

  constructor(private label: string, options: string[], initialIndex: number = 0, onChange?: (value: string) => void) {
    this.options = options;
    this.selectedIndex = Math.max(0, Math.min(initialIndex, options.length - 1));
    this.onChange = onChange;
    this.root = this.createElement();
    this.render();
    this.attachEvents();
  }

  public setOptions(options: string[], initialIndex: number = 0) {
    this.options = options;
    this.selectedIndex = Math.max(0, Math.min(initialIndex, options.length - 1));
    this.render();
  }

  private createElement(): HTMLElement {
    const root = document.createElement('div');
    root.className = 'status-pill status-select';
    root.setAttribute('role', 'combobox');
    root.setAttribute('aria-expanded', 'false');
    root.setAttribute('aria-haspopup', 'listbox');

    const button = document.createElement('button');
    button.className = 'status-select-button';
    button.type = 'button';
    button.setAttribute('aria-haspopup', 'listbox');
    button.setAttribute('aria-expanded', 'false');

    const popup = document.createElement('div');
    popup.className = 'status-select-popup';
    popup.setAttribute('role', 'listbox');
    popup.setAttribute('aria-label', 'Status options');

    const list = document.createElement('ul');
    list.className = 'status-select-options';

    popup.appendChild(list);
    root.appendChild(button);
    root.appendChild(popup);

    return root;
  }

  private render(): void {
    this.button = this.root.querySelector('button')!;
    this.popup = this.root.querySelector('.status-select-popup')!;
    this.optionsList = this.popup.querySelector('.status-select-options')!;
    this.updateButton();
    this.renderOptions();
  }

  private renderOptions(): void {
    this.optionsList.innerHTML = '';
    this.options.forEach((option, index) => {
      const li = document.createElement('li');
      li.className = 'status-select-option';
      if (index === this.selectedIndex) li.classList.add('selected');
      li.setAttribute('role', 'option');
      li.setAttribute('aria-selected', String(index === this.selectedIndex));
      li.textContent = option;
      this.optionsList.appendChild(li);
    });
  }

  private attachEvents(): void {
    const toggle = () => this.togglePopup();

    const handleOutsideClick = (e: MouseEvent) => {
      // 1. Only care about clicks when the popup is open
      if (!this.popup.classList.contains('open')) return;

      // 2. If the click target is inside our component (root or children), ignore it
      if (e.target && this.root.contains(e.target as Node)) return;

      // 3. Click happened outside the component → close the popup
      this.closePopup();
    };

    const handleOptionClick = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      if (target.classList.contains('status-select-option')) {
        const index = Array.from(this.optionsList.children).indexOf(target);
        this.selectOption(index);
      }
    };

    const handleKeyDown = (e: KeyboardEvent) => {
      if (!this.popup.classList.contains('open')) return;
      switch (e.key) {
        case 'Escape': this.closePopup(); break;
        case 'ArrowDown': this.navigateOptions(1); break;
        case 'ArrowUp': this.navigateOptions(-1); break;
        case 'Enter': case ' ':
          if (document.activeElement?.classList.contains('status-select-option')) {
            e.preventDefault();
            this.selectFocusedOption();
          }
          break;
      }
    };

    this.button.addEventListener('click', toggle);
    this.optionsList.addEventListener('click', handleOptionClick);
    document.addEventListener('click', handleOutsideClick);
    document.addEventListener('keydown', handleKeyDown);

    this.cleanup = () => {
      this.button.removeEventListener('click', toggle);
      this.optionsList.removeEventListener('click', handleOptionClick);
      document.removeEventListener('click', handleOutsideClick);
      document.removeEventListener('keydown', handleKeyDown);
    };
  }

  private togglePopup(): void {
    this.expanded ? this.closePopup() : this.openPopup();
  }

  private openPopup(): void {
    this.expanded = true;
    this.popup.classList.add('open');
    this.button.setAttribute('aria-expanded', 'true');
    this.root.setAttribute('aria-expanded', 'true');

    // Prevent popup from clipping off the bottom of the viewport
    const rect = this.popup.getBoundingClientRect();
    if (rect.bottom > window.innerHeight) {
      this.root.classList.add('flip-to-bottom');
    }
  }

  private closePopup(): void {
    console.log("Close!");
    this.expanded = false;
    this.popup.classList.remove('open');
    this.button.setAttribute('aria-expanded', 'false');
    this.root.setAttribute('aria-expanded', 'false');
    this.root.classList.remove('flip-to-bottom'); // Reset flip when closed
    this.button.focus();
  }

  private selectOption(index: number): void {
    if (index < 0 || index >= this.options.length) return;
    this.selectedIndex = index;
    this.updateButton();
    this.renderOptions();
    this.closePopup();
    this.onChange?.(this.options[this.selectedIndex]);
  }

  private updateButton(): void {
    this.button.textContent = `${this.label}${this.options[this.selectedIndex]}`;
  }

  private navigateOptions(direction: number): void {
    const options = this.optionsList.querySelectorAll('.status-select-option');
    const current = Array.from(options).findIndex(el => el === document.activeElement);
    let nextIndex = current === -1 ? 0 : current + direction;
    nextIndex = Math.max(0, Math.min(nextIndex, options.length - 1));

    options.forEach(el => el.classList.remove('focused'));
    const next = options[nextIndex] as HTMLElement;
    next?.focus();
    next?.classList.add('focused');
    next?.scrollIntoView({ block: 'nearest' });
  }

  private selectFocusedOption(): void {
    const focused = Array.from(this.optionsList.querySelectorAll('.status-select-option'))
      .find(el => el.classList.contains('focused'));
    if (focused) {
      const index = Array.from(this.optionsList.children).indexOf(focused);
      this.selectOption(index);
    }
  }

  public component(): HTMLElement {
    return this.root;
  }

  public destroy(): void {
    this.cleanup?.();
    this.root.remove();
  }
}