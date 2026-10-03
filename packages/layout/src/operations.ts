import {
  findItem,
  itemProblems,
  locateItem,
  locateSection,
  replaceSection,
  withItems,
  withOverrides,
  type ItemLocation,
} from './board.ts';
import { automaticSize, itemSize, placeSectionItems, sectionColumns, sectionOrder } from './derive.ts';
import { LayoutError } from './errors.ts';
import {
  chooseIndex,
  insertionCells,
  naturalIndex,
  Occupancy,
  pack,
  type Cell,
  type Extent,
} from './pack.ts';
import { SIZES, closestFit, isSizeId, sizeFits, type SizeId } from './sizes.ts';
import type {
  Board,
  Breakpoint,
  DerivedBreakpoint,
  Item,
  ItemInput,
  Section,
  SectionOverride,
} from './types.ts';

/** A drop target: the desired top-left cell inside a section. */
export interface Target {
  readonly sectionId: string;
  readonly x: number;
  readonly y: number;
}

/** Like `Target`, but the section defaults to the item's current one. */
export interface MoveTarget {
  readonly sectionId?: string;
  readonly x: number;
  readonly y: number;
}

export interface OpOptions {
  /**
   * Which layout to edit. `desktop` (the default) edits the canonical
   * layout; `tablet`/`phone` write per-breakpoint overrides instead.
   */
  readonly breakpoint?: Breakpoint;
}

export interface SmartInsertOptions {
  /** Only consider this section. */
  readonly sectionId?: string;
  /**
   * Viewport hint: the section and canonical row the user is looking at.
   * The item lands as close to that row as possible, preferring spots that
   * displace the fewest other items.
   */
  readonly near?: { readonly sectionId: string; readonly y: number };
}

export type Direction = 'left' | 'right' | 'up' | 'down';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function extentOf(item: Item): Extent {
  return SIZES[item.size];
}

function without<T>(list: readonly T[], index: number): T[] {
  return [...list.slice(0, index), ...list.slice(index + 1)];
}

function insertAt<T>(list: readonly T[], index: number, value: T): T[] {
  return [...list.slice(0, index), value, ...list.slice(index)];
}

function assertTarget(target: { readonly x: number; readonly y: number }): void {
  if (typeof target.x !== 'number' || typeof target.y !== 'number' || Number.isNaN(target.x) || Number.isNaN(target.y)) {
    throw new LayoutError('INVALID_ARGUMENT', 'target x and y must be numbers');
  }
}

function assertNewItem(board: Board, input: ItemInput): void {
  const problems = itemProblems(input, `item "${String(input.id)}"`);
  if (problems.length > 0) {
    const unsupported = isSizeId(input.size) && Array.isArray(input.supportedSizes) && !input.supportedSizes.includes(input.size);
    throw new LayoutError(unsupported ? 'UNSUPPORTED_SIZE' : 'INVALID_ARGUMENT', problems.join('; '));
  }
  if (findItem(board, input.id) !== undefined) {
    throw new LayoutError('DUPLICATE_ID', `an item with id "${input.id}" already exists`);
  }
}

/** The item's size if it fits, else the closest supported size that does. */
function fitSize(size: SizeId, supportedSizes: readonly SizeId[], columns: number): SizeId | undefined {
  return sizeFits(size, columns) ? size : closestFit(supportedSizes, size, columns);
}

function fitSizeOrThrow(id: string, size: SizeId, supportedSizes: readonly SizeId[], columns: number): SizeId {
  const fitted = fitSize(size, supportedSizes, columns);
  if (fitted === undefined) {
    throw new LayoutError('NO_FIT', `item "${id}" has no supported size that fits ${columns} columns`);
  }
  return fitted;
}

function makeItem(input: ItemInput, size: SizeId): Item {
  return { id: input.id, type: input.type, size, supportedSizes: [...input.supportedSizes], x: 0, y: 0 };
}

/** Index for "append": lands in the earliest gap that fits and displaces nothing. */
function appendIndex(others: readonly Extent[], columns: number, moving: Extent): number {
  const cells = insertionCells(others, columns, moving);
  return naturalIndex(others, columns, moving, cells, others.length);
}

type OverridePatch = {
  readonly [K in keyof SectionOverride]?: SectionOverride[K] | undefined;
};

