/**
 * Operation timings on a 60-widget board (4 sections × 15), engine level:
 * our pure functions against gridstack's GridStackEngine (one per section,
 * plus one for the sections), without any DOM. Run: `bun run bench`.
 *
 * gridstack's engine mutates in place, so each timed unit is a pair that
 * returns to the starting state (there and back), for both engines alike.
 */
import type { ColumnOptions, GridStackNode } from 'gridstack';
import { GridStackEngine } from 'gridstack/dist/gridstack-engine.js';
import {
  commit,
  createBoard,
  createHistory,
  deriveLayout,
  deserialize,
  move,
  remove,
  resize,
  serialize,
  smartInsert,
  undo,
  type Board,
  type BoardInput,
} from '@chadash/layout';
import { SIZE_UNITS, sixtyWidgetBoard, type BoardData } from '../scenarios/index.ts';

const SAMPLES = 25;
const PER_SAMPLE = 200;

function time(fn: () => void): number {
  for (let i = 0; i < PER_SAMPLE; i++) fn(); // warm up
  const samples: number[] = [];
  for (let s = 0; s < SAMPLES; s++) {
    const t0 = performance.now();
    for (let i = 0; i < PER_SAMPLE; i++) fn();
    samples.push((performance.now() - t0) / PER_SAMPLE);
  }
  samples.sort((a, b) => a - b);
  return samples[Math.floor(SAMPLES / 2)]! * 1000; // µs, median
}

// ---------------------------------------------------------------- ours

function oursBench(data: BoardData) {
  const input = data as unknown as BoardInput;
  const board: Board = createBoard(input);
  const infra = board.sections[1]!;
  const big = infra.items.find((i) => i.size === 'large')!;
  const wide = board.sections[2]!.items.find((i) => i.size === 'wide' && i.supportedSizes.includes('small'))!;
  const extra = { id: 'extra', type: 'pihole', size: 'small' as const, supportedSizes: ['small' as const, 'wide' as const] };
  const json = serialize(board);
  const moved = move(board, big.id, { x: 4, y: 0 });
  return {
    load: time(() => void createBoard(input)),
    move: time(() => void move(move(board, big.id, { x: 4, y: 0 }), big.id, { x: 0, y: 0 })),
    moveSection: time(() => void move(move(board, big.id, { sectionId: board.sections[2]!.id, x: 0, y: 0 }), big.id, { sectionId: infra.id, x: 0, y: 0 })),
    resize: time(() => void resize(resize(board, wide.id, 'small'), wide.id, 'wide')),
    insert: time(() => void remove(smartInsert(board, extra), 'extra')),
    derive: time(() => {
      deriveLayout(board, 'phone');
      deriveLayout(board, 'tablet');
    }),
    undo: time(() => void undo(commit(createHistory(board), moved))),
    serialize: time(() => void serialize(board)),
    restore: time(() => void deserialize(json)),
  };
}

// ---------------------------------------------------------------- gridstack

/** `columnChanged` is what `GridStack.column()` calls; it's marked internal, so it's missing from the typings. */
function columnChanged(e: GridStackEngine, from: number, to: number, layout: ColumnOptions): void {
  (e as unknown as { columnChanged(a: number, b: number, l: ColumnOptions): void }).columnChanged(from, to, layout);
}

interface GsBoard {
  sections: GridStackEngine;
  items: GridStackEngine[];
}

function gsLoad(data: BoardData): GsBoard {
  const sections = new GridStackEngine({ column: 12, mode: 'list' });
  const items = data.sections.map((s) => {
    const e = new GridStackEngine({ column: s.span, mode: 'compact' });
    e.batchUpdate();
    for (const it of s.items) e.addNode({ id: it.id, ...SIZE_UNITS[it.size], autoPosition: true } as GridStackNode);
    e.batchUpdate(false);
    return e;
  });
  sections.batchUpdate();
  data.sections.forEach((s, i) => sections.addNode({ id: s.id, w: s.span, h: items[i]!.getRow(), autoPosition: true } as GridStackNode));
  sections.batchUpdate(false);
  return { sections, items };
}

