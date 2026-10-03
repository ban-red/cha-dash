/**
 * The widget size system from docs/plan/02-experience.md ("Widget sizes").
 * Sizes are measured in grid units (U): `w` columns by `h` rows.
 */

/** Every size, in the order the experience doc lists them. */
export const SIZE_IDS = [
  'glyph',
  'strip',
  'strip-long',
  'small',
  'wide',
  'tall',
  'large',
  'hero',
] as const;

export type SizeId = (typeof SIZE_IDS)[number];

export interface SizeSpec {
  readonly id: SizeId;
  /** Human name from the experience doc. */
  readonly name: string;
  /** Width in columns. */
  readonly w: number;
  /** Height in rows. */
  readonly h: number;
}

export const SIZES: { readonly [K in SizeId]: SizeSpec } = {
  glyph: { id: 'glyph', name: 'Glyph', w: 1, h: 1 },
  strip: { id: 'strip', name: 'Strip', w: 4, h: 1 },
  'strip-long': { id: 'strip-long', name: 'Strip (long)', w: 8, h: 1 },
  small: { id: 'small', name: 'Small', w: 2, h: 2 },
  wide: { id: 'wide', name: 'Wide', w: 4, h: 2 },
  tall: { id: 'tall', name: 'Tall', w: 2, h: 4 },
  large: { id: 'large', name: 'Large', w: 4, h: 4 },
  hero: { id: 'hero', name: 'Hero', w: 8, h: 4 },
};

const SIZE_INDEX: { readonly [K in SizeId]: number } = Object.fromEntries(
  SIZE_IDS.map((id, i) => [id, i]),
) as { [K in SizeId]: number };

export function isSizeId(value: unknown): value is SizeId {
  return typeof value === 'string' && Object.hasOwn(SIZE_INDEX, value);
}

/** True when a widget of this size fits in a grid `columns` wide. */
export function sizeFits(size: SizeId, columns: number): boolean {
  return SIZES[size].w <= columns;
}

/** The "4×2" label the resize handle shows while snapping. */
export function sizeLabel(size: SizeId): string {
  const { w, h } = SIZES[size];
  return `${w}×${h}`;
}

/** Lexicographic comparison of numeric keys. */
function lessThan(a: readonly number[], b: readonly number[]): boolean {
  for (let i = 0; i < a.length; i++) {
    const x = a[i] as number;
    const y = b[i] as number;
    if (x !== y) return x < y;
  }
  return false;
}

function pick(
  candidates: Iterable<SizeId>,
  columns: number,
  key: (spec: SizeSpec) => readonly number[] | undefined,
): SizeId | undefined {
  let best: SizeId | undefined;
  let bestKey: readonly number[] | undefined;
  for (const id of candidates) {
    const spec = SIZES[id];
    if (spec.w > columns) continue;
    const k = key(spec);
    if (k === undefined) continue;
    const full = [...k, SIZE_INDEX[id]];
    if (bestKey === undefined || lessThan(full, bestKey)) {
      best = id;
      bestKey = full;
    }
  }
  return best;
}

/**
 * The candidate closest in shape to `preferred` that fits in `columns`:
 * same height first (a hero falls back to large, a long strip to a strip),
 * then the closest width. Returns undefined when nothing fits.
 */
export function closestFit(
  candidates: Iterable<SizeId>,
  preferred: SizeId,
  columns: number,
): SizeId | undefined {
  const p = SIZES[preferred];
  return pick(candidates, columns, (s) => [Math.abs(s.h - p.h), Math.abs(s.w - p.w)]);
}

/**
 * The size an item shows in a grid `columns` wide when nobody chose one:
 * its own size if it fits, else the closest supported size that fits, else
 * the closest size from the whole system (flagged `forced`, because the
 * widget did not declare it). A glyph fits any grid, so this always answers.
 */
export function autoSize(
  size: SizeId,
  supportedSizes: readonly SizeId[],
  columns: number,
): { readonly size: SizeId; readonly forced: boolean } {
  if (sizeFits(size, columns)) return { size, forced: false };
  const supported = closestFit(supportedSizes, size, columns);
  if (supported !== undefined) return { size: supported, forced: false };
  const system = closestFit(SIZE_IDS, size, columns) ?? 'glyph';
  return { size: system, forced: true };
}

/**
 * Snap a free-form w×h (for example, from dragging the corner resize handle)
 * to the nearest supported size that fits. Returns undefined when no
 * supported size fits in `columns`.
 */
export function snapSize(
  supportedSizes: readonly SizeId[],
  w: number,
  h: number,
  columns: number,
): SizeId | undefined {
  return pick(supportedSizes, columns, (s) => [(s.w - w) ** 2 + (s.h - h) ** 2, s.w * s.h]);
}

export type SizeStep = 'wider' | 'narrower' | 'taller' | 'shorter';

/**
 * The next supported size in a direction (keyboard Shift+arrows). Returns
 * undefined when there is no bigger/smaller supported size that fits.
 */
export function stepSize(
  supportedSizes: readonly SizeId[],
  current: SizeId,
  step: SizeStep,
  columns: number,
): SizeId | undefined {
  const c = SIZES[current];
  return pick(supportedSizes, columns, (s) => {
    switch (step) {
      case 'wider':
        return s.w > c.w ? [s.w - c.w, Math.abs(s.h - c.h)] : undefined;
      case 'narrower':
        return s.w < c.w ? [c.w - s.w, Math.abs(s.h - c.h)] : undefined;
      case 'taller':
        return s.h > c.h ? [s.h - c.h, Math.abs(s.w - c.w)] : undefined;
      case 'shorter':
        return s.h < c.h ? [c.h - s.h, Math.abs(s.w - c.w)] : undefined;
    }
  });
}
