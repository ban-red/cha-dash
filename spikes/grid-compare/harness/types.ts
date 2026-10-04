import type { BoardData, Columns, SizeName, Target, WidgetData } from '../scenarios/types.ts';

/** One placed widget, in its section's own cells. */
export interface PlacedWidget {
  readonly id: string;
  readonly x: number;
  readonly y: number;
  readonly w: number;
  readonly h: number;
}

/** One placed section, in board cells, with its widgets. */
export interface PlacedSection {
  readonly id: string;
  /** Board column and row of the section's top-left. */
  readonly x: number;
  readonly y: number;
  /** Board columns the section covers = its internal column count. */
  readonly columns: number;
  /** Rows the section box is given on the board. */
  readonly rows: number;
  readonly items: readonly PlacedWidget[];
}

/** What an engine shows at one column count. */
export interface Snapshot {
  readonly columns: Columns;
  readonly sections: readonly PlacedSection[];
}

/**
 * The adapter contract. Every adapter drives its engine only through the
 * engine's public API, the way an app would.
 */
export interface GridAdapter {
  readonly name: string;
  load(board: BoardData): void;
  move(item: string, to: Target): void;
  resize(item: string, size: SizeName): void;
  insert(item: WidgetData, near?: Target): void;
  /** Switch the viewport to a column count (what a window resize does). */
  setColumns(columns: Columns): void;
  /** One undo step. */
  undo(): void;
  serialize(): string;
  restore(json: string): void;
  snapshot(): Snapshot;
  dispose(): void;
}
