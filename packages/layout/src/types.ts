import type { SizeId } from './sizes.ts';

/** Named breakpoints. `desktop` is the canonical layout; the others are derived. */
export type Breakpoint = 'desktop' | 'tablet' | 'phone';

/** Breakpoints whose layout is derived from the canonical one. */
export type DerivedBreakpoint = Exclude<Breakpoint, 'desktop'>;

export const BREAKPOINTS: readonly Breakpoint[] = ['desktop', 'tablet', 'phone'];
export const DERIVED_BREAKPOINTS: readonly DerivedBreakpoint[] = ['tablet', 'phone'];

/**
 * How sections share the board.
 * - `sections`: sections flow in sparse bands, each band as tall as its
 *   tallest section (the default).
 * - `bento`: sections are blocks packed densely on the board, so a later
 *   section moves up into a hole that fits it.
 * - `mosaic`: no section blocks; every tile packs densely on the board in
 *   section order, with each section's header as a label before its tiles.
 *
 * Bento and mosaic place things on half-row tracks (`TRACKS_PER_ROW`): a
 * header takes one track, a tile `2h` tracks.
 */
export type Flow = 'sections' | 'bento' | 'mosaic';

export const FLOWS: readonly Flow[] = ['sections', 'bento', 'mosaic'];

/** Tracks per grid row in bento and mosaic: two half-rows. */
export const TRACKS_PER_ROW = 2;

/** Board columns per breakpoint (docs/plan/02-experience.md, "The grid"). */
export const BREAKPOINT_COLUMNS: { readonly [K in Breakpoint]: number } = {
  desktop: 12,
  tablet: 8,
  phone: 4,
};

/** Columns of the canonical layout. */
export const CANONICAL_COLUMNS = BREAKPOINT_COLUMNS.desktop;

/**
 * A widget on the board. `x`/`y` are its canonical (desktop) position inside
 * its section's grid. They are derived from the section's item order by the
 * dense Z-reflow and are never set directly.
 */
export interface Item {
  readonly id: string;
  /** Widget type, e.g. `"proxmox.node"`. Opaque to the engine. */
  readonly type: string;
  /** Canonical size. Always one of `supportedSizes`. */
  readonly size: SizeId;
  /** Sizes the widget declares it can render. Never empty. */
  readonly supportedSizes: readonly SizeId[];
  readonly x: number;
  readonly y: number;
}

/** What a caller provides to create an item: everything but the position. */
export interface ItemInput {
  readonly id: string;
  readonly type: string;
  readonly size: SizeId;
  readonly supportedSizes: readonly SizeId[];
}

/**
 * Per-breakpoint adjustments for one section. Everything is optional: an
 * absent override means "derive it".
 */
export interface SectionOverride {
  /** Section width in board columns at this breakpoint (1..breakpoint columns). */
  readonly span?: number;
  /**
   * Item order at this breakpoint. Items missing from it (added later) are
   * slotted in after their canonical predecessor; unknown ids are ignored.
   */
  readonly order?: readonly string[];
  /** Per-item size at this breakpoint. Ignored when it doesn't fit. */
  readonly sizes?: { readonly [itemId: string]: SizeId };
}

export type SectionOverrides = {
  readonly [K in DerivedBreakpoint]?: SectionOverride;
};

/**
 * A section: a titled block with its own grid. Its width on the canonical
 * 12-column board is `span`, and its internal grid has `span` columns.
 * `items` is in Z order (reading order), which is the source of truth for
 * positions.
 */
export interface Section {
  readonly id: string;
  readonly title?: string;
  /** Width in board columns at desktop, 1..12. Also the internal column count. */
  readonly span: number;
  readonly items: readonly Item[];
  readonly overrides?: SectionOverrides;
}

export interface SectionInput {
  readonly id: string;
  readonly title?: string;
  /** Defaults to 12 (full width). */
  readonly span?: number;
  readonly items?: readonly ItemInput[];
  readonly overrides?: SectionOverrides;
}

/** A board: sections in Z order, and how they share the board. */
export interface Board {
  readonly sections: readonly Section[];
  /** Absent means `sections`. */
  readonly flow?: Flow;
}

export interface BoardInput {
  readonly sections?: readonly SectionInput[];
  readonly flow?: Flow;
}

/**
 * A box on the board in bento and mosaic: board columns across, half-row
 * tracks down, both 0-based.
 */
export interface TrackArea {
  readonly column: number;
  readonly track: number;
  readonly w: number;
  readonly tracks: number;
}

/** An item as laid out at one breakpoint. */
export interface PlacedItem {
  readonly id: string;
  readonly type: string;
  readonly size: SizeId;
  readonly x: number;
  readonly y: number;
  readonly w: number;
  readonly h: number;
  /**
   * True when no supported size fit and the engine fell back to a size the
   * widget did not declare (e.g. a hero-only widget on a phone).
   */
  readonly forced: boolean;
  /** Bento and mosaic: where the item sits on the board, in tracks. */
  readonly area?: TrackArea;
}

/** A section as laid out at one breakpoint. */
export interface PlacedSection {
  readonly id: string;
  readonly title?: string;
  /** Board column where the section starts (bento: its packed column). */
  readonly column: number;
  /** Which band (row of sections) it sits in, counting from 0. Meaningful in the `sections` flow. */
  readonly band: number;
  /** Width in board columns, which is also its internal column count. */
  readonly span: number;
  /** Height in grid rows (0 when empty). */
  readonly rows: number;
  /** Items in Z order for this breakpoint (also the DOM and focus order). */
  readonly items: readonly PlacedItem[];
  /** Bento: the section's block, header included. Absent when it has nothing to show. */
  readonly area?: TrackArea;
  /** Bento and mosaic: the header's track (bento: the block's first; mosaic: a label). */
  readonly header?: TrackArea;
}

export interface DerivedLayout {
  readonly breakpoint: Breakpoint;
  readonly columns: number;
  readonly flow: Flow;
  /** Bento and mosaic: tracks the board uses. 0 in the `sections` flow. */
  readonly tracks: number;
  readonly sections: readonly PlacedSection[];
}