function gsBench(data: BoardData) {
  const g = gsLoad(data);
  const infra = g.items[1]!;
  const media = g.items[2]!;
  const big = infra.nodes.find((n) => n.w === 4 && n.h === 4)!;
  const wide = media.nodes.find((n) => n.w === 4 && n.h === 2)!;
  const save = () => JSON.stringify({ sections: g.sections.save(false), items: g.items.map((e) => e.save(false)) });
  const json = save();
  const restore = (text: string) => {
    const doc = JSON.parse(text) as { items: GridStackNode[][] };
    return doc.items.map((nodes, i) => {
      const e = new GridStackEngine({ column: data.sections[i]!.span, mode: 'compact' });
      e.batchUpdate();
      for (const n of nodes) e.addNode({ ...n });
      e.batchUpdate(false);
      return e;
    });
  };
  // a drag as gridstack's DOM layer runs it: beginUpdate, moveNode, endUpdate (the compact pass)
  const drag = (e: GridStackEngine, n: GridStackNode, to: { x?: number; y?: number; w?: number; h?: number }) => {
    e.beginUpdate(n);
    e.moveNode(n, to);
    e.endUpdate();
  };
  return {
    load: time(() => void gsLoad(data)),
    move: time(() => {
      drag(infra, big, { x: 4, y: 0 });
      drag(infra, big, { x: 0, y: 0 });
    }),
    moveSection: time(() => {
      // remove from one engine, add to the other, and back
      infra.removeNode(big, false);
      const copy = media.addNode({ id: big.id, w: big.w, h: big.h, x: 0, y: 0 } as GridStackNode);
      media.removeNode(copy, false);
      infra.addNode(big);
    }),
    resize: time(() => {
      drag(media, wide, { w: 2, h: 2 });
      drag(media, wide, { w: 4, h: 2 });
    }),
    insert: time(() => {
      const n = infra.addNode({ id: 'extra', w: 2, h: 2, autoPosition: true } as GridStackNode);
      infra.removeNode(n);
    }),
    derive: time(() => {
      // what column(4), column(8) and column(12) do to the sections' engine and each section's engine
      for (const [from, to] of [[12, 4], [4, 8], [8, 12]] as const) {
        columnChanged(g.sections, from, to, 'list');
        g.items.forEach((e, i) => columnChanged(e, Math.min(from, data.sections[i]!.span), Math.min(to, data.sections[i]!.span), 'compact'));
      }
    }),
    undo: time(() => void restore(save())), // the app's undo: snapshot, then rebuild from it
    serialize: time(() => void save()),
    restore: time(() => void restore(json)),
  };
}

export function runTimings() {
  const data = sixtyWidgetBoard();
  const count = data.sections.reduce((a, s) => a + s.items.length, 0);
  return { count, sections: data.sections.length, ours: oursBench(data), gridstack: gsBench(data) };
}

if (import.meta.main) {
  const { count, sections, ours, gridstack } = runTimings();
  const rows: [string, keyof typeof ours, string][] = [
    ['Load the board', 'load', 'once'],
    ['Drag a Large across its section', 'move', 'there and back'],
    ['Drag to another section', 'moveSection', 'there and back'],
    ['Resize Wide ↔ Small', 'resize', 'there and back'],
    ['Insert a widget', 'insert', 'insert + remove'],
    ['Derive 4 and 8 columns', 'derive', 'ours: 2 derivations; gridstack: 12→4→8→12'],
    ['Undo one step', 'undo', 'ours: commit + undo; gridstack: save() + rebuild'],
    ['Serialize', 'serialize', 'once'],
    ['Restore', 'restore', 'once'],
  ];
  console.log(`${count} widgets in ${sections} sections · Bun ${Bun.version} · median of ${SAMPLES} × ${PER_SAMPLE}\n`);
  console.log('| Operation (60 widgets) | Unit | @chadash/layout | gridstack 14 engine |');
  console.log('|---|---|---:|---:|');
  for (const [name, key, unit] of rows) console.log(`| ${name} | ${unit} | ${ours[key].toFixed(1)} µs | ${gridstack[key].toFixed(1)} µs |`);
}
