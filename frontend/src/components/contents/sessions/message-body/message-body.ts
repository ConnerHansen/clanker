import { AppSettings, InputMode, requestView } from 'services/app-settings';
import { Component } from 'components/component';
import { AgentResponse } from '../agent-response/agent-response';
import { UserInput } from '../user-input/user-input';
import { Prompt } from '@wails/main/App';
import { data } from '@wails/models';
import './message-body.css';

export class MessageBody implements Component {
  private element: HTMLDivElement;
  private currentAgentResponse: AgentResponse | undefined;
  private currentUserInput: UserInput | undefined;
  // private inputSubscription: Subscription | undefined;

  constructor() {
    this.element = document.createElement('div');
    this.element.classList.add('message-body', 'view');
  }

  component(): HTMLElement {
    return this.element;
  }

  public run() {
    this.listenForSessions();
    this.listenForPrompts();
    this.listenForCompaction();
    this.newInput();
  }

  private newInput() {
    // this.inputSubscription?.unsubscribe();
    this.currentUserInput = new UserInput();
    // this.inputSubscription = this.currentUserInput.onFreeze().subscribe(() => {
    //   if (AppSettings.inputMode === InputMode.PROMPT) {
    //     this.currentAgentResponse = new AgentResponse();
    //     this.element.appendChild(this.currentAgentResponse.component());

    //     Prompt(this.currentUserInput!.getText(), AppSettings.model);
    //   }

    //   this.newInput();
    // });

    this.element.appendChild(this.currentUserInput.component());
    this.currentUserInput.component().focus();
  }

  private listenForPrompts() {
    AppSettings.observables.onPromptRequested.subscribe(prompt => {
      if (AppSettings.inputMode === InputMode.PROMPT) {
        this.currentAgentResponse = new AgentResponse();
        this.element.appendChild(this.currentAgentResponse.component());

        Prompt(prompt, AppSettings.model);
      }

      if (this.currentUserInput?.isEditable() == false) {
        this.newInput();
      }
    });
  }

  private listenForSessions() {
    AppSettings.observables.onSessionLoaded.subscribe(session => {
      console.log('session!');
      this.currentAgentResponse?.destroy();
      // this.inputSubscription?.unsubscribe();
      this.element.innerHTML = '';

      requestView(this.component());

      // The FULL history renders: rows before the latest compaction seed were
      // compacted away from the MODEL's context, but they stay in the session
      // DB and remain display-only here (the runtime payload still cuts at the
      // seed — see data.CompactionCutIndex / agent SetSession).
      const messages = session.messages ?? [];
      messages.forEach(message => {
        if (message.Source == 'user') {
          const input = new UserInput(message);
          this.element.appendChild(input.component());
        } else {
          const input = new AgentResponse(message);
          this.element.appendChild(input.component());
        }
      });

      this.newInput();
    });
  }

  private listenForCompaction() {
    AppSettings.observables.onCompaction.subscribe(event => {
      // A compaction registered mid-run: append the seed summary as a frozen
      // user bubble so it shows live, identical to how the persisted seed row
      // renders on reload. It inserts above the editable input box.
      const input = new UserInput({ Source: 'user', Contents: event.message } as data.Message);
      this.element.insertBefore(input.component(), this.currentUserInput?.component() ?? null);
    });
  }
}