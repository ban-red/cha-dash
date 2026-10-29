import { isValidSpan, structureProblems } from './board.ts';
import { deriveLayout } from './derive.ts';
import { pack } from './pack.ts';
import { SIZES, closestFit } from './sizes.ts';
import {
  BREAKPOINTS,
  BREAKPOINT_COLUMNS,
  DERIVED_BREAKPOINTS,
  FLOWS,
  TRACKS_PER_ROW,
  type Board,
  type DerivedLayout,
  type Item,
  type PlacedItem,
  type Section,
  type TrackArea,
} from './types.ts';

interface Rect {
  readonly id: string;
  readonly x: number;
  readonly y: number;
  readonly w: number;
  readonly h: number;
}

/** Bounds and overlap problems among rectangles in a grid `columns` wide. */
function geometryProblems(rects: readonly Rect[], columns: number, where: string): string[] {
  const problems: string[] = [];
  for (const r of rects) {
    if (!Number.isInteger(r.x) || !Number.isInteger(r.y)) {
      problems.push(`${where}: "${r.id}" has a non-integer position`);
    }
    if (r.x < 0 || r.y < 0 || r.x + r.w > columns) {
      problems.push(`${where}: "${r.id}" at ${r.x},${r.y} (${r.w}×${r.h}) is outside ${columns} columns`);
    }
  }
  for (let i = 0; i < rects.length; i++) {
    const a = rects[i] as Rect;
    for (let j = i + 1; j < rects.length; j++) {
      const b = rects[j] as Rect;
      if (a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h) {
        problems.push(`${where}: "${a.id}" overlaps "${b.id}"`);
      }
    }
  }
  return problems;
}

const trackRect = (id: string, a: TrackArea): Rect => ({ id, x: a.column, y: a.track, w: a.w, h: a.tracks });

/**
 * Bento and mosaic: what sits on the board (bento's section blocks, mosaic's
 * labels and tiles) is in bounds and never overlaps, and every tile's area
 * is its extent in tracks (inside its block, in bento).
 */
function trackProblems(layout: DerivedLayout): string[] {
  const where = `${layout.breakpoint}.${layout.flow}`;
  const problems: string[] = [];
  const onBoard: Rect[] = [];
  for (const s of layout.sections) {
    if (layout.flow === 'bento') {
      if (s.area !== undefined) onBoard.push(trackRect(s.id, s.area));
    } else if (s.header !== undefined) onBoard.push(trackRect(`${s.id} (label)`, s.header));
    for (const p of s.items) {
      const a = p.area;
      if (a === undefined) {
        problems.push(`${where}: "${p.id}" has no area`);
        continue;
      }
      if (a.w !== p.w || a.tracks !== p.h * TRACKS_PER_ROW) problems.push(`${where}: "${p.id}" area doesn't match its extent`);
      if (layout.flow === 'mosaic') onBoard.push(trackRect(p.id, a));
      else if (s.area === undefined || a.column < s.area.column || a.column + a.w > s.area.column + s.area.w || a.track < s.area.track || a.track + a.tracks > s.area.track + s.area.tracks) {
        problems.push(`${where}: "${p.id}" is outside its section's block`);
      }
    }
  }
  problems.push(...geometryProblems(onBoard, layout.columns, where));
  return problems;
}

function itemRect(item: Item): Rect {
  const { w, h } = SIZES[item.size];
  return { id: item.id, x: item.x, y: item.y, w, h };
}

