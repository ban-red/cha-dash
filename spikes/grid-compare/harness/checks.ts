/**
 * Layout checks, independent of any engine: paint cells, count what's wrong.
 */
import { SIZE_UNITS, type BoardData, type SizeName, type WidgetData } from '../scenarios/types.ts';
import type { PlacedSection, Snapshot } from './types.ts';

export interface Problems {
  /** Cells covered by two widgets, or two sections. */
  overlaps: number;
  /** Widgets or sections reaching outside their grid. */
  outOfBounds: number;
  /** Empty cells inside a section's used rows (columns × rows − covered cells). Trailing space in the last row counts too, so compare engines on the same content. */
  gaps: number;
  /** Widgets whose w×h isn't one of the sizes the widget declares. */
  unsupported: number;
  /** Widgets whose w×h isn't in the size system at all (e.g. 3×2). */
  offSystem: number;
  /** Sections whose widgets need more rows than the section box has (content clipped or spilling). */
  overflow: number;
}

export const NO_PROBLEMS: Problems = { overlaps: 0, outOfBounds: 0, gaps: 0, unsupported: 0, offSystem: 0, overflow: 0 };

export function total(p: Problems): number {
  return p.overlaps + p.outOfBounds + p.gaps + p.unsupported + p.offSystem + p.overflow;
}

function sizeOf(w: number, h: number): SizeName | undefined {
  return (Object.keys(SIZE_UNITS) as SizeName[]).find((s) => SIZE_UNITS[s].w === w && SIZE_UNITS[s].h === h);
}

function paint(boxes: readonly { x: number; y: number; w: number; h: number }[], columns: number) {
  const cells = new Map<number, number>();
  let overlaps = 0;
  let outOfBounds = 0;
  let rows = 0;
  for (const b of boxes) {
    if (b.x < 0 || b.y < 0 || b.x + b.w > columns) outOfBounds++;
    rows = Math.max(rows, b.y + b.h);
    for (let y = b.y; y < b.y + b.h; y++) {
      for (let x = b.x; x < b.x + b.w; x++) {
        const k = y * 1000 + x;
        const n = (cells.get(k) ?? 0) + 1;
        if (n === 2) overlaps++;
        cells.set(k, n);
      }
    }
  }
  let gaps = 0;
  for (let y = 0; y < rows; y++) {
    for (let x = 0; x < columns; x++) if (!cells.has(y * 1000 + x)) gaps++;
  }
  return { overlaps, outOfBounds, gaps, rows };
}

export function widgetsById(board: BoardData, extra: readonly WidgetData[] = []): Map<string, WidgetData> {
  const m = new Map<string, WidgetData>();
  for (const s of board.sections) for (const i of s.items) m.set(i.id, i);
  for (const i of extra) m.set(i.id, i);
  return m;
}

export function checkSection(section: PlacedSection, catalog: Map<string, WidgetData>): Problems {
  const p = paint(section.items, section.columns);
  let unsupported = 0;
  let offSystem = 0;
  for (const it of section.items) {
    const size = sizeOf(it.w, it.h);
    if (!size) offSystem++;
    const widget = catalog.get(it.id);
    if (!size || !widget || !widget.supportedSizes.includes(size)) unsupported++;
  }
  return {
    overlaps: p.overlaps,
    outOfBounds: p.outOfBounds,
    gaps: p.gaps,
    unsupported,
    offSystem,
    overflow: p.rows > section.rows ? 1 : 0,
  };
}

export function checkSnapshot(snap: Snapshot, catalog: Map<string, WidgetData>): Problems {
  const out = { ...NO_PROBLEMS };
  for (const s of snap.sections) {
    const p = checkSection(s, catalog);
    for (const k of Object.keys(out) as (keyof Problems)[]) out[k] += p[k];
  }
  // sections against each other on the board
  const board = paint(
    snap.sections.map((s) => ({ x: s.x, y: s.y, w: s.columns, h: s.rows })),
    snap.columns,
  );
  out.overlaps += board.overlaps; // board-level gaps between sections aren't counted: bands are ragged by design
  out.outOfBounds += board.outOfBounds;
  return out;
}

/** A stable text form of a snapshot, for equality checks and diffs. */
export function layoutKey(snap: Snapshot): string {
  return snap.sections
    .map((s) => `${s.id}@${s.x},${s.y}/${s.columns}c:` + [...s.items].sort((a, b) => a.y - b.y || a.x - b.x).map((i) => `${i.id}(${i.x},${i.y},${i.w}x${i.h})`).join(' '))
    .join(' | ');
}

/** Widgets whose position or size differs between two snapshots. */
export function moved(a: Snapshot, b: Snapshot): string[] {
  const pos = (s: Snapshot) => new Map(s.sections.flatMap((sec) => sec.items.map((i) => [i.id, `${sec.id}:${i.x},${i.y},${i.w}x${i.h}`] as const)));
  const pa = pos(a);
  const pb = pos(b);
  return [...pa.keys()].filter((id) => pb.has(id) && pa.get(id) !== pb.get(id));
}