/** Apply a patch to one breakpoint override, dropping empty parts. */
function patchOverride(section: Section, breakpoint: DerivedBreakpoint, patch: OverridePatch): Section {
  const merged = { ...section.overrides?.[breakpoint], ...patch };
  const next: { -readonly [K in keyof SectionOverride]: SectionOverride[K] } = {};
  if (merged.span !== undefined) next.span = merged.span;
  if (merged.order !== undefined && merged.order.length > 0) next.order = merged.order;
  if (merged.sizes !== undefined && Object.keys(merged.sizes).length > 0) next.sizes = merged.sizes;
  const overrides: { -readonly [K in DerivedBreakpoint]?: SectionOverride } = { ...section.overrides };
  if (Object.keys(next).length === 0) delete overrides[breakpoint];
  else overrides[breakpoint] = next;
  return withOverrides(section, Object.keys(overrides).length === 0 ? undefined : overrides);
}

/** Picks an insertion index; `current` is the item's index when it stays in its section. */
type IndexPicker = (
  others: readonly Extent[],
  columns: number,
  moving: Extent,
  current?: number,
) => number | undefined;

/** Reorder an item inside its section at a derived breakpoint (writes an override order). */
function reorderAtBreakpoint(board: Board, loc: ItemLocation, breakpoint: DerivedBreakpoint, pickIndex: IndexPicker): Board {
  const { section } = loc;
  const columns = sectionColumns(section, breakpoint);
  const order = sectionOrder(section, breakpoint);
  const current = order.findIndex((item) => item.id === loc.item.id);
  const rest = without(order, current);
  const sizeOf = (item: Item): Extent => SIZES[itemSize(section, item, breakpoint).size];
  const index = pickIndex(rest.map(sizeOf), columns, sizeOf(loc.item), current);
  if (index === undefined || index === current) return board;
  const ids = insertAt(rest, index, loc.item).map((item) => item.id);
  const canonical = section.items.map((item) => item.id);
  const same = ids.every((id, i) => id === canonical[i]);
  return replaceSection(board, loc.sectionIndex, patchOverride(section, breakpoint, { order: same ? undefined : ids }));
}

/** Reorder an item inside its section in the canonical layout. */
function reorderCanonical(board: Board, loc: ItemLocation, pickIndex: IndexPicker): Board {
  const { section } = loc;
  const rest = without(section.items, loc.index);
  const index = pickIndex(rest.map(extentOf), section.span, extentOf(loc.item), loc.index);
  if (index === undefined || index === loc.index) return board;
  return replaceSection(board, loc.sectionIndex, withItems(section, insertAt(rest, index, loc.item)));
}

/** Move an item into another section (canonical layout only). */
function moveAcross(board: Board, loc: ItemLocation, destIndex: number, pickIndex: IndexPicker): Board {
  const dest = board.sections[destIndex] as Section;
  const size = fitSizeOrThrow(loc.item.id, loc.item.size, loc.item.supportedSizes, dest.span);
  const item: Item = size === loc.item.size ? loc.item : { ...loc.item, size };
  const index = pickIndex(dest.items.map(extentOf), dest.span, SIZES[size]) ?? dest.items.length;
  const source = withItems(loc.section, without(loc.section.items, loc.index));
  const next = replaceSection(board, loc.sectionIndex, source);
  return replaceSection(next, destIndex, withItems(dest, insertAt(dest.items, index, item)));
}

function targetPicker(target: { readonly x: number; readonly y: number }): IndexPicker {
  return (others, columns, moving, current) => chooseIndex(others, columns, moving, target, undefined, current);
}

// ---------------------------------------------------------------------------
// Item operations
// ---------------------------------------------------------------------------

/**
 * Add a new item as close as possible to a target cell (for example, a drop
 * from the widget gallery). If its size doesn't fit the section, the closest
 * supported size that fits is used.
 */
export function place(board: Board, input: ItemInput, target: Target): Board {
  assertTarget(target);
  assertNewItem(board, input);
  const s = locateSection(board, target.sectionId);
  const section = board.sections[s] as Section;
  const size = fitSizeOrThrow(input.id, input.size, input.supportedSizes, section.span);
  const index = chooseIndex(section.items.map(extentOf), section.span, SIZES[size], target) as number;
  return replaceSection(board, s, withItems(section, insertAt(section.items, index, makeItem(input, size))));
}

