import "@mdi/font/css/materialdesignicons.css";
import { DeleteSession } from "../../../../../wailsjs/go/main/App";
import { data } from "../../../../../wailsjs/go/models";
import { loadSession } from "../../../../services/app-settings";
import { Component } from "../../../component";
import "./session-list.css";

export class SessionList implements Component {
  private wrapper: HTMLElement;
  private listContainer: HTMLElement;
  private activeSession: data.Session | undefined;
  private sessions: data.Session[] = [];
  // private sessionRows: { [key:data.Session]: HTMLDivElement } = {};
  private sessionRows = new Map<data.Session, HTMLDivElement>();

  constructor() {
    // Wrapper acts as the scrollable viewport
    this.wrapper = document.createElement('div');
    this.wrapper.classList.add('scrollable-list');
    this.wrapper.style.height = '100%';
    this.wrapper.style.overflowY = 'auto';

    // Container holds the actual row elements
    this.listContainer = document.createElement('div');
    this.wrapper.appendChild(this.listContainer);
  }

  public loadSession(session: data.Session) {
    if (this.activeSession) {
      this.sessionRows.get(this.activeSession)?.classList.remove('active');
    }
    this.activeSession = session;
    loadSession(session).then(messages => {
      this.sessionRows.get(session)?.classList.add('active');
      console.log(messages);
    })
  }

  public async deleteSession(session: data.Session) {
    // TODO: Implement your deletion logic here (API call, remove from array, etc.)
    console.log("Deleting session:", session.id);
    await DeleteSession(session);

    this.sessionRows.get(session)?.remove();
    this.sessionRows.delete(session);
  }

  public pushSession(session: data.Session) {
    this.sessions.unshift(session);
    this.setSessions(this.sessions);
    this.loadSession(session);
    console.log(session);
  }

  /** Updates the rendered list with a new array of sessions */
  public setSessions(sessions: data.Session[]): void {
    this.render(sessions);
  }

  private formatSummaryTitle(session: data.Session, detail: string): string {
    return `Session ${session.id}: ${detail}`;
  }

  private humanizeTime(value: any): string {
    if (!value) return 'unknown time';
    const d = new Date(value);
    if (isNaN(d.getTime())) return String(value);
    return d.toLocaleString();
  }

  public component(): HTMLElement {
    return this.wrapper;
  }

  private render(sessions: data.Session[]): void {
    // Clear previous rows efficiently
    this.listContainer.innerHTML = '';
    this.sessions = sessions;
    this.sessionRows.clear();

    for (const session of sessions) {
      const row = document.createElement('div');
      row.classList.add('session-row', 'left-btn');

      const idCell = document.createElement('div');
      idCell.classList.add('id');
      idCell.textContent = String(session.id);

      // Summary staleness indicator (hidden while the session has no messages).
      if (session.summary_status) {
        const statusIcon = document.createElement('span');
        statusIcon.classList.add('summary-status', 'mdi');
        switch (session.summary_status) {
          case 'fresh':
            statusIcon.classList.add('fresh');
            statusIcon.classList.add('mdi-check-circle');
            statusIcon.title = this.formatSummaryTitle(session, `Summarized ${this.humanizeTime(session.summarized_at)}`);
            break;
          case 'stale':
            statusIcon.classList.add('stale');
            statusIcon.classList.add('mdi-alert-circle');
            statusIcon.title = this.formatSummaryTitle(session, 'New messages since last summary; will be re-summarized on the next sweep');
            break;
          default: // 'none'
            statusIcon.classList.add('none');
            statusIcon.classList.add('mdi-minus-circle-outline');
            statusIcon.title = this.formatSummaryTitle(session, 'Not summarized yet');
        }
        row.appendChild(statusIcon);
      }

      const nameCell = document.createElement('div');
      nameCell.classList.add('name');
      nameCell.textContent = session.name || 'Untitled';

      this.sessionRows.set(session, row);

      row.appendChild(idCell);
      row.appendChild(nameCell);

      // Trash icon
      const trashIcon = document.createElement("span");
      trashIcon.classList.add("trash-icon", "mdi", "mdi-trash-can");
      trashIcon.setAttribute("title", "Delete session");
      trashIcon.addEventListener("click", (e) => {
        e.stopPropagation(); // Prevent the row's loadSession click from firing
        this.deleteSession(session);
      });
      row.appendChild(trashIcon);

      row.addEventListener('click', () => this.loadSession(session));

      this.listContainer.appendChild(row);
    }
  }
}