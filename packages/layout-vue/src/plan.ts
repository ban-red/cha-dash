/**
 * The render plan: how a derived layout maps onto one flat CSS grid.
 *
 * Every item and section header is a direct child of a single grid, so an
 * item that moves to another section keeps its DOM node (and the widget
 * mounted inside it). Sections still read as blocks: a section's internal
 * columns are exactly the board columns it covers (`span` = internal
 * columns), so an item's board column is `section.column + x`.
 *
 * Rows are tracks. In the `sections` flow each band (row of sections) gets
 * an optional header track, then its unit rows; bands are separated by a
 * spacer track so the gap between bands is the section gap rather than the
 * gutter. In `bento` and `mosaic` every track is half a unit row
 * (`--layout-track`: two tracks and a gap make a unit), and the engine's
 * track areas say where everything goes.
 */
import { TRACKS_PER_ROW, type Board, type DerivedLayout, type Item, type PlacedItem, type PlacedSection, type Section } from '@chadash/layout';

export interface PlannedSection {
  readonly placed: PlacedSection;
  readonly section: Section;
  /** Track index (0-based) of the band's header row, when the band has one. */
  readonly headerRow: number | undefined;
  /** Board column and width of the header (a mosaic label is narrower than the board). */
  readonly headerColumn: number;
  readonly headerSpan: number;
  /** Track index (0-based) of the section's first unit row. */
  readonly rowStart: number;
  /** Unit rows the section's area covers (at least 1 while editing). */
  readonly rows: number;
  /** Tracks the section's area covers: `rows`, or two a row in bento (0 in mosaic, which has no areas). */
  readonly tracks: number;
}

export interface PlannedItem {
  readonly id: string;
  readonly item: Item;
  readonly placed: PlacedItem;
  readonly section: PlannedSection;
  /** Board column (0-based) of the item's left edge. */
  readonly column: number;
  /** Track index (0-based) of the item's top row. */
  readonly row: number;
  /** Tracks the item covers: `h`, or `2h` in bento and mosaic. */
  readonly tracks: number;
}

export type PlanCell =
  | { readonly kind: 'header'; readonly key: string; readonly section: PlannedSection }
  | { readonly kind: 'item'; readonly key: string; readonly item: PlannedItem };

export interface BoardPlan {
  readonly columns: number;
  /** Value for `grid-template-rows`. */
  readonly rowsTemplate: string;
  readonly sections: readonly PlannedSection[];
  /** Items in DOM and focus order: section by section, each in its Z order. */
  readonly items: readonly PlannedItem[];
  /** Headers and items in reading order (a header precedes its section's items). */
  readonly cells: readonly PlanCell[];
  readonly byId: ReadonlyMap<string, PlannedItem>;
}

export interface PlanOptions {
  readonly editing: boolean;
  /** `auto`: a header row for bands with a titled section. `always`: every section gets a header. */
  readonly headers: 'auto' | 'always';
}

const UNIT = 'var(--layout-unit)';
const SPACER = 'var(--layout-band-gap)';
const HEADER = 'auto';
const TRACK = 'var(--layout-track)';

function hasTitle(section: PlacedSection): boolean {
  return section.title !== undefined && section.title !== '';
}

