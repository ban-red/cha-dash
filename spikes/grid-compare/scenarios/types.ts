/**
 * Engine-neutral scenario data. Nothing here imports an engine: the same
 * objects are replayed against @chadash/layout and gridstack by the adapters.
 */

/** The size system from 02 · Experience → Widget sizes, in grid units. */
export type SizeName = 'glyph' | 'strip' | 'strip-long' | 'small' | 'wide' | 'tall' | 'large' | 'hero';

export const SIZE_UNITS: { readonly [K in SizeName]: { readonly w: number; readonly h: number } } = {
  glyph: { w: 1, h: 1 },
  strip: { w: 4, h: 1 },
  'strip-long': { w: 8, h: 1 },
  small: { w: 2, h: 2 },
  wide: { w: 4, h: 2 },
  tall: { w: 2, h: 4 },
  large: { w: 4, h: 4 },
  hero: { w: 8, h: 4 },
};

export interface WidgetData {
  readonly id: string;
  readonly type: string;
  readonly size: SizeName;
  readonly supportedSizes: readonly SizeName[];
}

export interface SectionData {
  readonly id: string;
  readonly title: string;
  /** Width in board columns at 12 columns, and the section's internal column count. */
  readonly span: number;
  /** Reading order. Positions are not part of the data: both engines pack in this order. */
  readonly items: readonly WidgetData[];
}

export interface BoardData {
  readonly sections: readonly SectionData[];
}

export type Columns = 12 | 8 | 4;

/** A target cell inside a section, at the current column count. */
export interface Target {
  readonly section: string;
  readonly x: number;
  readonly y: number;
}

export type Step =
  | { readonly op: 'move'; readonly item: string; readonly to: Target }
  | { readonly op: 'resize'; readonly item: string; readonly size: SizeName }
  | { readonly op: 'insert'; readonly item: WidgetData; readonly near?: Target }
  | { readonly op: 'columns'; readonly columns: Columns }
  | { readonly op: 'undo'; readonly times: number }
  | { readonly op: 'roundTrip' };

export interface Scenario {
  readonly id: string;
  readonly title: string;
  /** What the product needs to see, in a sentence. */
  readonly expect: string;
  readonly steps: readonly Step[];
}
