import './app.css';
import './style.css';

import { AppSettings } from './services/app-settings';
import { LeftBar } from './components/left-bar/left-bar';
import { Statusbar } from './components/statusbar/statusbar';
import { PromptModal } from './components/prompt-modal/prompt-modal';

class Main {
  private readonly mainBody = document.querySelector('#main')!;
  private readonly content = document.querySelector('#content')! as HTMLDivElement;
  private readonly statusbar = new Statusbar();
  private currentView: HTMLElement | undefined;
  private scrolledToBottom = false;

  constructor() {
    this.mainBody.append(this.statusbar.component());
    const leftBar = new LeftBar();
    document.getElementById('left-bar-container')?.appendChild(leftBar.component());

    // Blocking agent prompts (system-ask forms, permission requests) surface
    // as a dimmed centered modal on top of whatever view is active; it owns
    // the overlay for the whole app lifetime, like the statusbar.
    const promptModal = new PromptModal();
    promptModal.run();

    const mainArea = document.getElementById('main');

    const triggerResize = (_event: UIEvent) => {
      this.resizeContent();
    }

    let previousView: HTMLElement | undefined;
    AppSettings.observables.onViewRequested.subscribe(view => {
      console.log('View!');

      this.currentView?.removeEventListener('resize', triggerResize);

      if (view) {
        previousView = this.currentView;
        this.currentView = view;
      } else if (previousView) {
        this.currentView = previousView;
        previousView = undefined;
      } else {
        this.currentView = document.createElement('div');
      }

      this.currentView.classList.add('invisible');
      this.content.innerHTML = '';
      this.content.append(this.currentView);
      this.currentView.addEventListener('resize', triggerResize);
      this.resizeContent();
    });

    // this.content.addEventListener()
    const observer = new MutationObserver(list => {
      console.log('MUTATION!');
      for (const mutation of list) {
        if (mutation.type == 'childList') {
          if (this.scrolledToBottom) {
            this.content.scrollTop = this.content.scrollHeight;
          }
        }
      }
    });

    observer.observe(this.content, {
      childList: true,
      subtree: true
    });

    this.content.addEventListener('scroll', () => {
      this.scrolledToBottom = (this.content.scrollTop + this.mainBody.clientHeight > this.content.scrollHeight);
    });

    // Handle new session event from left bar
    leftBar.component().addEventListener('create-session', () => {
      console.log('New Session Clicked');
      // Call Wails backend here
    });

    // Listen for sidebar changes to adjust main content layout if needed
    leftBar.component().addEventListener('leftbar-expand', () => {
      if (mainArea) mainArea.classList.add('expanded-sidebar');
      this.resizeContent();
    });

    leftBar.component().addEventListener('leftbar-collapse', () => {
      if (mainArea) mainArea.classList.remove('expanded-sidebar');
      this.resizeContent();
    });
  }

  public run() { }

  private resizeContent() {
    setTimeout(() => {
      this.content.scrollTop = this.content.scrollHeight;
      this.currentView?.classList.remove('invisible');
      this.scrolledToBottom = true;
    }, 0);
  }
}

const mainApp = new Main();
mainApp.run();