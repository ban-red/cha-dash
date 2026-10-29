import { mergeOrder } from './board.ts';
import { Occupancy, pack, type Cell } from './pack.ts';
import { SIZES, autoSize, sizeFits, type SizeId } from './sizes.ts';
import {
  BREAKPOINTS,
  BREAKPOINT_COLUMNS,
  TRACKS_PER_ROW,
  type Board,
  type Breakpoint,
  type DerivedLayout,
  type Flow,
  type Item,
  type PlacedItem,
  type PlacedSection,
  type Section,
  type TrackArea,
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

export interface DeriveOptions {
  /** Lay out with this flow instead of the board's: an editor shows a mosaic as bento. */
  readonly flow?: Flow;
  /** Bento and mosaic: `auto` gives titled sections a header track, `always` every section. */
  readonly headers?: 'auto' | 'always';
  /** Bento: the fewest rows a section's block keeps, so an empty one can still take a drop while editing. */
  readonly minRows?: number;
}

/** The narrowest a section's label is in mosaic (or the whole board, when it's narrower). */
export const MOSAIC_LABEL_COLUMNS = 2;

/**
 * Derive the full layout at a breakpoint. `desktop` returns the canonical
 * layout; `tablet` and `phone` are derived deterministically from it plus
 * any overrides. Tile sizes and positions inside a section are the same in
 * every flow; the flow only decides where sections and tiles sit on the board.
 *
 * - `sections`: sections flow across the board in Z order like CSS grid's
 *   default (sparse) auto-placement: a section that doesn't fit in the rest
 *   of the band starts a new band, and nothing back-fills.
 * - `bento`: each section is a block of one header track plus two tracks a
 *   row, packed by the dense first-fit tiles use, in Z order. A later
 *   section moves up into a hole that fits it.
 * - `mosaic`: every tile is packed by the same rule onto the whole board,
 *   section after section. A section's header is a label glued to the top
 *   of its first tile: the two pack as one block, as wide as the tile or
 *   `MOSAIC_LABEL_COLUMNS`, so a label never drifts into a gap of its own.
 *
 * All three are pure functions of the board: order is the source of truth,
 * and a change to one section never moves a section before it.
 */
export function deriveLayout(board: Board, breakpoint: Breakpoint, options: DeriveOptions = {}): DerivedLayout {
  const columns = BREAKPOINT_COLUMNS[breakpoint];
  const flow = options.flow ?? board.flow ?? 'sections';
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
  if (flow === 'sections') return { breakpoint, columns, flow, tracks: 0, sections };

  const grid = new Occupancy(columns);
  const headed = (s: PlacedSection): boolean => options.headers === 'always' || (s.title !== undefined && s.title !== '');
  const area = (x: number, track: number, w: number, tracks: number): TrackArea => ({ column: x, track, w, tracks });
  const placedOn = (it: PlacedItem, x: number, track: number): PlacedItem => ({ ...it, area: area(x, track, it.w, it.h * TRACKS_PER_ROW) });

  const out = sections.map((s): PlacedSection => {
    const head = headed(s) ? 1 : 0;
    if (flow === 'bento') {
      const rows = Math.max(s.rows, options.minRows ?? 0);
      const tracks = head + rows * TRACKS_PER_ROW;
      if (tracks === 0) return s; // untitled and empty: nothing to show
      const at = grid.add(s.span, tracks);
      const top = at.y + head;
      const items = s.items.map((it) => placedOn(it, at.x + it.x, top + it.y * TRACKS_PER_ROW));
      const box = { ...s, column: at.x, items, area: area(at.x, at.y, s.span, tracks) };
      return head ? { ...box, header: area(at.x, at.y, s.span, 1) } : box;
    }
    let header: TrackArea | undefined;
    const items = s.items.map((it, i) => {
      const tracks = it.h * TRACKS_PER_ROW;
      if (i > 0 || !head) {
        const at = grid.add(it.w, tracks);
        return placedOn(it, at.x, at.y);
      }
      const w = Math.min(Math.max(it.w, MOSAIC_LABEL_COLUMNS), columns);
      const at = grid.add(w, 1 + tracks);
      header = area(at.x, at.y, w, 1);
      return placedOn(it, at.x, at.y + 1);
    });
    if (head && s.items.length === 0) {
      const w = Math.min(MOSAIC_LABEL_COLUMNS, columns);
      const at = grid.add(w, 1);
      header = area(at.x, at.y, w, 1);
    }
    return header ? { ...s, items, header } : { ...s, items };
  });
  return { breakpoint, columns, flow, tracks: grid.height, sections: out };
}

/** Derive every breakpoint at once. */
export function deriveAll(board: Board, options: DeriveOptions = {}): { readonly [K in Breakpoint]: DerivedLayout } {
  const out = {} as { [K in Breakpoint]: DerivedLayout };
  for (const bp of BREAKPOINTS) out[bp] = deriveLayout(board, bp, options);
  return out;
}
