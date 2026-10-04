/**
 * Our engine, @chadash/layout, behind the adapter contract. Pure functions:
 * every operation returns a new board, history is a stack of references.
 */
import {
  commit,
  createBoard,
  createHistory,
  deriveLayout,
  deserialize,
  move,
  place,
  resize,
  serialize,
  smartInsert,
  undo,
  type Board,
  type Breakpoint,
  type History,
} from '@chadash/layout';
import type { BoardData, Columns, SizeName, Target, WidgetData } from '../scenarios/types.ts';
import type { GridAdapter, PlacedSection, Snapshot } from '../harness/types.ts';

const BREAKPOINT: { readonly [K in Columns]: Breakpoint } = { 12: 'desktop', 8: 'tablet', 4: 'phone' };

export function snapshotOf(board: Board, columns: Columns): Snapshot {
  const layout = deriveLayout(board, BREAKPOINT[columns]);
  // bands stack: a band is as tall as its tallest section
  const bandRows: number[] = [];
  for (const s of layout.sections) bandRows[s.band] = Math.max(bandRows[s.band] ?? 0, s.rows);
  const bandTop = bandRows.map((_, i) => bandRows.slice(0, i).reduce((a, b) => a + b, 0));
  const sections: PlacedSection[] = layout.sections.map((s) => ({
    id: s.id,
    x: s.column,
    y: bandTop[s.band] ?? 0,
    columns: s.span,
    rows: s.rows,
    items: s.items.map(({ id, x, y, w, h }) => ({ id, x, y, w, h })),
  }));
  return { columns, sections };
}

export class ChadashAdapter implements GridAdapter {
  readonly name = '@chadash/layout';
  private history: History<Board> = createHistory(createBoard());
  private columns: Columns = 12;

  get board(): Board {
    return this.history.present;
  }

  private get breakpoint(): Breakpoint {
    return BREAKPOINT[this.columns];
  }

  private apply(next: Board): void {
    this.history = commit(this.history, next);
  }

  load(board: BoardData): void {
    this.history = createHistory(createBoard({ sections: board.sections.map((s) => ({ ...s, items: [...s.items] })) }));
    this.columns = 12;
  }

  move(item: string, to: Target): void {
    this.apply(move(this.board, item, { sectionId: to.section, x: to.x, y: to.y }, { breakpoint: this.breakpoint }));
  }

  resize(item: string, size: SizeName): void {
    this.apply(resize(this.board, item, size, { breakpoint: this.breakpoint }));
  }

  insert(item: WidgetData, near?: Target): void {
    const input = { ...item, supportedSizes: [...item.supportedSizes] };
    this.apply(near ? place(this.board, input, { sectionId: near.section, x: near.x, y: near.y }) : smartInsert(this.board, input));
  }

  setColumns(columns: Columns): void {
    this.columns = columns; // derivation is a pure function of the board: nothing to recompute or cache
  }

  undo(): void {
    this.history = undo(this.history);
  }

  serialize(): string {
    return serialize(this.board);
  }

  restore(json: string): void {
    this.history = createHistory(deserialize(json));
  }

  snapshot(): Snapshot {
    return snapshotOf(this.board, this.columns);
  }

  dispose(): void {}
}