/**
 * Add a new item without a target: find the best spot.
 *
 * - Without hints it goes into the first section where it fits, in the
 *   earliest gap that fits (or at the end), so nothing else moves.
 * - With `near`, it lands as close as possible to that row of that section
 *   (displacing the fewest items among equally close spots), falling back to
 *   the nearest other section where it fits.
 */
export function smartInsert(board: Board, input: ItemInput, options: SmartInsertOptions = {}): Board {
  assertNewItem(board, input);
  if (board.sections.length === 0) throw new LayoutError('NO_SECTION', 'the board has no sections');
  if (options.near !== undefined && (typeof options.near.y !== 'number' || Number.isNaN(options.near.y))) {
    throw new LayoutError('INVALID_ARGUMENT', 'near.y must be a number');
  }
  const center = options.near === undefined ? undefined : locateSection(board, options.near.sectionId);
  let candidates: number[];
  if (options.sectionId !== undefined) {
    candidates = [locateSection(board, options.sectionId)];
  } else if (center !== undefined) {
    candidates = board.sections
      .map((_, i) => i)
      .sort((a, b) => Math.abs(a - center) - Math.abs(b - center) || a - b);
  } else {
    candidates = board.sections.map((_, i) => i);
  }
  for (const s of candidates) {
    const section = board.sections[s] as Section;
    const size = fitSize(input.size, input.supportedSizes, section.span);
    if (size === undefined) continue;
    const moving = SIZES[size];
    const others = section.items.map(extentOf);
    const near = options.near?.sectionId === section.id ? options.near : undefined;
    const index = near === undefined
      ? appendIndex(others, section.span, moving)
      : nearIndex(others, section.span, moving, near.y);
    return replaceSection(board, s, withItems(section, insertAt(section.items, index, makeItem(input, size))));
  }
  throw new LayoutError('NO_FIT', `item "${input.id}" has no supported size that fits any section`);
}

/** Insertion index closest to row `y`, then fewest displaced items, then earliest. */
function nearIndex(others: readonly Extent[], columns: number, moving: Extent, y: number): number {
  const ty = Math.max(0, Math.round(Math.min(y, 1_000_000)));
  const cells = insertionCells(others, columns, moving);
  const current = pack(others, columns);
  let best = 0;
  let bestKey: [number, number] | undefined;
  for (let i = 0; i < cells.length; i++) {
    const cell = cells[i] as Cell;
    const previous = cells[i - 1];
    if (previous !== undefined && previous.x === cell.x && previous.y === cell.y) continue;
    const key: [number, number] = [Math.abs(cell.y - ty), displaced(others, columns, moving, i, cell, current)];
    if (bestKey === undefined || key[0] < bestKey[0] || (key[0] === bestKey[0] && key[1] < bestKey[1])) {
      best = i;
      bestKey = key;
    }
  }
  return naturalIndex(others, columns, moving, cells, best);
}

/** How many of `others` change position if `moving` is inserted at `index`. */
function displaced(
  others: readonly Extent[],
  columns: number,
  moving: Extent,
  index: number,
  landing: Cell,
  current: readonly Cell[],
): number {
  const grid = new Occupancy(columns);
  let count = 0;
  for (let i = 0; i < others.length; i++) {
    if (i === index) grid.occupy(landing.x, landing.y, moving.w, moving.h);
    const e = others[i] as Extent;
    const cell = grid.add(e.w, e.h);
    const was = current[i] as Cell;
    if (cell.x !== was.x || cell.y !== was.y) count++;
  }
  return count;
}

/**
 * Move an item so its top-left lands as close as possible to the target
 * cell; neighbours reflow. Targets are clamped, never rejected.
 *
 * At `desktop` the target may be in another section (the item adopts the
 * closest supported size that fits there). At `tablet`/`phone` the move
 * stays inside the item's section and is stored as an override order.
 * Returns the same board object when nothing changes.
 */
export function move(board: Board, id: string, target: MoveTarget, options: OpOptions = {}): Board {
  assertTarget(target);
  const breakpoint = options.breakpoint ?? 'desktop';
  const loc = locateItem(board, id);
  const destId = target.sectionId ?? loc.section.id;
  if (breakpoint !== 'desktop') {
    if (destId !== loc.section.id) {
      throw new LayoutError(
        'CROSS_SECTION_AT_BREAKPOINT',
        `moving "${id}" to another section only works on the canonical (desktop) layout`,
      );
    }
    return reorderAtBreakpoint(board, loc, breakpoint, targetPicker(target));
  }
  if (destId === loc.section.id) return reorderCanonical(board, loc, targetPicker(target));
  return moveAcross(board, loc, locateSection(board, destId), targetPicker(target));
}

