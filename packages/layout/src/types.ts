import type { SizeId } from './sizes.ts';

/** Named breakpoints. `desktop` is the canonical layout; the others are derived. */
export type Breakpoint = 'desktop' | 'tablet' | 'phone';

/** Breakpoints whose layout is derived from the canonical one. */
export type DerivedBreakpoint = Exclude<Breakpoint, 'desktop'>;

export const BREAKPOINTS: readonly Breakpoint[] = ['desktop', 'tablet', 'phone'];
export const DERIVED_BREAKPOINTS: readonly DerivedBreakpoint[] = ['tablet', 'phone'];

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

/** A board: sections in Z order. */
export interface Board {
  readonly sections: readonly Section[];
}

export interface BoardInput {
  readonly sections?: readonly SectionInput[];
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
}

/** A section as laid out at one breakpoint. */
export interface PlacedSection {
  readonly id: string;
  readonly title?: string;
  /** Board column where the section starts. */
  readonly column: number;
  /** Which band (row of sections) it sits in, counting from 0. */
  readonly band: number;
  /** Width in board columns, which is also its internal column count. */
  readonly span: number;
  /** Height in grid rows (0 when empty). */
  readonly rows: number;
  /** Items in Z order for this breakpoint (also the DOM and focus order). */
  readonly items: readonly PlacedItem[];
}

export interface DerivedLayout {
  readonly breakpoint: Breakpoint;
  readonly columns: number;
  readonly sections: readonly PlacedSection[];
}