function sectionProblems(section: Section, where: string): string[] {
  const problems: string[] = [];
  problems.push(...geometryProblems(section.items.map(itemRect), section.span, where));
  const expected = pack(
    section.items.map((item) => SIZES[item.size]),
    section.span,
  );
  section.items.forEach((item, i) => {
    const cell = expected[i];
    if (cell !== undefined && (cell.x !== item.x || cell.y !== item.y)) {
      problems.push(`${where}: "${item.id}" is at ${item.x},${item.y} but reflow puts it at ${cell.x},${cell.y}`);
    }
  });
  const ids = section.items.map((item) => item.id);
  const byId = new Map(section.items.map((item) => [item.id, item]));
  for (const bp of DERIVED_BREAKPOINTS) {
    const o = section.overrides?.[bp];
    if (o === undefined) continue;
    const at = `${where}.overrides.${bp}`;
    if (o.span === undefined && o.order === undefined && o.sizes === undefined) problems.push(`${at}: empty override`);
    if (o.span !== undefined && !isValidSpan(o.span, BREAKPOINT_COLUMNS[bp])) problems.push(`${at}: invalid span`);
    if (o.order !== undefined) {
      const sorted = [...o.order].sort();
      const expectedIds = [...ids].sort();
      if (sorted.length !== expectedIds.length || sorted.some((id, i) => id !== expectedIds[i])) {
        problems.push(`${at}: order is not a permutation of the section's items`);
      }
    }
    if (o.sizes !== undefined) {
      if (Object.keys(o.sizes).length === 0) problems.push(`${at}: empty sizes`);
      for (const [id, size] of Object.entries(o.sizes)) {
        const item = byId.get(id);
        if (item === undefined) problems.push(`${at}: size for unknown item "${id}"`);
        else if (!item.supportedSizes.includes(size)) problems.push(`${at}: "${id}" does not support "${size}"`);
      }
    }
  }
  return problems;
}

/**
 * Problems with a derived layout: items out of bounds or overlapping,
 * sections overflowing the board, or a forced size where a supported one
 * would have fit. Empty means valid.
 */
export function checkLayout(board: Board, layout: DerivedLayout): string[] {
  const problems: string[] = [];
  const where = layout.breakpoint;
  if (layout.sections.length !== board.sections.length) problems.push(`${where}: section count differs`);
  for (const placed of layout.sections) {
    const source = board.sections.find((s) => s.id === placed.id);
    const at = `${where}.${placed.id}`;
    if (source === undefined) {
      problems.push(`${at}: unknown section`);
      continue;
    }
    if (placed.column < 0 || placed.column + placed.span > layout.columns) {
      problems.push(`${at}: section overflows ${layout.columns} board columns`);
    }
    problems.push(...geometryProblems(placed.items, placed.span, at));
    const seen = new Set(placed.items.map((p) => p.id));
    if (seen.size !== placed.items.length || seen.size !== source.items.length || source.items.some((i) => !seen.has(i.id))) {
      problems.push(`${at}: items differ from the canonical section`);
    }
    for (const p of placed.items as readonly PlacedItem[]) {
      const item = source.items.find((i) => i.id === p.id);
      if (item === undefined) continue;
      const { w, h } = SIZES[p.size];
      if (p.w !== w || p.h !== h) problems.push(`${at}: "${p.id}" extent doesn't match size "${p.size}"`);
      const supported = item.supportedSizes.includes(p.size);
      if (supported === p.forced) problems.push(`${at}: "${p.id}" forced flag is wrong`);
      if (p.forced && closestFit(item.supportedSizes, item.size, placed.span) !== undefined) {
        problems.push(`${at}: "${p.id}" is forced although a supported size fits`);
      }
    }
  }
  if (layout.flow !== 'sections') problems.push(...trackProblems(layout));
  return problems;
}

/**
 * Every invariant the engine maintains, checked from scratch. Returns a list
 * of human-readable problems; empty means the board is valid.
 *
 * - ids are unique; sizes are supported and fit their section
 * - canonical positions equal the dense Z-reflow of item order (so items
 *   are in bounds and never overlap)
 * - overrides are in normal form (orders are permutations, sizes supported)
 * - every derived breakpoint is in bounds and overlap-free, in every flow
 */
export function checkInvariants(board: Board): string[] {
  const problems = structureProblems(board.sections);
  if (problems.length > 0) return problems;
  board.sections.forEach((section, s) => {
    problems.push(...sectionProblems(section, `sections[${s}]`));
  });
  if (board.flow !== undefined && !FLOWS.includes(board.flow)) problems.push(`unknown flow "${String(board.flow)}"`);
  for (const bp of BREAKPOINTS) {
    for (const flow of FLOWS) problems.push(...checkLayout(board, deriveLayout(board, bp, { flow })));
  }
  return problems;
}
