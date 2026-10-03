import { describe, expect, test } from 'bun:test';
import { SIZES, type SizeId } from '../src/index.ts';
import { Occupancy, chooseIndex, insertionCells, pack, readingCompare } from '../src/pack.ts';

const ext = (...sizes: SizeId[]) => sizes.map((s) => SIZES[s]);

describe('dense packing', () => {
  test('fills rows left to right, then wraps', () => {
    const cells = pack(ext('small', 'small', 'small', 'small', 'small', 'small', 'small'), 12);
    expect(cells).toEqual([
      { x: 0, y: 0 },
      { x: 2, y: 0 },
      { x: 4, y: 0 },
      { x: 6, y: 0 },
      { x: 8, y: 0 },
      { x: 10, y: 0 },
      { x: 0, y: 2 },
    ]);
  });

  test('a full row pushes the next item to the next row', () => {
    expect(pack(ext('strip-long', 'strip', 'glyph'), 12)).toEqual([
      { x: 0, y: 0 },
      { x: 8, y: 0 },
      { x: 0, y: 1 },
    ]);
  });

  test('back-fills earlier gaps (dense)', () => {
    // The hero can't fit beside the strip and small, so it drops to row 2;
    // the later strip and glyph back-fill row 0.
    expect(pack(ext('strip', 'small', 'hero', 'strip', 'glyph'), 12)).toEqual([
      { x: 0, y: 0 },
      { x: 4, y: 0 },
      { x: 0, y: 2 },
      { x: 6, y: 0 },
      { x: 10, y: 0 },
    ]);
  });

  test('rejects an item wider than the grid', () => {
    expect(() => new Occupancy(4).firstFit(8, 4)).toThrow(RangeError);
    expect(() => new Occupancy(0)).toThrow(RangeError);
  });

  test('insertion cells move forward monotonically with the index', () => {
    const others = ext('wide', 'small', 'glyph', 'hero', 'small', 'strip');
    const cells = insertionCells(others, 12, SIZES.small);
    for (let i = 1; i < cells.length; i++) {
      expect(readingCompare(cells[i - 1]!, cells[i]!)).toBeLessThanOrEqual(0);
    }
  });

  test('chooseIndex picks the cell nearest the target and clamps wild targets', () => {
    const others = ext('small', 'small', 'small');
    expect(chooseIndex(others, 12, SIZES.small, { x: 4, y: 0 })).toBe(2);
    expect(chooseIndex(others, 12, SIZES.small, { x: -50, y: -50 })).toBe(0);
    expect(chooseIndex(others, 12, SIZES.small, { x: Infinity, y: Infinity })).toBe(3);
    expect(chooseIndex(others, 12, SIZES.small, { x: 0, y: 0 }, (c) => c.x > 100)).toBeUndefined();
  });
});
