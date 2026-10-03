import { describe, expect, test } from 'bun:test';
import fc from 'fast-check';
import {
  BREAKPOINTS,
  LayoutError,
  SIZES,
  SIZE_IDS,
  checkInvariants,
  closestFit,
  commit,
  createHistory,
  deriveLayout,
  deserialize,
  fromDocument,
  move,
  redo,
  reflow,
  resize,
  sectionColumns,
  serialize,
  smartInsert,
  snapSize,
  stepSize,
  toDocument,
  undo,
  type Board,
  type History,
  type SizeId,
} from '../src/index.ts';
import { pack } from '../src/pack.ts';
import {
  RUNS,
  allItems,
  applyOp,
  arbBoard,
  arbBreakpoint,
  arbItemSpec,
  arbOp,
  arbSize,
  cssDense,
  geometryErrors,
  specToItem,
} from './helpers.ts';

const arbScenario = fc.tuple(arbBoard, fc.array(arbOp, { maxLength: 30 }));

function run(board: Board, ops: readonly Parameters<typeof applyOp>[1][]): Board[] {
  const states = [board];
  for (const op of ops) states.push(applyOp(states[states.length - 1] as Board, op));
  return states;
}

describe('properties', () => {
  test('random operation sequences keep every invariant', () => {
    fc.assert(
      fc.property(arbScenario, ([board, ops]) => {
        expect(checkInvariants(board)).toEqual([]);
        let current = board;
        for (const op of ops) {
          current = applyOp(current, op);
          const problems = checkInvariants(current);
          if (problems.length > 0) throw new Error(`after ${JSON.stringify(op)}:\n${problems.join('\n')}`);
        }
      }),
      { numRuns: RUNS.heavy },
    );
  });

  test('no overlaps and every item in bounds at every breakpoint (independent oracle)', () => {
    fc.assert(
      fc.property(arbScenario, ([board, ops]) => {
        const end = run(board, ops).at(-1) as Board;
        for (const bp of BREAKPOINTS) {
          const layout = deriveLayout(end, bp);
          expect(geometryErrors(layout)).toEqual([]);
          const placed = layout.sections.flatMap((s) => s.items.map((i) => i.id)).sort();
          expect(placed).toEqual(allItems(end).map((i) => i.id).sort());
        }
      }),
      { numRuns: RUNS.heavy },
    );
  });

  test('derivation is deterministic (repeat, deep copy, and after a serialization round trip)', () => {
    fc.assert(
      fc.property(arbScenario, arbBreakpoint, ([board, ops], bp) => {
        const end = run(board, ops).at(-1) as Board;
        const first = deriveLayout(end, bp);
        expect(deriveLayout(end, bp)).toEqual(first);
        expect(deriveLayout(structuredClone(end), bp)).toEqual(first);
        expect(deriveLayout(deserialize(serialize(end)), bp)).toEqual(first);
      }),
      { numRuns: RUNS.heavy },
    );
  });

  test('undo restores exactly, redo replays exactly', () => {
    fc.assert(
      fc.property(arbScenario, fc.integer({ min: 1, max: 40 }), ([board, ops], limit) => {
        let history: History<Board> = createHistory(board, { limit });
        const snapshots: string[] = [serialize(board)];
        const states: Board[] = [board];
        for (const op of ops) {
          const next = applyOp(history.present, op);
          const before = history;
          history = commit(history, next);
          if (history !== before) {
            states.push(next);
            snapshots.push(serialize(next));
          }
        }
        // Walk all the way back (bounded by the cap) and check each state.
        const reachable = Math.min(states.length - 1, limit);
        for (let k = 1; k <= reachable; k++) {
          history = undo(history);
          const expected = states[states.length - 1 - k] as Board;
          expect(history.present).toBe(expected);
          expect(serialize(history.present)).toBe(snapshots[snapshots.length - 1 - k] as string);
        }
        expect(undo(history)).toBe(history);
        for (let k = reachable - 1; k >= 0; k--) {
          history = redo(history);
          expect(history.present).toBe(states[states.length - 1 - k] as Board);
        }
        expect(redo(history)).toBe(history);
      }),
      { numRuns: RUNS.heavy },
    );
  });

  test('resize only ever lands on supported sizes', () => {
    fc.assert(
      fc.property(
        arbBoard,
        fc.array(fc.tuple(fc.nat(), arbSize, arbBreakpoint), { maxLength: 25 }),
        (board, attempts) => {
          let current = board;
          for (const [pick, size, bp] of attempts) {
            const items = allItems(current);
            const target = items[pick % Math.max(1, items.length)];
            if (target === undefined) break;
            const supported = target.supportedSizes.includes(size);
            try {
              current = resize(current, target.id, size, { breakpoint: bp });
              expect(supported).toBe(true);
            } catch (error) {
              expect(error).toBeInstanceOf(LayoutError);
              const code = (error as LayoutError).code;
              expect(code).toBe(supported ? 'NO_FIT' : 'UNSUPPORTED_SIZE');
            }
            for (const sec of current.sections) {
              for (const it of sec.items) expect(it.supportedSizes).toContain(it.size);
              for (const o of Object.values(sec.overrides ?? {})) {
                for (const [id, s] of Object.entries(o.sizes ?? {})) {
                  expect(sec.items.find((i) => i.id === id)?.supportedSizes).toContain(s);
                }
              }
            }
            for (const b of BREAKPOINTS) {
              for (const ps of deriveLayout(current, b).sections) {
                for (const p of ps.items) {
                  const it = items.find((i) => i.id === p.id) ?? allItems(current).find((i) => i.id === p.id);
                  if (it === undefined) continue;
                  // A size outside supportedSizes only appears when nothing supported fits.
                  if (!it.supportedSizes.includes(p.size)) {
                    expect(p.forced).toBe(true);
                    expect(closestFit(it.supportedSizes, it.size, ps.span)).toBeUndefined();
                  }
                }
              }
            }
          }
        },
      ),
      { numRuns: RUNS.heavy },
    );
  });

  test('snapSize and stepSize only return supported sizes that fit', () => {
    fc.assert(
      fc.property(
        fc.uniqueArray(arbSize, { minLength: 1, maxLength: 8 }),
        fc.integer({ min: -3, max: 20 }),
        fc.integer({ min: -3, max: 20 }),
        fc.integer({ min: 1, max: 12 }),
        fc.constantFrom('wider', 'narrower', 'taller', 'shorter' as const),
        (supported, w, h, columns, step) => {
          const snapped = snapSize(supported, w, h, columns);
          if (snapped === undefined) {
            expect(supported.every((s) => SIZES[s].w > columns)).toBe(true);
          } else {
            expect(supported).toContain(snapped);
            expect(SIZES[snapped].w).toBeLessThanOrEqual(columns);
          }
          for (const current of supported) {
            const next = stepSize(supported, current, step, columns);
            if (next !== undefined) {
              expect(supported).toContain(next);
              expect(SIZES[next].w).toBeLessThanOrEqual(columns);
            }
          }
        },
      ),
      { numRuns: RUNS.standard },
    );
  });

  test('serialization round-trips exactly', () => {
    fc.assert(
      fc.property(arbScenario, ([board, ops]) => {
        const end = run(board, ops).at(-1) as Board;
        const json = serialize(end);
        const back = deserialize(json);
        expect(back).toEqual(end);
        expect(serialize(back)).toBe(json);
        expect(fromDocument(JSON.parse(JSON.stringify(toDocument(end))))).toEqual(end);
      }),
      { numRuns: RUNS.heavy },
    );
  });

  test('packing matches a reference CSS grid "row dense" implementation', () => {
    fc.assert(
      fc.property(fc.array(arbSize, { maxLength: 30 }), fc.integer({ min: 1, max: 12 }), (sizes, columns) => {
        const extents = sizes.map((s) => SIZES[s]).filter((e) => e.w <= columns);
        expect(pack(extents, columns)).toEqual(cssDense(extents, columns));
      }),
      { numRuns: RUNS.standard },
    );
  });

  test('prefix stability: a canonical move or resize never moves items earlier in Z order', () => {
    fc.assert(
      fc.property(
        arbBoard,
        fc.nat(),
        fc.oneof(
          fc.record({ kind: fc.constant('move' as const), x: fc.integer({ min: -1, max: 13 }), y: fc.integer({ min: -1, max: 20 }) }),
          fc.record({ kind: fc.constant('resize' as const), size: arbSize }),
        ),
        (board, pick, op) => {
          const items = allItems(board);
          const target = items[pick % Math.max(1, items.length)];
          if (target === undefined) return;
          const sec = board.sections.find((s) => s.items.some((i) => i.id === target.id));
          if (sec === undefined) return;
          let next: Board;
          try {
            next = op.kind === 'move'
              ? move(board, target.id, { x: op.x, y: op.y })
              : resize(board, target.id, op.size);
          } catch {
            return;
          }
          const after = next.sections.find((s) => s.id === sec.id);
          if (after === undefined) return;
          const oldIndex = sec.items.findIndex((i) => i.id === target.id);
          const newIndex = after.items.findIndex((i) => i.id === target.id);
          const stable = Math.min(oldIndex, newIndex);
          for (let i = 0; i < stable; i++) {
            expect(after.items[i]).toEqual(sec.items[i] as (typeof sec.items)[number]);
          }
          // Untouched items keep their relative order.
          const others = (list: typeof sec.items) => list.filter((i) => i.id !== target.id).map((i) => i.id);
          expect(others(after.items)).toEqual(others(sec.items));
        },
      ),
      { numRuns: RUNS.standard },
    );
  });

  test('moving an item to where it already is changes nothing', () => {
    fc.assert(
      fc.property(arbBoard, fc.nat(), arbBreakpoint, (board, pick, bp) => {
        const items = allItems(board);
        const target = items[pick % Math.max(1, items.length)];
        if (target === undefined) return;
        const layout = deriveLayout(board, bp);
        const placed = layout.sections.flatMap((s) => s.items).find((p) => p.id === target.id);
        if (placed === undefined) return;
        expect(move(board, target.id, { x: placed.x, y: placed.y }, { breakpoint: bp })).toBe(board);
      }),
      { numRuns: RUNS.standard },
    );
  });

  test('smart insert without hints never displaces existing items', () => {
    fc.assert(
      fc.property(arbBoard, arbItemSpec, (board, spec) => {
        if (board.sections.length === 0) return;
        const span = Math.max(...board.sections.map((s) => s.span));
        let next: Board;
        try {
          next = smartInsert(board, specToItem('fresh', spec, span));
        } catch (error) {
          expect((error as LayoutError).code).toBe('NO_FIT');
          return;
        }
        const before = new Map(allItems(board).map((i) => [i.id, `${i.x},${i.y}`]));
        for (const it of allItems(next)) {
          if (it.id !== 'fresh') expect(`${it.x},${it.y}`).toBe(before.get(it.id) as string);
        }
      }),
      { numRuns: RUNS.standard },
    );
  });

  test('reflow is idempotent and repairs hand-scrambled positions', () => {
    fc.assert(
      fc.property(arbBoard, fc.array(fc.nat({ max: 20 }), { maxLength: 40 }), (board, noise) => {
        expect(reflow(board)).toBe(board);
        let n = 0;
        const scrambled: Board = {
          sections: board.sections.map((s) => ({
            ...s,
            items: s.items.map((i) => ({ ...i, x: noise[n++ % Math.max(1, noise.length)] ?? 0, y: noise[n++ % Math.max(1, noise.length)] ?? 0 })),
          })),
        };
        const repaired = reflow(scrambled);
        expect(repaired).toEqual(board);
        expect(reflow(repaired)).toBe(repaired);
      }),
      { numRuns: RUNS.standard },
    );
  });

  test('derived sections that still fit keep their internal structure exactly', () => {
    fc.assert(
      fc.property(arbBoard, arbBreakpoint, (board, bp) => {
        const layout = deriveLayout(board, bp);
        board.sections.forEach((sec, i) => {
          const o = bp === 'desktop' ? undefined : sec.overrides?.[bp];
          const untouched = o === undefined || (o.order === undefined && o.sizes === undefined && o.span === undefined);
          if (!untouched || sectionColumns(sec, bp) !== sec.span) return;
          const placed = layout.sections[i];
          expect(placed?.items.map((p) => `${p.id}@${p.x},${p.y}:${p.size}`)).toEqual(
            sec.items.map((it) => `${it.id}@${it.x},${it.y}:${it.size}`),
          );
        });
      }),
      { numRuns: RUNS.standard },
    );
  });

  test('every size in the system is reachable by snapping to its own extent', () => {
    for (const size of SIZE_IDS) {
      expect(snapSize(SIZE_IDS as readonly SizeId[], SIZES[size].w, SIZES[size].h, 12)).toBe(size);
    }
  });
});
