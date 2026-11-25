import { ContextView } from '../contents/contexts/context-view/context-view';
import { Component } from '../component';
import { SessionView } from '../contents/sessions/session-view/session-view';
import './left-bar.css';

// Define modes for clarity
export type LeftBarMode = 'session-list' | 'context-list' | 'subagent-list';

export class LeftBar implements Component {
  private container: HTMLElement;
  private activeMode: LeftBarMode = 'session-list';
  private isCollapsed: boolean = false;
  private contentContainer: HTMLElement;
  private iconBar: HTMLElement;
  private scrollArea: HTMLElement;
  private header: HTMLElement;

  private sessionView: SessionView = new SessionView();
  private contextView: ContextView = new ContextView();

  // Map of mode to component loader/function
  private modeComponents: Partial<Record<LeftBarMode, Component>> = {
    'session-list': this.sessionView,
    'context-list': this.contextView,
    'subagent-list': this.contextView,
  };

  constructor() {
    this.container = document.createElement('div');
    this.container.id = 'left-bar';
    this.container.className = 'left-bar';

    // Icon Bar (Fixed Width)
    this.iconBar = document.createElement('div');
    this.iconBar.className = 'left-bar-icon-bar';

    // Content Area (Fixed Width, Hidden by default via CSS)
    this.contentContainer = document.createElement('div');
    this.contentContainer.className = 'left-bar-content-container';

    // Header inside content
    this.header = document.createElement('div');
    this.header.className = 'left-bar-content-header';
    this.contentContainer.appendChild(this.header);

    // Scroll Area inside content
    this.scrollArea = document.createElement('div');
    this.scrollArea.className = 'left-bar-scroll-area';
    this.contentContainer.appendChild(this.scrollArea);

    // Assemble DOM
    this.container.appendChild(this.iconBar);
    this.container.appendChild(this.contentContainer);

    // Initialize UI
    this.initIconBar();

    // Start with a default mode loaded
    this.updateVisualState();
    this.setMode('session-list');
  }

  private initIconBar(): void {
    const modes: { mode: LeftBarMode; icon: string; label: string }[] = [
      { mode: 'session-list', icon: 'mdi-forum', label: 'Sessions' },
      { mode: 'context-list', icon: 'mdi-file-document', label: 'Contexts' },
      { mode: 'subagent-list', icon: 'mdi-transit-connection-variant', label: 'Subagents' },
    ];

    modes.forEach(({ mode, icon, label }) => {
      const btn = document.createElement('div');
      btn.className = 'left-bar-icon-btn';
      btn.dataset.mode = mode;
      btn.title = label;

      const iconEl = document.createElement('i');
      iconEl.className = `mdi ${icon} left-bar-icon`;

      btn.appendChild(iconEl);

      // Clicking an icon expands the sidebar if it's collapsed, then sets the mode
      btn.addEventListener('click', () => {
        if (this.isCollapsed) {
          this.expand();
        } else if (this.activeMode === mode) {
          this.collapse();
          // Optional: Allow clicking active mode to collapse? 
          // Based on your request, we just ensure it's visible.
          // If you want double-click to collapse, add that logic here.
        }
        this.setMode(mode);
      });

      this.iconBar.appendChild(btn);
    });
  }

  private async setMode(mode: LeftBarMode): Promise<void> {
    this.activeMode = mode;

    // Update active state in icon bar
    const buttons: NodeListOf<HTMLDivElement> = this.iconBar.querySelectorAll('.left-bar-icon-btn');
    buttons.forEach(btn => {
      if (btn.dataset.mode === mode) {
        btn.classList.add('active');
      } else {
        btn.classList.remove('active');
      }
    });

    // Update Header Text
    this.header.innerHTML = `<h3>${mode.replace('-', ' ').replace(/\b\w/g, l => l.toUpperCase())}</h3>`;

    // Load and render component
    const component = this.modeComponents[mode];
    if (component) {
      try {
        this.scrollArea.innerHTML = '';
        this.scrollArea.appendChild(component.component());
      } catch (error) {
        console.error(`Failed to load component for mode ${mode}`, error);
        this.showError('Failed to load component');
      }
    }
  }

  private showError(message: string): void {
    this.scrollArea.innerHTML = `<div class="error-message">${message}</div>`;
  }

  /**
   * Expands the sidebar if it is collapsed
   */
  private expand(): void {
    if (!this.isCollapsed) return;

    this.isCollapsed = false;
    this.updateVisualState();

    // Dispatch event for external listeners (e.g., to resize main content)
    this.container.dispatchEvent(new CustomEvent('leftbar-expand'));
  }

  /**
   * Collapses the sidebar
   */
  public collapse(): void {
    if (this.isCollapsed) return;

    this.isCollapsed = true;
    this.updateVisualState();

    // Dispatch event for external listeners
    this.container.dispatchEvent(new CustomEvent('leftbar-collapse'));
  }

  private updateVisualState(): void {
    if (this.isCollapsed) {
      this.container.classList.add('collapsed');
    } else {
      this.container.classList.remove('collapsed');
    }
  }

  component(): HTMLElement {
    return this.container;
  }
}
