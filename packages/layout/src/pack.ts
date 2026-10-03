/**
 * Dense Z-order packing.
 *
 * Items are placed one by one, in order, each at the first free cell in
 * reading order (top to bottom, left to right) where it fits. This is the
 * same algorithm as CSS `grid-auto-flow: row dense`, so a renderer that
 * feeds the same order into a dense CSS grid gets the same picture.
 *
 * Two properties follow and the rest of the engine leans on them:
 * - **Prefix stability:** an item's position depends only on the items
 *   before it, so an edit at order index k never moves items before k.
 * - **Monotone insertion:** the cell an inserted item lands on moves forward
 *   (in reading order) as its insertion index grows, so equal cells form one
 *   contiguous run of indices.
 */

export interface Cell {
  readonly x: number;
  readonly y: number;
}

export interface Extent {
  readonly w: number;
  readonly h: number;
}

/** Largest grid the bitmask rows support. Boards use at most 12. */
export const MAX_COLUMNS = 30;

/** Mutable occupancy grid: one bitmask per row. Internal to the engine. */
export class Occupancy {
  readonly columns: number;
  private readonly rows: number[] = [];
  private readonly full: number;
  private firstOpen = 0;

  constructor(columns: number) {
    if (!Number.isInteger(columns) || columns < 1 || columns > MAX_COLUMNS) {
      throw new RangeError(`columns must be an integer in 1..${MAX_COLUMNS}, got ${columns}`);
    }
    this.columns = columns;
    this.full = 2 ** columns - 1;
  }

  /** Rows touched so far (the packed height). */
  get height(): number {
    return this.rows.length;
  }

  /** First cell in reading order where a w×h block fits. */
  firstFit(w: number, h: number): Cell {
    const { columns, rows, full } = this;
    if (w < 1 || h < 1 || w > columns) {
      throw new RangeError(`cannot fit ${w}×${h} in ${columns} columns`);
    }
    const base = 2 ** w - 1;
    for (let y = this.firstOpen; ; y++) {
      const row = rows[y] ?? 0;
      if (row === full) continue;
      for (let x = 0; x <= columns - w; x++) {
        const mask = base << x;
        if ((row & mask) !== 0) continue;
        let free = true;
        for (let r = y + 1; r < y + h; r++) {
          if (((rows[r] ?? 0) & mask) !== 0) {
            free = false;
            break;
          }
        }
        if (free) return { x, y };
      }
    }
  }

  occupy(x: number, y: number, w: number, h: number): void {
    const mask = (2 ** w - 1) << x;
    const { rows } = this;
    while (rows.length < y + h) rows.push(0);
    for (let r = y; r < y + h; r++) rows[r] = (rows[r] as number) | mask;
    while (this.firstOpen < rows.length && rows[this.firstOpen] === this.full) this.firstOpen++;
  }

  /** firstFit + occupy. */
  add(w: number, h: number): Cell {
    const cell = this.firstFit(w, h);
    this.occupy(cell.x, cell.y, w, h);
    return cell;
  }
}

/** Pack extents in order into a grid `columns` wide. */
export function pack(extents: readonly Extent[], columns: number): Cell[] {
  const grid = new Occupancy(columns);
  return extents.map(({ w, h }) => grid.add(w, h));
}

/** Packed height in rows of a list of placed rectangles. */
export function packedHeight(cells: readonly Cell[], extents: readonly Extent[]): number {
  let rows = 0;
  for (let i = 0; i < cells.length; i++) {
    const bottom = (cells[i] as Cell).y + (extents[i] as Extent).h;
    if (bottom > rows) rows = bottom;
  }
  return rows;
}

/** Reading order: row first, then column. */
export function readingCompare(a: Cell, b: Cell): number {
  return a.y - b.y || a.x - b.x;
}

/**
 * Where an item of extent `moving` would land if inserted at each index
 * 0..others.length of `others`. Thanks to prefix stability this costs one
 * pass, not one pack per index.
 */
export function insertionCells(others: readonly Extent[], columns: number, moving: Extent): Cell[] {
  const grid = new Occupancy(columns);
  const cells: Cell[] = [];
  for (let i = 0; i <= others.length; i++) {
    cells.push(grid.firstFit(moving.w, moving.h));
    const next = others[i];
    if (next !== undefined) grid.add(next.w, next.h);
  }
  return cells;
}

function sameCell(a: Cell, b: Cell): boolean {
  return a.x === b.x && a.y === b.y;
}

/**
 * Given insertion cells and a chosen index, the run [lo, hi] of indices that
 * land on the same cell. All of them produce the identical packed layout.
 */
export function tieRun(cells: readonly Cell[], index: number): [number, number] {
  const cell = cells[index] as Cell;
  let lo = index;
  let hi = index;
  while (lo > 0 && sameCell(cells[lo - 1] as Cell, cell)) lo--;
  while (hi < cells.length - 1 && sameCell(cells[hi + 1] as Cell, cell)) hi++;
  return [lo, hi];
}

/**
 * Within a tie run every index gives the same picture; pick the one that
 * keeps the order closest to visual reading order (the number of other
 * items that sit before the landing cell), so derived breakpoints read the
 * way the desktop layout looks. If `prefer` (the item's current index) is in
 * the run, keep it: dropping an item back where it was changes nothing.
 */
export function naturalIndex(
  others: readonly Extent[],
  columns: number,
  moving: Extent,
  cells: readonly Cell[],
  index: number,
  prefer?: number,
): number {
  const [lo, hi] = tieRun(cells, index);
  if (lo === hi) return lo;
  if (prefer !== undefined && prefer >= lo && prefer <= hi) return prefer;
  const landing = cells[index] as Cell;
  const grid = new Occupancy(columns);
  let before = 0;
  for (let i = 0; i < others.length; i++) {
    if (i === lo) grid.occupy(landing.x, landing.y, moving.w, moving.h);
    const e = others[i] as Extent;
    const cell = grid.add(e.w, e.h);
    if (readingCompare(cell, landing) < 0) before++;
  }
  return Math.min(hi, Math.max(lo, before));
}

/** Largest coordinate a target is clamped to (keeps distances finite). */
const FAR = 1_000_000;

/**
 * Choose the insertion index whose landing cell is nearest (Euclidean) to
 * `target`, the desired top-left cell. Distance ties go to the earlier cell.
 * `filter` can restrict which landing cells qualify (used by keyboard moves);
 * `prefer` is the item's current index, kept when it lands on the same cell.
 * Returns undefined only when `filter` rejects every cell.
 */
export function chooseIndex(
  others: readonly Extent[],
  columns: number,
  moving: Extent,
  target: Cell,
  filter?: (cell: Cell) => boolean,
  prefer?: number,
): number | undefined {
  const tx = Math.min(Math.max(0, Math.round(target.x)), columns - moving.w);
  const ty = Math.min(Math.max(0, Math.round(target.y)), FAR);
  const cells = insertionCells(others, columns, moving);
  let best: number | undefined;
  let bestDistance = Infinity;
  for (let i = 0; i < cells.length; i++) {
    const cell = cells[i] as Cell;
    if (filter !== undefined && !filter(cell)) continue;
    const d = (cell.x - tx) ** 2 + (cell.y - ty) ** 2;
    if (d < bestDistance) {
      bestDistance = d;
      best = i;
    }
  }
  if (best === undefined) return undefined;
  return naturalIndex(others, columns, moving, cells, best, prefer);
}
