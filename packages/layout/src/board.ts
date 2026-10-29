import { LayoutError } from './errors.ts';
import { Occupancy } from './pack.ts';
import { SIZES, isSizeId, sizeFits, type SizeId } from './sizes.ts';
import {
  BREAKPOINT_COLUMNS,
  CANONICAL_COLUMNS,
  DERIVED_BREAKPOINTS,
  FLOWS,
  type Board,
  type BoardInput,
  type DerivedBreakpoint,
  type Item,
  type ItemInput,
  type Section,
  type SectionInput,
  type SectionOverride,
  type SectionOverrides,
} from './types.ts';

// ---------------------------------------------------------------------------
// Reflow
// ---------------------------------------------------------------------------

/**
 * Recompute canonical positions of a section's items from their order.
 * Items whose position doesn't change keep their object identity, and the
 * input array is returned as-is when nothing moved.
 */
export function packItems(items: readonly Item[], columns: number): readonly Item[] {
  const grid = new Occupancy(columns);
  let changed = false;
  const out = items.map((item) => {
    const { w, h } = SIZES[item.size];
    const { x, y } = grid.add(w, h);
    if (item.x === x && item.y === y) return item;
    changed = true;
    return { ...item, x, y };
  });
  return changed ? out : items;
}

/**
 * Re-run the dense Z-reflow on every section. Boards produced by this
 * package are always already reflowed, so this returns the same board; it
 * exists for boards assembled by hand. Idempotent.
 */
export function reflow(board: Board): Board {
  let changed = false;
  const sections = board.sections.map((section) => {
    const items = packItems(section.items, section.span);
    if (items === section.items) return section;
    changed = true;
    return { ...section, items };
  });
  return changed ? { ...board, sections } : board;
}

// ---------------------------------------------------------------------------
// Overrides normal form
// ---------------------------------------------------------------------------

/**
 * Merge a (possibly stale or partial) override order with the canonical
 * order: drop unknown and duplicate ids, then slot every missing item in
 * right after its canonical predecessor (or first, if it has none).
 */
export function mergeOrder(order: readonly string[], canonical: readonly string[]): string[] {
  const known = new Set(canonical);
  const seen = new Set<string>();
  const result: string[] = [];
  for (const id of order) {
    if (known.has(id) && !seen.has(id)) {
      seen.add(id);
      result.push(id);
    }
  }
  let previous: string | undefined;
  for (const id of canonical) {
    if (!seen.has(id)) {
      const at = previous === undefined ? 0 : result.indexOf(previous) + 1;
      result.splice(at, 0, id);
      seen.add(id);
    }
    previous = id;
  }
  return result;
}

function sameIds(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((id, i) => id === b[i]);
}

/**
 * Bring one breakpoint override into normal form against the section's
 * current items. Returns undefined when nothing is left in it.
 */
function normalizeOverride(
  override: SectionOverride,
  items: readonly Item[],
): SectionOverride | undefined {
  const byId = new Map(items.map((item) => [item.id, item]));
  let order: readonly string[] | undefined;
  if (override.order !== undefined) {
    const canonical = items.map((item) => item.id);
    const merged = mergeOrder(override.order, canonical);
    order = override.order.length === merged.length && sameIds(override.order, merged) ? override.order : merged;
  }
  let sizes: { [id: string]: SizeId } | undefined;
  if (override.sizes !== undefined) {
    let dropped = false;
    const kept: { [id: string]: SizeId } = {};
    for (const [id, size] of Object.entries(override.sizes)) {
      const item = byId.get(id);
      if (item !== undefined && item.supportedSizes.includes(size)) kept[id] = size;
      else dropped = true;
    }
    if (Object.keys(kept).length > 0) sizes = dropped ? kept : override.sizes;
  }
  const out: { -readonly [K in keyof SectionOverride]: SectionOverride[K] } = {};
  if (override.span !== undefined) out.span = override.span;
  if (order !== undefined && order.length > 0) out.order = order;
  if (sizes !== undefined) out.sizes = sizes;
  if (Object.keys(out).length === 0) return undefined;
  if (out.span === override.span && out.order === override.order && out.sizes === override.sizes) {
    return override;
  }
  return out;
}

/** Normalize all overrides of a section; undefined when none remain. */
export function normalizeOverrides(
  overrides: SectionOverrides | undefined,
  items: readonly Item[],
): SectionOverrides | undefined {
  if (overrides === undefined) return undefined;
  const out: { -readonly [K in DerivedBreakpoint]?: SectionOverride } = {};
  let changed = false;
  for (const bp of DERIVED_BREAKPOINTS) {
    const current = overrides[bp];
    if (current === undefined) continue;
    const next = normalizeOverride(current, items);
    if (next !== current) changed = true;
    if (next !== undefined) out[bp] = next;
  }
  if (Object.keys(out).length === 0) return undefined;
  return changed ? out : overrides;
}