/**
 * Resize an item to one of its supported sizes. The item keeps its place in
 * the order and the section reflows.
 *
 * At `tablet`/`phone` this stores a per-breakpoint size override (or clears
 * it when the size equals what derivation would pick anyway).
 * Throws `UNSUPPORTED_SIZE` for sizes the widget doesn't declare and
 * `NO_FIT` for sizes wider than the section at that breakpoint.
 */
export function resize(board: Board, id: string, size: SizeId, options: OpOptions = {}): Board {
  const loc = locateItem(board, id);
  const { item, section } = loc;
  if (!isSizeId(size) || !item.supportedSizes.includes(size)) {
    throw new LayoutError('UNSUPPORTED_SIZE', `item "${id}" does not support size "${String(size)}"`);
  }
  const breakpoint = options.breakpoint ?? 'desktop';
  const columns = sectionColumns(section, breakpoint);
  if (!sizeFits(size, columns)) {
    throw new LayoutError('NO_FIT', `size "${size}" does not fit ${columns} columns`);
  }
  if (breakpoint === 'desktop') {
    if (size === item.size) return board;
    const items = section.items.slice();
    items[loc.index] = { ...item, size };
    return replaceSection(board, loc.sectionIndex, withItems(section, items));
  }
  const sizes = { ...section.overrides?.[breakpoint]?.sizes };
  const desired = size === automaticSize(section, item, breakpoint) ? undefined : size;
  if (sizes[id] === desired) return board;
  if (desired === undefined) delete sizes[id];
  else sizes[id] = desired;
  return replaceSection(board, loc.sectionIndex, patchOverride(section, breakpoint, { sizes }));
}

/** Remove an item (and any overrides that mention it). */
export function remove(board: Board, id: string): Board {
  const loc = locateItem(board, id);
  return replaceSection(board, loc.sectionIndex, withItems(loc.section, without(loc.section.items, loc.index)));
}

/**
 * Keyboard move by one step.
 *
 * - `left`/`right` swap the item with its neighbour in Z order.
 * - `up`/`down` move it to the nearest spot in the row above/below.
 * - At a section's edge on `desktop` it crosses into the previous section
 *   (appended) or the next one (first), if it fits there. At derived
 *   breakpoints it stops at the edge.
 *
 * Returns the same board when there is nowhere to go.
 */
export function nudge(board: Board, id: string, direction: Direction, options: OpOptions = {}): Board {
  const breakpoint = options.breakpoint ?? 'desktop';
  const loc = locateItem(board, id);
  const { section } = loc;
  const placed = placeSectionItems(section, breakpoint);
  const current = placed.findIndex((p) => p.id === id);
  const here = placed[current] as Cell;
  const count = placed.length;

  const picker: IndexPicker = (others, columns, moving) => {
    switch (direction) {
      case 'left':
        return current > 0 ? current - 1 : undefined;
      case 'right':
        return current < count - 1 ? current + 1 : undefined;
      case 'up':
        return chooseIndex(others, columns, moving, { x: here.x, y: here.y - 1 }, (c) => c.y < here.y);
      case 'down':
        return chooseIndex(others, columns, moving, { x: here.x, y: here.y + 1 }, (c) => c.y > here.y);
    }
  };

  const next = breakpoint === 'desktop'
    ? reorderCanonical(board, loc, picker)
    : reorderAtBreakpoint(board, loc, breakpoint, picker);
  if (next !== board || breakpoint !== 'desktop') return next;

  // At the edge of the section: cross into the adjacent one.
  const forward = direction === 'right' || direction === 'down';
  const destIndex = loc.sectionIndex + (forward ? 1 : -1);
  const dest = board.sections[destIndex];
  if (dest === undefined) return board;
  if (fitSize(loc.item.size, loc.item.supportedSizes, dest.span) === undefined) return board;
  return moveAcross(board, loc, destIndex, (others, columns, moving) =>
    forward ? 0 : appendIndex(others, columns, moving),
  );
}
