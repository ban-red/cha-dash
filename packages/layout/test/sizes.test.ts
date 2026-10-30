import { describe, expect, test } from 'bun:test';
import { SIZES, SIZE_IDS, autoSize, closestFit, isSizeId, sizeFits, sizeLabel, snapSize, stepSize } from '../src/index.ts';

describe('size system', () => {
  test('matches the experience doc table', () => {
    const table = Object.fromEntries(SIZE_IDS.map((id) => [id, sizeLabel(id)]));
    expect(table).toEqual({
      glyph: '1×1',
      strip: '4×1',
      'strip-long': '8×1',
      small: '2×2',
      wide: '4×2',
      panel: '4×3',
      tall: '2×4',
      column: '3×4',
      large: '4×4',
      hero: '8×4',
    });
  });

  test('every size but glyph and column has an even width, so it tiles on 4 columns', () => {
    for (const id of SIZE_IDS) {
      if (id !== 'glyph' && id !== 'column') expect(SIZES[id].w % 2).toBe(0);
    }
    expect(SIZES.column.w).toBe(3);
  });

  test('isSizeId and sizeFits', () => {
    expect(isSizeId('hero')).toBe(true);
    expect(isSizeId('huge')).toBe(false);
    expect(isSizeId('toString')).toBe(false);
    expect(isSizeId(4)).toBe(false);
    expect(sizeFits('hero', 8)).toBe(true);
    expect(sizeFits('hero', 4)).toBe(false);
  });
});

describe('autoSize (narrow-grid fallback)', () => {
  test('a hero falls back to large on the phone', () => {
    expect(autoSize('hero', ['hero', 'large', 'wide'], 4)).toEqual({ size: 'large', forced: false });
  });

  test('a hero-only widget is forced to large (and flagged)', () => {
    expect(autoSize('hero', ['hero'], 4)).toEqual({ size: 'large', forced: true });
  });

  test('a long strip falls back to a strip', () => {
    expect(autoSize('strip-long', ['strip-long', 'strip'], 4)).toEqual({ size: 'strip', forced: false });
    expect(autoSize('strip-long', ['strip-long'], 4)).toEqual({ size: 'strip', forced: true });
  });

  test('prefers the same height, then the closest width', () => {
    expect(autoSize('large', ['large', 'tall', 'small'], 2)).toEqual({ size: 'tall', forced: false });
    expect(autoSize('wide', ['wide', 'tall', 'small'], 2)).toEqual({ size: 'small', forced: false });
    expect(autoSize('hero', ['hero', 'small', 'wide'], 4)).toEqual({ size: 'wide', forced: false });
  });

  test('keeps the size when it fits and falls back to a glyph on a 1-column grid', () => {
    expect(autoSize('small', ['small'], 4)).toEqual({ size: 'small', forced: false });
    expect(autoSize('large', ['large'], 1)).toEqual({ size: 'glyph', forced: true });
  });

  test('closestFit returns undefined when nothing fits', () => {
    expect(closestFit(['hero', 'large'], 'hero', 2)).toBeUndefined();
  });
});

describe('snapSize (corner resize handle)', () => {
  test('snaps to the nearest supported size', () => {
    expect(snapSize(['small', 'wide', 'large'], 4, 2, 12)).toBe('wide');
    expect(snapSize(['small', 'wide', 'large'], 5, 5, 12)).toBe('large');
    expect(snapSize(['small', 'wide', 'large'], 0, 0, 12)).toBe('small');
  });

  test('ties go to the smaller size', () => {
    // 3×2 is equally far from small (2×2) and wide (4×2).
    expect(snapSize(['small', 'wide', 'large'], 3, 2, 12)).toBe('small');
  });

  test('never offers a size that does not fit', () => {
    expect(snapSize(['hero', 'large'], 8, 4, 4)).toBe('large');
    expect(snapSize(['hero'], 8, 4, 4)).toBeUndefined();
  });
});

describe('stepSize (Shift+arrows)', () => {
  const sizes = ['small', 'wide', 'large', 'hero'] as const;

  test('steps to the next supported size in each direction', () => {
    expect(stepSize(sizes, 'small', 'wider', 12)).toBe('wide');
    expect(stepSize(sizes, 'wide', 'taller', 12)).toBe('large');
    expect(stepSize(sizes, 'large', 'wider', 12)).toBe('hero');
    expect(stepSize(sizes, 'wide', 'narrower', 12)).toBe('small');
    expect(stepSize(sizes, 'large', 'shorter', 12)).toBe('wide');
  });

  test('returns undefined at the end of the range or when the next size does not fit', () => {
    expect(stepSize(sizes, 'hero', 'wider', 12)).toBeUndefined();
    expect(stepSize(sizes, 'small', 'shorter', 12)).toBeUndefined();
    expect(stepSize(sizes, 'large', 'wider', 4)).toBeUndefined();
  });
});
