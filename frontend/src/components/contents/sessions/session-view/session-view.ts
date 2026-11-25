import { CreateSession, ListSessions } from '../../../../../wailsjs/go/main/App';
import { Component } from '../../../component';
import { MessageBody } from '../message-body/message-body';
import { SessionList } from '../session-list/session-list';
import './session-view.css';

export class SessionView implements Component {
  container: HTMLElement;

  private sessionListComponent = new SessionList();
  private messageBody = new MessageBody();

  constructor() {
    this.container = document.createElement('div');
    this.container.className = 'session-view';

    this.messageBody.run();

    this.refreshSessions();
    this.renderHeader();
    this.renderContent();
  }

  private renderHeader(): void {
    const header = document.createElement('div');
    header.className = 'session-view-header';

    // Create Session Button
    const createBtn = document.createElement('div');
    createBtn.className = 'create-session-btn';
    createBtn.id = 'create-session';

    const icon = document.createElement('i');
    icon.className = 'mdi mdi-plus-circle create-session-icon';

    const text = document.createElement('span');
    text.className = 'create-session-text';
    text.textContent = 'New Session';

    createBtn.appendChild(icon);
    createBtn.appendChild(text);

    // Event listener for new session creation
    createBtn.addEventListener('click', () => {
      this.container.dispatchEvent(new CustomEvent('create-session'));
      this.createSession();
    });

    header.appendChild(createBtn);
    this.container.appendChild(header);
  }

  private renderContent(): void {
    const contentArea = document.createElement('div');
    contentArea.className = 'session-view-content';

    // Instantiate the existing SessionList component
    const sessionListEl = this.sessionListComponent.component();

    contentArea.appendChild(sessionListEl);
    this.container.appendChild(contentArea);
  }

  private refreshSessions() {
    ListSessions().then(sessions => {
      console.log("sessions", sessions);

      if (sessions == null) {
        this.createSession();
      } else {
        // dunnolol
        console.log("Loading session", sessions[0]);
        this.sessionListComponent.loadSession(sessions[0]);
      }

      this.sessionListComponent.setSessions(sessions);
    });
  }

  private createSession() {
    CreateSession()
      .then(session => {
        this.sessionListComponent.pushSession(session);
      })
  }

  component(): HTMLElement {
    return this.container;
  }
}