/**
 * Build a section from new canonical items: reflow positions and keep the
 * overrides in normal form. Keeps identity where nothing changed.
 */
export function withItems(section: Section, items: readonly Item[]): Section {
  const packed = packItems(items, section.span);
  const overrides = normalizeOverrides(section.overrides, packed);
  if (packed === section.items && overrides === section.overrides) return section;
  return withOverrides({ ...section, items: packed }, overrides);
}

/** Replace a section's overrides (removing the key when undefined). */
export function withOverrides(section: Section, overrides: SectionOverrides | undefined): Section {
  if (overrides === section.overrides) return section;
  const { overrides: _old, ...rest } = section;
  return overrides === undefined ? rest : { ...rest, overrides };
}

export function replaceSection(board: Board, index: number, section: Section): Board {
  if (board.sections[index] === section) return board;
  const sections = board.sections.slice();
  sections[index] = section;
  return { ...board, sections };
}

// ---------------------------------------------------------------------------
// Lookups
// ---------------------------------------------------------------------------

export interface ItemLocation {
  readonly section: Section;
  readonly sectionIndex: number;
  readonly item: Item;
  /** Index in the section's canonical order. */
  readonly index: number;
}

/** Find an item anywhere on the board. */
export function findItem(board: Board, id: string): ItemLocation | undefined {
  for (let s = 0; s < board.sections.length; s++) {
    const section = board.sections[s] as Section;
    const index = section.items.findIndex((item) => item.id === id);
    if (index !== -1) {
      return { section, sectionIndex: s, item: section.items[index] as Item, index };
    }
  }
  return undefined;
}

export function getItem(board: Board, id: string): Item | undefined {
  return findItem(board, id)?.item;
}

export function getSection(board: Board, id: string): Section | undefined {
  return board.sections.find((section) => section.id === id);
}

export function locateItem(board: Board, id: string): ItemLocation {
  const found = findItem(board, id);
  if (found === undefined) throw new LayoutError('UNKNOWN_ITEM', `no item with id "${id}"`);
  return found;
}

export function locateSection(board: Board, id: string): number {
  const index = board.sections.findIndex((section) => section.id === id);
  if (index === -1) throw new LayoutError('UNKNOWN_SECTION', `no section with id "${id}"`);
  return index;
}

// ---------------------------------------------------------------------------
// Validation and construction
// ---------------------------------------------------------------------------

function isNonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0;
}

/** Problems with an item's own fields (not its position). */
export function itemProblems(item: ItemInput, where: string): string[] {
  const problems: string[] = [];
  if (!isNonEmptyString(item.id)) problems.push(`${where}: id must be a non-empty string`);
  if (!isNonEmptyString(item.type)) problems.push(`${where}: type must be a non-empty string`);
  if (!Array.isArray(item.supportedSizes) || item.supportedSizes.length === 0) {
    problems.push(`${where}: supportedSizes must be a non-empty array`);
    return problems;
  }
  const seen = new Set<string>();
  for (const size of item.supportedSizes) {
    if (!isSizeId(size)) problems.push(`${where}: unknown size "${String(size)}" in supportedSizes`);
    else if (seen.has(size)) problems.push(`${where}: duplicate size "${size}" in supportedSizes`);
    seen.add(size);
  }
  if (!isSizeId(item.size)) problems.push(`${where}: unknown size "${String(item.size)}"`);
  else if (!item.supportedSizes.includes(item.size)) {
    problems.push(`${where}: size "${item.size}" is not in supportedSizes`);
  }
  return problems;
}

export function isValidSpan(span: unknown, max: number): span is number {
  return typeof span === 'number' && Number.isInteger(span) && span >= 1 && span <= max;
}

function overrideProblems(overrides: unknown, where: string): string[] {
  if (overrides === undefined) return [];
  if (typeof overrides !== 'object' || overrides === null || Array.isArray(overrides)) {
    return [`${where}: overrides must be an object`];
  }
  const problems: string[] = [];
  for (const [bp, override] of Object.entries(overrides)) {
    const at = `${where}.overrides.${bp}`;
    if (!(DERIVED_BREAKPOINTS as readonly string[]).includes(bp)) {
      problems.push(`${at}: unknown breakpoint (expected ${DERIVED_BREAKPOINTS.join(' or ')})`);
      continue;
    }
    if (typeof override !== 'object' || override === null || Array.isArray(override)) {
      problems.push(`${at}: must be an object`);
      continue;
    }
    const o = override as Record<string, unknown>;
    const columns = BREAKPOINT_COLUMNS[bp as DerivedBreakpoint];
    if (o['span'] !== undefined && !isValidSpan(o['span'], columns)) {
      problems.push(`${at}.span: must be an integer in 1..${columns}`);
    }
    if (o['order'] !== undefined) {
      if (!Array.isArray(o['order']) || !o['order'].every(isNonEmptyString)) {
        problems.push(`${at}.order: must be an array of item ids`);
      }
    }
    if (o['sizes'] !== undefined) {
      const sizes = o['sizes'];
      if (typeof sizes !== 'object' || sizes === null || Array.isArray(sizes)) {
        problems.push(`${at}.sizes: must be an object of item id to size`);
      } else {
        for (const [id, size] of Object.entries(sizes)) {
          if (!isSizeId(size)) problems.push(`${at}.sizes.${id}: unknown size "${String(size)}"`);
        }
      }
    }
  }
  return problems;
}

