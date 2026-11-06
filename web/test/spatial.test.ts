/** TV mode's arrow-key focus (lib/spatial.ts): the nearest box that way, straight ahead first. */
import { describe, expect, it } from 'vitest';
import { nextBox, type Box } from '../src/lib/spatial';

// A 3 × 2 grid of 100-px tiles, then a wide one under it.
const grid: Box[] = [
  { x: 0, y: 0, w: 100, h: 100 },
  { x: 110, y: 0, w: 100, h: 100 },
  { x: 220, y: 0, w: 100, h: 100 },
  { x: 0, y: 110, w: 100, h: 100 },
  { x: 110, y: 110, w: 100, h: 100 },
  { x: 220, y: 110, w: 100, h: 100 },
  { x: 0, y: 220, w: 320, h: 100 },
];

describe('nextBox', () => {
  it('moves one tile in each direction', () => {
    expect(nextBox(grid, 0, 'right')).toBe(1);
    expect(nextBox(grid, 1, 'right')).toBe(2);
    expect(nextBox(grid, 1, 'down')).toBe(4);
    expect(nextBox(grid, 4, 'up')).toBe(1);
    expect(nextBox(grid, 5, 'left')).toBe(4);
  });
  it('stops at the edge', () => {
    expect(nextBox(grid, 2, 'right')).toBe(-1);
    expect(nextBox(grid, 0, 'up')).toBe(-1);
    expect(nextBox(grid, 6, 'down')).toBe(-1);
  });
  it('reaches a wide tile from any tile above it, and leaves it to the nearest', () => {
    expect(nextBox(grid, 3, 'down')).toBe(6);
    expect(nextBox(grid, 5, 'down')).toBe(6);
    expect(nextBox(grid, 6, 'up')).toBe(4);
  });
  it('prefers straight ahead to nearer but off to the side', () => {
    const boxes: Box[] = [
      { x: 0, y: 100, w: 50, h: 50 },
      { x: 60, y: 0, w: 50, h: 50 }, // near, but up and to the side
      { x: 200, y: 100, w: 50, h: 50 }, // further, straight ahead
    ];
    expect(nextBox(boxes, 0, 'right')).toBe(2);
  });
  it('starts at the first box when nothing has focus', () => {
    expect(nextBox(grid, -1, 'down')).toBe(0);
    expect(nextBox([], -1, 'down')).toBe(-1);
  });
});
