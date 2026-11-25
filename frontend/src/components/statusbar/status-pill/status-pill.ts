import { Component } from "../../component";

export class StatusPill implements Component {
  private element;

  constructor() {
    this.element = document.createElement('div');
    this.element.classList.add('status-pill');
  }

  public component(): HTMLElement {
    return this.element;
  }

  public setText(text: string) {
    this.element.textContent = text;
  }
}