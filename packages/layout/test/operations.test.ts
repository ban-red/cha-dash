import { describe, expect, test } from 'bun:test';
import {
  LayoutError,
  checkInvariants,
  createBoard,
  deriveLayout,
  getItem,
  move,
  nudge,
  place,
  remove,
  resize,
  smartInsert,
  type Board,
  type LayoutErrorCode,
} from '../src/index.ts';
import { item, positions, section } from './helpers.ts';

function expectCode(fn: () => unknown, code: LayoutErrorCode): void {
  let caught: unknown;
  try {
    fn();
  } catch (error) {
    caught = error;
  }
  expect(caught).toBeInstanceOf(LayoutError);
  expect((caught as LayoutError).code).toBe(code);
}

const smalls = (...ids: string[]) => ids.map((id) => item(id, 'small', ['small', 'wide', 'large']));

/** One 12-column section with six smalls filling row 0. */
function row(): Board {
  return createBoard({ sections: [section('main', smalls('a', 'b', 'c', 'd', 'e', 'f')), section('side', [], 4)] });
}

function phoneOrder(board: Board, sectionId: string): string[] {
  const s = deriveLayout(board, 'phone').sections.find((sec) => sec.id === sectionId);
  return (s?.items ?? []).map((p) => `${p.id}@${p.x},${p.y}`);
}

describe('place', () => {
  test('lands on the target cell and neighbours reflow', () => {
    const next = place(row(), item('n', 'small'), { sectionId: 'main', x: 4, y: 0 });
    expect(positions(next, 'main')).toEqual(['a@0,0', 'b@2,0', 'n@4,0', 'c@6,0', 'd@8,0', 'e@10,0', 'f@0,2']);
    expect(checkInvariants(next)).toEqual([]);
  });

  test('clamps far-away targets instead of failing', () => {
    const next = place(row(), item('n', 'small'), { sectionId: 'main', x: 100, y: 100 });
    expect(positions(next, 'main')).toEqual(['a@0,0', 'b@2,0', 'c@4,0', 'd@6,0', 'e@8,0', 'f@10,0', 'n@0,2']);
  });

  test('adopts the closest supported size that fits a narrow section', () => {
    const next = place(row(), item('n', 'hero', ['hero', 'large']), { sectionId: 'side', x: 0, y: 0 });
    expect(getItem(next, 'n')?.size).toBe('large');
  });

  test('refuses what it cannot do', () => {
    expectCode(() => place(row(), item('n', 'hero'), { sectionId: 'side', x: 0, y: 0 }), 'NO_FIT');
    expectCode(() => place(row(), item('a', 'small'), { sectionId: 'main', x: 0, y: 0 }), 'DUPLICATE_ID');
    expectCode(() => place(row(), item('n', 'small'), { sectionId: 'nope', x: 0, y: 0 }), 'UNKNOWN_SECTION');
    expectCode(() => place(row(), item('n', 'hero', ['small']), { sectionId: 'main', x: 0, y: 0 }), 'UNSUPPORTED_SIZE');
    expectCode(() => place(row(), item('n', 'small'), { sectionId: 'main', x: Number.NaN, y: 0 }), 'INVALID_ARGUMENT');
    expectCode(() => place(row(), { id: '', type: 't', size: 'small', supportedSizes: ['small'] }, { sectionId: 'main', x: 0, y: 0 }), 'INVALID_ARGUMENT');
  });
});