/** Problems with section and item fields, ids and fit (not positions). */
export function structureProblems(sections: readonly SectionInput[]): string[] {
  const problems: string[] = [];
  const sectionIds = new Set<string>();
  const itemIds = new Set<string>();
  sections.forEach((section, s) => {
    const where = `sections[${s}]`;
    if (!isNonEmptyString(section.id)) problems.push(`${where}: id must be a non-empty string`);
    else if (sectionIds.has(section.id)) problems.push(`${where}: duplicate section id "${section.id}"`);
    else sectionIds.add(section.id);
    if (section.title !== undefined && typeof section.title !== 'string') {
      problems.push(`${where}: title must be a string`);
    }
    const span = section.span ?? CANONICAL_COLUMNS;
    if (!isValidSpan(span, CANONICAL_COLUMNS)) {
      problems.push(`${where}: span must be an integer in 1..${CANONICAL_COLUMNS}`);
    }
    problems.push(...overrideProblems(section.overrides, where));
    if (!Array.isArray(section.items ?? [])) {
      problems.push(`${where}: items must be an array`);
      return;
    }
    const items: readonly ItemInput[] = section.items ?? [];
    items.forEach((item, i) => {
      const at = `${where}.items[${i}]`;
      const own = itemProblems(item, at);
      problems.push(...own);
      if (isNonEmptyString(item.id)) {
        if (itemIds.has(item.id)) problems.push(`${at}: duplicate item id "${item.id}"`);
        itemIds.add(item.id);
      }
      if (own.length === 0 && isValidSpan(span, CANONICAL_COLUMNS) && !sizeFits(item.size, span)) {
        problems.push(`${at}: size "${item.size}" (${SIZES[item.size].w} wide) does not fit a ${span}-column section`);
      }
    });
  });
  return problems;
}

function toItem(input: ItemInput): Item {
  return {
    id: input.id,
    type: input.type,
    size: input.size,
    supportedSizes: [...input.supportedSizes],
    x: 0,
    y: 0,
  };
}

function copyOverrides(overrides: SectionOverrides | undefined): SectionOverrides | undefined {
  if (overrides === undefined) return undefined;
  const out: { -readonly [K in DerivedBreakpoint]?: SectionOverride } = {};
  for (const bp of DERIVED_BREAKPOINTS) {
    const o = overrides[bp];
    if (o === undefined) continue;
    const copy: { -readonly [K in keyof SectionOverride]: SectionOverride[K] } = {};
    if (o.span !== undefined) copy.span = o.span;
    if (o.order !== undefined) copy.order = [...o.order];
    if (o.sizes !== undefined) copy.sizes = { ...o.sizes };
    out[bp] = copy;
  }
  return out;
}

/** Build a section from validated input. */
export function buildSection(input: SectionInput): Section {
  const span = input.span ?? CANONICAL_COLUMNS;
  const items = packItems((input.items ?? []).map(toItem), span);
  const base: Section = input.title === undefined
    ? { id: input.id, span, items }
    : { id: input.id, title: input.title, span, items };
  return withOverrides(base, normalizeOverrides(copyOverrides(input.overrides), items));
}

/**
 * Create a board. Positions are computed from item order; any `x`/`y` on the
 * input is ignored. Throws `LayoutError('INVALID_BOARD')` listing every
 * problem found.
 */
export function createBoard(input: BoardInput = {}): Board {
  const sections = input.sections ?? [];
  const problems = structureProblems(sections);
  if (input.flow !== undefined && !FLOWS.includes(input.flow)) problems.push(`unknown flow "${String(input.flow)}"`);
  if (problems.length > 0) {
    throw new LayoutError('INVALID_BOARD', `invalid board:\n- ${problems.join('\n- ')}`);
  }
  return withFlow({ sections: sections.map(buildSection) }, input.flow);
}

/** A board with `flow` set; `sections` (the default) is stored as no flow at all. */
export function withFlow(board: Board, flow: Board['flow']): Board {
  if (flow === undefined || flow === 'sections') {
    if (board.flow === undefined) return board;
    const { flow: _, ...rest } = board;
    return rest;
  }
  return board.flow === flow ? board : { ...board, flow };
}
