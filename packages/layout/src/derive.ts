import { mergeOrder } from './board.ts';
import { pack, type Cell } from './pack.ts';
import { SIZES, autoSize, sizeFits, type SizeId } from './sizes.ts';
import {
  BREAKPOINTS,
  BREAKPOINT_COLUMNS,
  type Board,
  type Breakpoint,
  type DerivedLayout,
  type Item,
  type PlacedItem,
  type PlacedSection,
  type Section,
} from './types.ts';

/**
 * Internal column count of a section at a breakpoint: its override span if
 * set, else its canonical span capped at the board's columns. A section that
 * still fits keeps its internal structure exactly; a wider one is narrowed.
 */
export function sectionColumns(section: Section, breakpoint: Breakpoint): number {
  if (breakpoint === 'desktop') return section.span;
  return section.overrides?.[breakpoint]?.span ?? Math.min(section.span, BREAKPOINT_COLUMNS[breakpoint]);
}

/** Items of a section in their Z order at a breakpoint. */
export function sectionOrder(section: Section, breakpoint: Breakpoint): readonly Item[] {
  if (breakpoint === 'desktop') return section.items;
  const order = section.overrides?.[breakpoint]?.order;
  if (order === undefined) return section.items;
  const byId = new Map(section.items.map((item) => [item.id, item]));
  const ids = mergeOrder(
    order,
    section.items.map((item) => item.id),
  );
  return ids.map((id) => byId.get(id) as Item);
}

export interface EffectiveSize {
  readonly size: SizeId;
  readonly forced: boolean;
}

/**
 * The size an item shows at a breakpoint: a valid override size, else its
 * canonical size if it fits, else the closest supported size that fits,
 * else a forced system fallback.
 */
export function itemSize(section: Section, item: Item, breakpoint: Breakpoint): EffectiveSize {
  const columns = sectionColumns(section, breakpoint);
  if (breakpoint !== 'desktop') {
    const chosen = section.overrides?.[breakpoint]?.sizes?.[item.id];
    if (chosen !== undefined && item.supportedSizes.includes(chosen) && sizeFits(chosen, columns)) {
      return { size: chosen, forced: false };
    }
  }
  return autoSize(item.size, item.supportedSizes, columns);
}

/** The size derivation would pick at a breakpoint if there were no size override. */
export function automaticSize(section: Section, item: Item, breakpoint: Breakpoint): SizeId {
  return autoSize(item.size, item.supportedSizes, sectionColumns(section, breakpoint)).size;
}

function placed(item: Item, size: SizeId, cell: Cell, forced: boolean): PlacedItem {
  const { w, h } = SIZES[size];
  return { id: item.id, type: item.type, size, x: cell.x, y: cell.y, w, h, forced };
}

/** Lay out one section's items at a breakpoint (positions inside the section). */
export function placeSectionItems(section: Section, breakpoint: Breakpoint): readonly PlacedItem[] {
  if (breakpoint === 'desktop') {
    return section.items.map((item) => placed(item, item.size, item, false));
  }
  const columns = sectionColumns(section, breakpoint);
  const order = sectionOrder(section, breakpoint);
  const sizes = order.map((item) => itemSize(section, item, breakpoint));
  const cells = pack(
    sizes.map(({ size }) => SIZES[size]),
    columns,
  );
  return order.map((item, i) => {
    const { size, forced } = sizes[i] as EffectiveSize;
    return placed(item, size, cells[i] as Cell, forced);
  });
}

/**
 * Derive the full layout at a breakpoint. `desktop` returns the canonical
 * layout; `tablet` and `phone` are derived deterministically from it plus
 * any overrides.
 *
 * Sections flow across the board in Z order like CSS grid's default (sparse)
 * auto-placement: a section that doesn't fit in the rest of the band starts
 * a new band, and nothing back-fills.
 */
export function deriveLayout(board: Board, breakpoint: Breakpoint): DerivedLayout {
  const columns = BREAKPOINT_COLUMNS[breakpoint];
  let column = 0;
  let band = 0;
  const sections: PlacedSection[] = board.sections.map((section) => {
    const span = Math.min(sectionColumns(section, breakpoint), columns);
    if (column > 0 && column + span > columns) {
      band++;
      column = 0;
    }
    const items = placeSectionItems(section, breakpoint);
    let rows = 0;
    for (const item of items) rows = Math.max(rows, item.y + item.h);
    const out: PlacedSection = section.title === undefined
      ? { id: section.id, column, band, span, rows, items }
      : { id: section.id, title: section.title, column, band, span, rows, items };
    column += span;
    return out;
  });
  return { breakpoint, columns, sections };
}

/** Derive every breakpoint at once. */
export function deriveAll(board: Board): { readonly [K in Breakpoint]: DerivedLayout } {
  const out = {} as { [K in Breakpoint]: DerivedLayout };
  for (const bp of BREAKPOINTS) out[bp] = deriveLayout(board, bp);
  return out;
}