export function planBoard(board: Board, layout: DerivedLayout, options: PlanOptions): BoardPlan {
  const sourceById = new Map(board.sections.map((s) => [s.id, s]));
  const itemsById = new Map<string, Item>();
  for (const s of board.sections) for (const item of s.items) itemsById.set(item.id, item);
  if (layout.flow !== 'sections') return planTracks(layout, sourceById, itemsById, options);

  const bands: PlacedSection[][] = [];
  for (const s of layout.sections) (bands[s.band] ??= []).push(s);

  const tracks: string[] = [];
  const sections: PlannedSection[] = [];
  for (const band of bands) {
    if (band === undefined) continue;
    const showHeader = band.some((s) => options.headers === 'always' || hasTitle(s));
    const areaRows = (s: PlacedSection): number => (options.editing ? Math.max(s.rows, 1) : s.rows);
    const unitRows = Math.max(0, ...band.map(areaRows));
    if (!showHeader && unitRows === 0) {
      for (const s of band) {
        sections.push({
          placed: s,
          section: sourceById.get(s.id) as Section,
          headerRow: undefined,
          headerColumn: s.column,
          headerSpan: s.span,
          rowStart: tracks.length,
          rows: 0,
          tracks: 0,
        });
      }
      continue;
    }
    if (tracks.length > 0) tracks.push(SPACER);
    const headerRow = showHeader ? tracks.length : undefined;
    if (showHeader) tracks.push(HEADER);
    const rowStart = tracks.length;
    for (let i = 0; i < unitRows; i++) tracks.push(UNIT);
    for (const s of band) {
      const rows = areaRows(s);
      sections.push({ placed: s, section: sourceById.get(s.id) as Section, headerRow, headerColumn: s.column, headerSpan: s.span, rowStart, rows, tracks: rows });
    }
  }

  const items: PlannedItem[] = [];
  const cells: PlanCell[] = [];
  const byId = new Map<string, PlannedItem>();
  for (const section of sections) {
    const { placed } = section;
    if (section.headerRow !== undefined && (options.headers === 'always' || hasTitle(placed))) {
      cells.push({ kind: 'header', key: `section:${placed.id}`, section });
    }
    for (const p of placed.items) {
      const planned: PlannedItem = {
        id: p.id,
        item: itemsById.get(p.id) as Item,
        placed: p,
        section,
        column: placed.column + p.x,
        row: section.rowStart + p.y,
        tracks: p.h,
      };
      items.push(planned);
      byId.set(p.id, planned);
      cells.push({ kind: 'item', key: `item:${p.id}`, item: planned });
    }
  }

  return { columns: layout.columns, rowsTemplate: compressTracks(tracks), sections, items, cells, byId };
}

/** Bento and mosaic: every track is a half row, and the layout's track areas place everything. */
function planTracks(
  layout: DerivedLayout,
  sourceById: ReadonlyMap<string, Section>,
  itemsById: ReadonlyMap<string, Item>,
  options: PlanOptions,
): BoardPlan {
  const sections: PlannedSection[] = [];
  const items: PlannedItem[] = [];
  const cells: PlanCell[] = [];
  const byId = new Map<string, PlannedItem>();
  for (const s of layout.sections) {
    const { area, header } = s;
    const rows = options.editing ? Math.max(s.rows, 1) : s.rows;
    const top = area === undefined ? (s.items[0]?.area?.track ?? 0) : area.track + (header === undefined ? 0 : 1);
    const section: PlannedSection = {
      placed: s,
      section: sourceById.get(s.id) as Section,
      headerRow: header?.track,
      headerColumn: header?.column ?? s.column,
      headerSpan: header?.w ?? s.span,
      rowStart: top,
      rows: area === undefined ? 0 : rows,
      tracks: area === undefined ? 0 : rows * TRACKS_PER_ROW,
    };
    sections.push(section);
    if (header !== undefined) cells.push({ kind: 'header', key: `section:${s.id}`, section });
    for (const p of s.items) {
      const a = p.area;
      const planned: PlannedItem = {
        id: p.id,
        item: itemsById.get(p.id) as Item,
        placed: p,
        section,
        column: a?.column ?? s.column + p.x,
        row: a?.track ?? top + p.y * TRACKS_PER_ROW,
        tracks: a?.tracks ?? p.h * TRACKS_PER_ROW,
      };
      items.push(planned);
      byId.set(p.id, planned);
      cells.push({ kind: 'item', key: `item:${p.id}`, item: planned });
    }
  }
  const tracks = Array.from({ length: layout.tracks }, () => TRACK);
  return { columns: layout.columns, rowsTemplate: compressTracks(tracks), sections, items, cells, byId };
}

/** `a a a b` → `repeat(3, a) b`. */
function compressTracks(tracks: readonly string[]): string {
  if (tracks.length === 0) return 'none';
  const out: string[] = [];
  for (let i = 0; i < tracks.length; ) {
    let j = i;
    while (j < tracks.length && tracks[j] === tracks[i]) j++;
    const n = j - i;
    out.push(n > 1 ? `repeat(${n}, ${tracks[i]})` : (tracks[i] as string));
    i = j;
  }
  return out.join(' ');
}

