/**
 * Properties over many random sections, engine level (no DOM): supported
 * sizes, agreement with CSS `grid-auto-flow: row dense`, and whether the drag
 * preview is the drop.
 */
import { describe, expect, test } from 'bun:test';
import type { GridStackNode } from 'gridstack';
import { GridStackEngine } from 'gridstack/dist/gridstack-engine.js';
import { LayoutError, createBoard, move, resize, snapSize, stepSize, type SizeId } from '@chadash/layout';
import { GridstackAdapter, constraints } from '../adapters/gridstack.ts';
import { DOGFOOD, SIZE_UNITS, type SizeName } from '../scenarios/index.ts';

function rng(seed: number) {
  return () => ((seed = (seed * 1664525 + 1013904223) >>> 0) / 2 ** 32);
}

const SIZES: SizeName[] = ['glyph', 'strip', 'small', 'wide', 'tall', 'large', 'hero'];
const RUNS = 2000;

/** Independent reference: CSS grid `row dense` placement of boxes in order. */
function cssDense(boxes: readonly { w: number; h: number }[], columns: number): { x: number; y: number }[] {
  const taken = new Set<number>();
  const free = (x: number, y: number, w: number, h: number) => {
    for (let j = y; j < y + h; j++) for (let i = x; i < x + w; i++) if (taken.has(j * columns + i)) return false;
    return true;
  };
  return boxes.map(({ w, h }) => {
    for (let y = 0; ; y++) {
      for (let x = 0; x + w <= columns; x++) {
        if (free(x, y, w, h)) {
          for (let j = y; j < y + h; j++) for (let i = x; i < x + w; i++) taken.add(j * columns + i);
          return { x, y };
        }
      }
    }
  });
}

function randomSection(r: () => number) {
  const n = 4 + Math.floor(r() * 12);
  return Array.from({ length: n }, (_, i) => ({ id: `i${i}`, size: SIZES[Math.floor(r() * SIZES.length)]! }));
}

describe('supported sizes', () => {
  test('gridstack can only box them in: min/max lets the 15 dogfood widgets reach 93 sizes, only 35 of them declared', () => {
    let reachable = 0;
    let supported = 0;
    for (const w of DOGFOOD.sections.flatMap((s) => s.items)) {
      const c = constraints(w.supportedSizes);
      reachable += (c.maxW - c.minW + 1) * (c.maxH - c.minH + 1);
      supported += w.supportedSizes.length;
    }
    expect({ reachable, supported }).toEqual({ reachable: 93, supported: 35 });
  });

  test('gridstack accepts 3×3 and 2×4 for a widget that declares Small, Wide and Large', () => {
    const g = new GridstackAdapter({ mode: 'compact', sectionMode: 'list' });
    g.load(DOGFOOD);
    const containers = () => g.snapshot().sections.flatMap((s) => s.items).find((i) => i.id === 'containers')!;
    // what a corner-handle drag ends with: gridstack clamps to min/max, nothing more
    const el = document.querySelector('[gs-id="containers"]') as HTMLElement & { gridstackNode?: GridStackNode };
    el.gridstackNode!.grid!.update(el, { w: 3, h: 3 });
    expect([containers().w, containers().h]).toEqual([3, 3]);
    el.gridstackNode!.grid!.update(el, { w: 2, h: 4 });
    expect([containers().w, containers().h]).toEqual([2, 4]);
    g.dispose();
  });

  test('ours refuses an undeclared size, and the handle and arrow keys only reach declared ones', () => {
    const board = createBoard({ sections: [{ id: 's', span: 8, items: [{ id: 'c', type: 'containers', size: 'small', supportedSizes: ['small', 'wide', 'large'] }] }] });
    expect(() => resize(board, 'c', 'tall')).toThrow(LayoutError);
    expect(snapSize(['small', 'wide', 'large'], 3, 3, 8)).toBe('small'); // ties go to the smaller size
    expect(snapSize(['small', 'wide', 'large'], 4, 3, 8)).toBe('wide');
    expect(stepSize(['small', 'wide', 'large'], 'small', 'taller', 8)).toBe('large'); // skips the undeclared 2×4
    expect(stepSize(['small', 'wide', 'large'], 'wide', 'taller', 8)).toBe('large');
  });
});

describe(`agreement with CSS row-dense packing (${RUNS} random 8-column sections)`, () => {
  test('ours: every section', () => {
    const r = rng(42);
    for (let t = 0; t < RUNS; t++) {
      const items = randomSection(r);
      const board = createBoard({ sections: [{ id: 's', span: 8, items: items.map((i) => ({ ...i, type: 'x', supportedSizes: [i.size as SizeId] })) }] });
      const want = cssDense(items.map((i) => SIZE_UNITS[i.size]), 8);
      expect(board.sections[0]!.items.map(({ x, y }) => ({ x, y }))).toEqual(want);
    }
  });

  test('gridstack compact: all but 0.8% (it re-sorts by position after placing, then packs again)', () => {
    const r = rng(42);
    let differ = 0;
    for (let t = 0; t < RUNS; t++) {
      const items = randomSection(r);
      const e = new GridStackEngine({ column: 8, mode: 'compact' });
      e.batchUpdate();
      for (const i of items) e.addNode({ id: i.id, ...SIZE_UNITS[i.size], autoPosition: true } as GridStackNode);
      e.batchUpdate(false);
      const got = items.map((i) => e.nodes.find((n) => n.id === i.id)!).map((n) => ({ x: n.x, y: n.y }));
      if (JSON.stringify(got) !== JSON.stringify(cssDense(items.map((i) => SIZE_UNITS[i.size]), 8))) differ++;
    }
    expect(differ).toBe(16); // 0.8%, seeded
  });
});

describe(`is the drag preview the drop? (${RUNS} random drags)`, () => {
  test('gridstack compact previews with the cheaper list flow and compacts on release: 42% of drops jump after the pointer lets go', () => {
    const r = rng(7);
    let jumps = 0;
    for (let t = 0; t < RUNS; t++) {
      const e = new GridStackEngine({ column: 8, mode: 'compact' });
      for (const i of randomSection(r)) e.addNode({ id: i.id, ...SIZE_UNITS[i.size], autoPosition: true } as GridStackNode);
      const node = e.nodes[Math.floor(r() * e.nodes.length)]!;
      const target = { x: Math.floor(r() * 8), y: Math.floor(r() * e.getRow()) };
      e.beginUpdate(node); // what GridStack does on drag start
      e.moveNode(node, target); // each pointer move
      const preview = e.nodes.map((n) => `${n.id}:${n.x},${n.y}`).sort().join(' ');
      e.endUpdate(); // drop
      if (preview !== e.nodes.map((n) => `${n.id}:${n.x},${n.y}`).sort().join(' ')) jumps++;
    }
    expect(jumps).toBe(832); // 42%, seeded
  });

  test('ours: the preview is move(origin, id, target) and so is the drop, so a drop at the last preview cell changes nothing', () => {
    const r = rng(7);
    for (let t = 0; t < 200; t++) {
      const items = randomSection(r);
      const origin = createBoard({ sections: [{ id: 's', span: 8, items: items.map((i) => ({ ...i, type: 'x', supportedSizes: [i.size as SizeId] })) }] });
      const id = items[Math.floor(r() * items.length)]!.id;
      const target = { x: Math.floor(r() * 8), y: Math.floor(r() * 12) };
      expect(move(origin, id, target)).toEqual(move(origin, id, target));
    }
  });
});
