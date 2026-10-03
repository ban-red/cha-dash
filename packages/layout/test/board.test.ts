import { describe, expect, test } from 'bun:test';
import {
  LayoutError,
  addSection,
  checkInvariants,
  clearOverrides,
  createBoard,
  findItem,
  getSection,
  moveSection,
  removeSection,
  resize,
  setSectionSpan,
  setSectionTitle,
  type Board,
  type ItemInput,
  type LayoutErrorCode,
} from '../src/index.ts';
import { item, positions, section } from './helpers.ts';

function expectCode(fn: () => unknown, code: LayoutErrorCode, message?: string): void {
  let caught: unknown;
  try {
    fn();
  } catch (error) {
    caught = error;
  }
  expect(caught).toBeInstanceOf(LayoutError);
  expect((caught as LayoutError).code).toBe(code);
  if (message !== undefined) expect((caught as LayoutError).message).toContain(message);
}

describe('createBoard', () => {
  test('an empty board', () => {
    expect(createBoard()).toEqual({ sections: [] });
  });

  test('computes positions from order and ignores input positions', () => {
    const withJunk = { ...item('a', 'small'), x: 7, y: 9 } as ItemInput;
    const board = createBoard({ sections: [section('s', [withJunk, item('b', 'wide')])] });
    expect(positions(board, 's')).toEqual(['a@0,0', 'b@2,0']);
  });

  test('defaults to a full-width section and copies its input', () => {
    const supported = ['small', 'wide'] as const;
    const input = { id: 's', items: [item('a', 'small', supported)] };
    const board = createBoard({ sections: [input] });
    expect(board.sections[0]?.span).toBe(12);
    expect(board.sections[0]?.items[0]?.supportedSizes).not.toBe(supported);
  });

  test('reports every structural problem', () => {
    expectCode(
      () =>
        createBoard({
          sections: [
            section('s', [item('a', 'small'), item('a', 'small')]),
            section('s', [item('h', 'hero')], 4),
            section('t', [item('x', 'hero', ['small'])], 13),
          ],
        }),
      'INVALID_BOARD',
    );
    const cases: [Parameters<typeof createBoard>[0], string][] = [
      [{ sections: [section('s', [item('a', 'small')]), section('t', [item('a', 'small')])] }, 'duplicate item id "a"'],
      [{ sections: [section('s', []), section('s', [])] }, 'duplicate section id "s"'],
      [{ sections: [section('s', [item('h', 'hero')], 4)] }, 'does not fit a 4-column section'],
      [{ sections: [section('s', [item('x', 'hero', ['small'])])] }, 'is not in supportedSizes'],
      [{ sections: [section('s', [item('x', 'huge' as never, ['huge' as never])])] }, 'unknown size "huge"'],
      [{ sections: [section('s', [item('x', 'small', ['small', 'small'])])] }, 'duplicate size "small"'],
      [{ sections: [section('s', [{ id: 'x', type: 't', size: 'small', supportedSizes: [] }])] }, 'non-empty array'],
      [{ sections: [section('s', [], 0)] }, 'span must be an integer in 1..12'],
      [{ sections: [section('s', [], 2.5)] }, 'span must be an integer in 1..12'],
      [{ sections: [section('s', [], 12, { overrides: { phone: { span: 5 } } })] }, 'span: must be an integer in 1..4'],
      [{ sections: [section('s', [], 12, { overrides: { watch: {} } as never })] }, 'unknown breakpoint'],
      [{ sections: [section('s', [], 12, { overrides: { phone: { sizes: { a: 'mega' as never } } } })] }, 'unknown size "mega"'],
      [{ sections: [{ id: '', items: [] }] }, 'id must be a non-empty string'],
    ];
    for (const [input, message] of cases) expectCode(() => createBoard(input), 'INVALID_BOARD', message);
  });

  test('drops stale override references and merges partial orders', () => {
    const board = createBoard({
      sections: [
        section('s', ['a', 'b', 'c', 'd'].map((id) => item(id, 'small', ['small', 'wide'])), 12, {
          overrides: {
            phone: { order: ['ghost', 'c', 'a', 'c'], sizes: { a: 'wide', b: 'hero', zz: 'small' } },
            tablet: { sizes: { zz: 'small' } },
          },
        }),
      ],
    });
    // Unknown and duplicate ids go; b and d slot in after their canonical
    // predecessors (a and c), wherever those sit in the override order.
    expect(board.sections[0]?.overrides).toEqual({ phone: { order: ['c', 'd', 'a', 'b'], sizes: { a: 'wide' } } });
    expect(checkInvariants(board)).toEqual([]);
  });
});

describe('lookups', () => {
  test('findItem and getSection', () => {
    const board = createBoard({ sections: [section('s', []), section('t', [item('a', 'small'), item('b', 'small')])] });
    expect(findItem(board, 'b')).toMatchObject({ sectionIndex: 1, index: 1, item: { id: 'b', x: 2, y: 0 } });
    expect(findItem(board, 'zz')).toBeUndefined();
    expect(getSection(board, 't')?.items.length).toBe(2);
  });
});

