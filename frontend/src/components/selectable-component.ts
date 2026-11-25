import { Observable } from "rxjs";

export interface SelectableComponent<T> {
  onSelection: Observable<T>;
  clearSelection(): void;
}