describe('smartInsert', () => {
  test('fills the earliest gap that fits without moving anything', () => {
    // A long strip on row 0 leaves cols 8–11 free above the hero's right side.
    const board = createBoard({ sections: [section('main', [item('s1', 'strip-long'), item('h1', 'hero')])] });
    expect(positions(board, 'main')).toEqual(['s1@0,0', 'h1@0,1']);
    const next = smartInsert(board, item('n', 'small'));
    // Z order follows reading order: n sits on row 0, before the hero.
    expect(positions(next, 'main')).toEqual(['s1@0,0', 'n@8,0', 'h1@0,1']);
  });

  test('with a viewport hint, lands on that row and displaces as little as possible', () => {
    const ids = Array.from({ length: 18 }, (_, i) => `i${i}`);
    const board = createBoard({ sections: [section('main', smalls(...ids))] });
    const next = smartInsert(board, item('n', 'small'), { near: { sectionId: 'main', y: 4 } });
    const n = getItem(next, 'n');
    expect(n).toMatchObject({ x: 10, y: 4 });
    // Only the last item had to move.
    const moved = ids.filter((id) => {
      const before = getItem(board, id);
      const after = getItem(next, id);
      return before?.x !== after?.x || before?.y !== after?.y;
    });
    expect(moved).toEqual(['i17']);
  });

  test('skips sections the item cannot fit and respects sectionId', () => {
    const board = createBoard({ sections: [section('narrow', [], 2), section('wide', [])] });
    expect(getItem(smartInsert(board, item('n', 'wide')), 'n')).toBeDefined();
    expect(smartInsert(board, item('n', 'wide')).sections[1]?.items.map((i) => i.id)).toEqual(['n']);
    expect(smartInsert(board, item('g', 'glyph'), { sectionId: 'wide' }).sections[1]?.items.map((i) => i.id)).toEqual(['g']);
  });

  test('a near hint for a section that cannot take the item falls back to the closest one that can', () => {
    const board = createBoard({ sections: [section('a', []), section('b', [], 2), section('c', [])] });
    const next = smartInsert(board, item('n', 'wide'), { near: { sectionId: 'b', y: 0 } });
    expect(next.sections[0]?.items.map((i) => i.id)).toEqual(['n']);
  });

  test('refuses when there is nowhere to put it', () => {
    expectCode(() => smartInsert(createBoard(), item('n', 'small')), 'NO_SECTION');
    expectCode(() => smartInsert(createBoard({ sections: [section('s', [], 4)] }), item('n', 'hero')), 'NO_FIT');
    expectCode(() => smartInsert(row(), item('n', 'small'), { sectionId: 'nope' }), 'UNKNOWN_SECTION');
    expectCode(() => smartInsert(row(), item('n', 'small'), { sectionId: 'main', near: { sectionId: 'nope', y: 0 } }), 'UNKNOWN_SECTION');
    expectCode(() => smartInsert(row(), item('n', 'small'), { near: { sectionId: 'main', y: Number.NaN } }), 'INVALID_ARGUMENT');
  });
});