describe('section operations', () => {
  const base = (): Board =>
    createBoard({
      sections: [section('a', [item('x', 'small')]), section('b', [item('h', 'hero', ['hero', 'large'])]), section('c', [], 4)],
    });

  test('addSection inserts at an index (end by default)', () => {
    expect(addSection(base(), { id: 'd' }).sections.map((s) => s.id)).toEqual(['a', 'b', 'c', 'd']);
    expect(addSection(base(), { id: 'd', span: 6 }, 0).sections.map((s) => s.id)).toEqual(['d', 'a', 'b', 'c']);
    expect(addSection(base(), { id: 'd' }, 99).sections.at(-1)?.id).toBe('d');
    const withItems = addSection(base(), { id: 'd', items: [item('y', 'wide')] });
    expect(positions(withItems, 'd')).toEqual(['y@0,0']);
  });

  test('addSection refuses duplicates and bad input', () => {
    expectCode(() => addSection(base(), { id: 'a' }), 'DUPLICATE_ID');
    expectCode(() => addSection(base(), { id: 'd', items: [item('x', 'small')] }), 'DUPLICATE_ID');
    expectCode(() => addSection(base(), { id: 'd', span: 20 }), 'INVALID_ARGUMENT');
  });

  test('removeSection and moveSection', () => {
    expect(removeSection(base(), 'b').sections.map((s) => s.id)).toEqual(['a', 'c']);
    expectCode(() => removeSection(base(), 'zz'), 'UNKNOWN_SECTION');
    expect(moveSection(base(), 'c', 0).sections.map((s) => s.id)).toEqual(['c', 'a', 'b']);
    expect(moveSection(base(), 'a', 10).sections.map((s) => s.id)).toEqual(['b', 'c', 'a']);
    const board = base();
    expect(moveSection(board, 'b', 1)).toBe(board);
  });

  test('setSectionTitle sets, clears and no-ops', () => {
    const titled = setSectionTitle(base(), 'a', 'Infrastructure');
    expect(titled.sections[0]?.title).toBe('Infrastructure');
    expect(setSectionTitle(titled, 'a', 'Infrastructure')).toBe(titled);
    expect('title' in (setSectionTitle(titled, 'a', undefined).sections[0] ?? {})).toBe(false);
    expect('title' in (setSectionTitle(titled, 'a', '').sections[0] ?? {})).toBe(false);
  });

  test('setSectionSpan at desktop repacks, and refuses when an item would not fit', () => {
    const board = createBoard({ sections: [section('s', ['p', 'q', 'r'].map((id) => item(id, 'small')))] });
    expect(positions(setSectionSpan(board, 's', 4), 's')).toEqual(['p@0,0', 'q@2,0', 'r@0,2']);
    expect(setSectionSpan(board, 's', 12)).toBe(board);
    expectCode(() => setSectionSpan(base(), 'b', 4), 'NO_FIT', 'item "h"');
    expectCode(() => setSectionSpan(board, 's', 0), 'INVALID_ARGUMENT');
    expectCode(() => setSectionSpan(board, 's', 13), 'INVALID_ARGUMENT');
    // Resizing first makes it possible.
    const fits = setSectionSpan(resize(base(), 'h', 'large'), 'b', 4);
    expect(fits.sections[1]?.span).toBe(4);
  });

  test('setSectionSpan at a derived breakpoint stores or clears an override', () => {
    const board = base();
    const narrowed = setSectionSpan(board, 'a', 4, { breakpoint: 'tablet' });
    expect(narrowed.sections[0]?.overrides).toEqual({ tablet: { span: 4 } });
    expect(setSectionSpan(narrowed, 'a', 8, { breakpoint: 'tablet' }).sections[0]?.overrides).toBeUndefined();
    expect(setSectionSpan(board, 'a', 8, { breakpoint: 'tablet' })).toBe(board);
    expectCode(() => setSectionSpan(board, 'a', 5, { breakpoint: 'phone' }), 'INVALID_ARGUMENT');
  });

  test('clearOverrides by section and breakpoint', () => {
    let board = base();
    board = setSectionSpan(board, 'a', 4, { breakpoint: 'tablet' });
    board = setSectionSpan(board, 'a', 2, { breakpoint: 'phone' });
    board = setSectionSpan(board, 'b', 4, { breakpoint: 'tablet' });
    expect(clearOverrides(board, { breakpoint: 'phone' }).sections[0]?.overrides).toEqual({ tablet: { span: 4 } });
    const onlyB = clearOverrides(board, { sectionId: 'b' });
    expect(onlyB.sections[1]?.overrides).toBeUndefined();
    expect(onlyB.sections[0]).toBe(board.sections[0]!);
    const none = clearOverrides(board);
    expect(none.sections.every((s) => s.overrides === undefined)).toBe(true);
    expect(clearOverrides(none)).toBe(none);
  });
});
