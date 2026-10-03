import { describe, expect, test } from 'bun:test';
import {
  SIZE_IDS,
  createBoard,
  deriveAll,
  move,
  reflow,
  smartInsert,
  type Board,
  type ItemInput,
  type SizeId,
} from '../src/index.ts';

/**
 * Generous sanity budgets, not benchmarks: a 60 fps frame is ~16.7 ms and
 * these operations must leave most of it to rendering. Set LAYOUT_BENCH=1 to
 * print the measured medians (useful for the ADR-0013 comparison).
 */
const FRAME_MS = 16.7;
const BUDGET_MS = FRAME_MS / 4;

/** Deterministic mixed sizes (no RNG, so runs are comparable). */
function items(count: number, prefix: string): ItemInput[] {
  const pool: SizeId[] = ['small', 'small', 'wide', 'glyph', 'strip', 'tall', 'small', 'large', 'wide', 'hero'];
  return Array.from({ length: count }, (_, i) => {
    const size = pool[(i * 7) % pool.length] as SizeId;
    return { id: `${prefix}${i}`, type: 'bench', size, supportedSizes: [...SIZE_IDS] };
  });
}

function median(fn: () => unknown, iterations: number): number {
  for (let i = 0; i < 20; i++) fn(); // warm up
  const samples: number[] = [];
  for (let i = 0; i < iterations; i++) {
    const start = performance.now();
    fn();
    samples.push(performance.now() - start);
  }
  samples.sort((a, b) => a - b);
  return samples[Math.floor(samples.length / 2)] as number;
}

function report(name: string, ms: number): void {
  if (process.env['LAYOUT_BENCH'] === '1') console.log(`${name.padEnd(44)} ${ms.toFixed(3)} ms`);
}

const oneSection: Board = createBoard({ sections: [{ id: 'all', items: items(60, 'i') }] });
const fiveSections: Board = createBoard({
  sections: [12, 8, 4, 6, 6].map((span, s) => ({
    id: `s${s}`,
    span,
    items: items(12, `s${s}i`).map((it) => ({ ...it, size: span < 8 && (it.size === 'hero') ? 'large' : it.size })),
  })),
});

function scrambled(board: Board): Board {
  return { sections: board.sections.map((s) => ({ ...s, items: s.items.map((i) => ({ ...i, x: 0, y: 0 })) })) };
}

describe('performance sanity (60 items)', () => {
  test('full reflow of 60 items', () => {
    const input = scrambled(oneSection);
    const ms = median(() => reflow(input), 200);
    report('reflow 60 items, one section', ms);
    expect(ms).toBeLessThan(BUDGET_MS);
  });

  test('derive all three breakpoints', () => {
    const ms1 = median(() => deriveAll(oneSection), 200);
    const ms5 = median(() => deriveAll(fiveSections), 200);
    report('deriveAll, 60 items in one section', ms1);
    report('deriveAll, 60 items in five sections', ms5);
    expect(ms1).toBeLessThan(BUDGET_MS);
    expect(ms5).toBeLessThan(BUDGET_MS);
  });

  test('a drag preview step (move) and a phone-override move', () => {
    let x = 0;
    const desktop = median(() => move(oneSection, 'i30', { x: (x = (x + 3) % 12), y: (x * 2) % 20 }), 300);
    const phone = median(() => move(oneSection, 'i30', { x: (x = (x + 1) % 4), y: x * 3 }, { breakpoint: 'phone' }), 300);
    report('move (drag preview), 60 items', desktop);
    report('move at phone (override), 60 items', phone);
    expect(desktop).toBeLessThan(BUDGET_MS);
    expect(phone).toBeLessThan(BUDGET_MS);
  });

  test('smart insert with a viewport hint (worst case: scores every slot)', () => {
    let n = 0;
    const ms = median(
      () => smartInsert(oneSection, { id: `new${n++}`, type: 'bench', size: 'small', supportedSizes: ['small'] }, { near: { sectionId: 'all', y: 20 } }),
      200,
    );
    report('smartInsert near, 60 items', ms);
    expect(ms).toBeLessThan(BUDGET_MS);
  });
});