describe('move', () => {
  test('moves to the target and the others close ranks in order', () => {
    const next = move(row(), 'a', { x: 10, y: 0 });
    expect(positions(next, 'main')).toEqual(['b@0,0', 'c@2,0', 'd@4,0', 'e@6,0', 'f@8,0', 'a@10,0']);
  });

  test('dropping an item where it already is returns the same board', () => {
    const board = row();
    expect(move(board, 'c', { x: 4, y: 0 })).toBe(board);
  });

  test('full row: moving into it wraps the overflow to the next row', () => {
    const board = createBoard({ sections: [section('main', [item('l', 'strip-long'), item('s', 'strip'), item('g', 'glyph')])] });
    expect(positions(board, 'main')).toEqual(['l@0,0', 's@8,0', 'g@0,1']);
    const next = move(board, 'g', { x: 11, y: 0 });
    expect(positions(next, 'main')).toEqual(['l@0,0', 'g@8,0', 's@0,1']);
  });

  test('moves across sections, adopting a size that fits there', () => {
    const board = createBoard({
      sections: [section('main', [item('h', 'hero', ['hero', 'large']), ...smalls('a')]), section('side', [], 4)],
    });
    const next = move(board, 'h', { sectionId: 'side', x: 0, y: 0 });
    expect(positions(next, 'main')).toEqual(['a@0,0']);
    expect(next.sections[1]?.items).toEqual([expect.objectContaining({ id: 'h', size: 'large', x: 0, y: 0 })]);
    expect(checkInvariants(next)).toEqual([]);
  });

  test('refuses a cross-section move when no supported size fits', () => {
    const board = createBoard({ sections: [section('main', [item('h', 'hero')]), section('side', [], 4)] });
    expectCode(() => move(board, 'h', { sectionId: 'side', x: 0, y: 0 }), 'NO_FIT');
  });

  test('on the phone it writes an override order and leaves the canonical layout alone', () => {
    const board = row();
    expect(phoneOrder(board, 'main')).toEqual(['a@0,0', 'b@2,0', 'c@0,2', 'd@2,2', 'e@0,4', 'f@2,4']);
    const next = move(board, 'f', { x: 0, y: 0 }, { breakpoint: 'phone' });
    expect(positions(next, 'main')).toEqual(positions(board, 'main'));
    expect(next.sections[0]?.overrides?.phone?.order).toEqual(['f', 'a', 'b', 'c', 'd', 'e']);
    expect(phoneOrder(next, 'main')[0]).toBe('f@0,0');
    // Moving it back to its derived spot clears the override again.
    const back = move(next, 'f', { x: 2, y: 4 }, { breakpoint: 'phone' });
    expect(back.sections[0]?.overrides).toBeUndefined();
  });

  test('cross-section moves only work on the canonical layout', () => {
    expectCode(() => move(row(), 'a', { sectionId: 'side', x: 0, y: 0 }, { breakpoint: 'tablet' }), 'CROSS_SECTION_AT_BREAKPOINT');
    expectCode(() => move(row(), 'zzz', { x: 0, y: 0 }), 'UNKNOWN_ITEM');
  });
});

describe('resize', () => {
  test('resizes in place and the section reflows', () => {
    const next = resize(row(), 'a', 'wide');
    expect(positions(next, 'main')).toEqual(['a@0,0', 'b@4,0', 'c@6,0', 'd@8,0', 'e@10,0', 'f@0,2']);
    expect(getItem(next, 'a')?.size).toBe('wide');
  });

  test('only supported sizes, only sizes that fit', () => {
    expectCode(() => resize(row(), 'a', 'hero'), 'UNSUPPORTED_SIZE');
    expectCode(() => resize(row(), 'a', 'nope' as never), 'UNSUPPORTED_SIZE');
    const narrow = createBoard({ sections: [section('n', [item('x', 'small', ['small', 'large'])], 2)] });
    expectCode(() => resize(narrow, 'x', 'large'), 'NO_FIT');
  });

  test('resizing to the current size returns the same board', () => {
    const board = row();
    expect(resize(board, 'a', 'small')).toBe(board);
  });

  test('on the phone it stores a size override, cleared when it matches the derived size', () => {
    const board = createBoard({ sections: [section('main', [item('h', 'hero', ['hero', 'large', 'wide'])])] });
    expect(deriveLayout(board, 'phone').sections[0]?.items[0]?.size).toBe('large');
    const next = resize(board, 'h', 'wide', { breakpoint: 'phone' });
    expect(next.sections[0]?.overrides).toEqual({ phone: { sizes: { h: 'wide' } } });
    expect(deriveLayout(next, 'phone').sections[0]?.items[0]?.size).toBe('wide');
    expect(getItem(next, 'h')?.size).toBe('hero');
    const back = resize(next, 'h', 'large', { breakpoint: 'phone' });
    expect(back.sections[0]?.overrides).toBeUndefined();
    expectCode(() => resize(board, 'h', 'hero', { breakpoint: 'phone' }), 'NO_FIT');
  });
});