/** CSS grid lines for a box: `{ gridColumn: '3 / span 4', gridRow: '2 / span 2' }`. */
export function gridArea(column: number, row: number, w: number, h: number): { gridColumn: string; gridRow: string } {
  return { gridColumn: `${column + 1} / span ${w}`, gridRow: `${row + 1} / span ${h}` };
}

// ---------------------------------------------------------------------------
// Hit testing
// ---------------------------------------------------------------------------

/** A section's unit area, in px relative to the grid. */
export interface AreaBox {
  readonly sectionId: string;
  readonly left: number;
  readonly top: number;
  readonly width: number;
  readonly height: number;
  /** Internal columns. */
  readonly span: number;
}

export interface CellTarget {
  readonly sectionId: string;
  readonly x: number;
  readonly y: number;
}

/** Cell pitch (unit plus gutter) of an area, in px. */
export function pitchOf(area: AreaBox, gap: number): number {
  return (area.width + gap) / Math.max(1, area.span);
}

function distanceToBox(area: AreaBox, x: number, y: number): number {
  const dx = Math.max(area.left - x, 0, x - (area.left + area.width));
  const dy = Math.max(area.top - y, 0, y - (area.top + area.height));
  return Math.hypot(dx, dy);
}

/**
 * The drop target for a widget whose top-left is at `topLeft` (px, grid
 * coordinates) and whose size is `size` (px). The section is the one nearest
 * the widget's centre; the cell is the nearest cell to its top-left. Targets
 * outside the grid are left for the engine to clamp.
 */
export function cellTarget(
  areas: readonly AreaBox[],
  topLeft: { readonly x: number; readonly y: number },
  size: { readonly w: number; readonly h: number },
  gap: number,
  allow: (sectionId: string) => boolean = () => true,
): CellTarget | undefined {
  const cx = topLeft.x + size.w / 2;
  const cy = topLeft.y + size.h / 2;
  let best: AreaBox | undefined;
  let bestDistance = Infinity;
  for (const area of areas) {
    if (!allow(area.sectionId)) continue;
    const d = distanceToBox(area, cx, cy);
    if (d < bestDistance) {
      best = area;
      bestDistance = d;
    }
  }
  if (best === undefined) return undefined;
  const pitch = pitchOf(best, gap);
  if (!(pitch > 0)) return { sectionId: best.sectionId, x: 0, y: 0 };
  return {
    sectionId: best.sectionId,
    x: Math.max(0, Math.round((topLeft.x - best.left) / pitch)),
    y: Math.max(0, Math.round((topLeft.y - best.top) / pitch)),
  };
}

// ---------------------------------------------------------------------------
// Focus navigation
// ---------------------------------------------------------------------------

/**
 * The item focus moves to from `id` with an arrow key (when nothing is
 * picked up). Left and right walk the DOM order, which is the order `nudge`
 * walks. Up and down go to the nearest item entirely above or below,
 * preferring the closest row and then the closest column.
 */
export function neighbourOf(plan: BoardPlan, id: string, direction: 'left' | 'right' | 'up' | 'down'): string | undefined {
  const { items } = plan;
  const index = items.findIndex((it) => it.id === id);
  const current = items[index];
  if (current === undefined) return undefined;
  if (direction === 'left') return items[index - 1]?.id;
  if (direction === 'right') return items[index + 1]?.id;
  const top = current.row;
  const bottom = current.row + current.tracks;
  const centre = current.column + current.placed.w / 2;
  let best: PlannedItem | undefined;
  let bestKey: readonly [number, number] | undefined;
  for (const candidate of items) {
    if (candidate === current) continue;
    const cTop = candidate.row;
    const cBottom = candidate.row + candidate.tracks;
    let gap: number;
    if (direction === 'up') {
      if (cBottom > top) continue;
      gap = top - cBottom;
    } else {
      if (cTop < bottom) continue;
      gap = cTop - bottom;
    }
    const key = [gap, Math.abs(candidate.column + candidate.placed.w / 2 - centre)] as const;
    if (bestKey === undefined || key[0] < bestKey[0] || (key[0] === bestKey[0] && key[1] < bestKey[1])) {
      best = candidate;
      bestKey = key;
    }
  }
  return best?.id;
}