describe('remove', () => {
  test('removes the item and the rest reflow', () => {
    expect(positions(remove(row(), 'a'), 'main')).toEqual(['b@0,0', 'c@2,0', 'd@4,0', 'e@6,0', 'f@8,0']);
    expectCode(() => remove(row(), 'zzz'), 'UNKNOWN_ITEM');
  });

  test('cleans up overrides that mention the item', () => {
    let board = move(row(), 'f', { x: 0, y: 0 }, { breakpoint: 'phone' });
    board = resize(board, 'a', 'wide', { breakpoint: 'phone' });
    board = remove(board, 'a');
    expect(board.sections[0]?.overrides).toEqual({ phone: { order: ['f', 'b', 'c', 'd', 'e'] } });
    board = remove(board, 'f');
    expect(board.sections[0]?.overrides?.phone?.order).toEqual(['b', 'c', 'd', 'e']);
    expect(checkInvariants(board)).toEqual([]);
  });

  test('removing the last item leaves an empty section', () => {
    const board = createBoard({ sections: [section('main', [item('only', 'small')])] });
    const next = remove(board, 'only');
    expect(next.sections[0]?.items).toEqual([]);
    expect(deriveLayout(next, 'phone').sections[0]).toMatchObject({ rows: 0, items: [] });
  });
});

describe('nudge (keyboard)', () => {
  const twoRows = () => createBoard({ sections: [section('main', smalls('a', 'b', 'c', 'd', 'e', 'f', 'g', 'h')), section('next', [])] });

  test('left and right swap with the neighbour in Z order', () => {
    expect(positions(nudge(twoRows(), 'b', 'left'), 'main').slice(0, 2)).toEqual(['b@0,0', 'a@2,0']);
    expect(positions(nudge(twoRows(), 'a', 'right'), 'main').slice(0, 2)).toEqual(['b@0,0', 'a@2,0']);
  });

  test('up and down move to the nearest spot in the row above or below', () => {
    expect(getItem(nudge(twoRows(), 'h', 'up'), 'h')).toMatchObject({ x: 2, y: 0 });
    const down = nudge(twoRows(), 'a', 'down');
    expect(getItem(down, 'a')).toMatchObject({ x: 0, y: 2 });
    expect(getItem(down, 'h')).toMatchObject({ x: 2, y: 2 });
  });

  test('crosses into the adjacent section at the edges, or stops when there is none', () => {
    const board = twoRows();
    const crossed = nudge(board, 'h', 'right');
    expect(crossed.sections[1]?.items.map((i) => i.id)).toEqual(['h']);
    expect(nudge(board, 'a', 'left')).toBe(board);
    expect(nudge(board, 'a', 'up')).toBe(board);
    const back = nudge(crossed, 'h', 'left');
    expect(back.sections[1]?.items).toEqual([]);
    expect(back.sections[0]?.items.map((i) => i.id).includes('h')).toBe(true);
  });

  test('at a derived breakpoint it writes an override and stops at section edges', () => {
    const board = twoRows();
    const next = nudge(board, 'b', 'left', { breakpoint: 'phone' });
    expect(next.sections[0]?.overrides?.phone?.order?.slice(0, 2)).toEqual(['b', 'a']);
    expect(positions(next, 'main')).toEqual(positions(board, 'main'));
    expect(nudge(board, 'h', 'right', { breakpoint: 'phone' })).toBe(board);
  });
});

describe('immutability', () => {
  test('operations never mutate their input', () => {
    const board = row();
    const frozen = structuredClone(board);
    place(board, item('n', 'small'), { sectionId: 'main', x: 2, y: 0 });
    smartInsert(board, item('n', 'small'), { near: { sectionId: 'main', y: 2 } });
    move(board, 'a', { x: 6, y: 0 });
    move(board, 'a', { x: 0, y: 4 }, { breakpoint: 'phone' });
    resize(board, 'b', 'large');
    resize(board, 'b', 'wide', { breakpoint: 'tablet' });
    remove(board, 'c');
    nudge(board, 'd', 'down');
    expect(board).toEqual(frozen);
  });

  test('untouched sections keep their identity', () => {
    const board = row();
    const next = move(board, 'a', { x: 10, y: 0 });
    expect(next.sections[1]).toBe(board.sections[1]!);
    expect(next.sections[0]).not.toBe(board.sections[0]!);
  });
});
