import { describe, expect, test } from 'bun:test';
import {
  checkInvariants,
  createBoard,
  deriveAll,
  deriveLayout,
  move,
  place,
  sectionColumns,
  type Board,
  type Breakpoint,
} from '../src/index.ts';
import { item, section } from './helpers.ts';

function layoutOf(board: Board, bp: Breakpoint, sectionId: string): string[] {
  const s = deriveLayout(board, bp).sections.find((sec) => sec.id === sectionId);
  return (s?.items ?? []).map((p) => `${p.id}:${p.size}@${p.x},${p.y}${p.forced ? '!' : ''}`);
}

const six = () =>
  createBoard({
    sections: [section('main', ['a', 'b', 'c', 'd', 'e', 'f'].map((id) => item(id, 'small')))],
  });

describe('derived breakpoints', () => {
  test('desktop is the canonical layout', () => {
    const board = six();
    expect(layoutOf(board, 'desktop', 'main')).toEqual([
      'a:small@0,0', 'b:small@2,0', 'c:small@4,0', 'd:small@6,0', 'e:small@8,0', 'f:small@10,0',
    ]);
  });

  test('a full-width section narrows to 8 and 4 columns, keeping Z order', () => {
    const board = six();
    expect(layoutOf(board, 'tablet', 'main')).toEqual([
      'a:small@0,0', 'b:small@2,0', 'c:small@4,0', 'd:small@6,0', 'e:small@0,2', 'f:small@2,2',
    ]);
    expect(layoutOf(board, 'phone', 'main')).toEqual([
      'a:small@0,0', 'b:small@2,0', 'c:small@0,2', 'd:small@2,2', 'e:small@0,4', 'f:small@2,4',
    ]);
  });

  test('a section that still fits keeps its internal structure', () => {
    const board = createBoard({
      sections: [section('side', [item('w', 'wide'), item('s1', 'small'), item('s2', 'small')], 4)],
    });
    expect(layoutOf(board, 'tablet', 'side')).toEqual(layoutOf(board, 'desktop', 'side'));
    expect(layoutOf(board, 'phone', 'side')).toEqual(layoutOf(board, 'desktop', 'side'));
  });

  test('a hero falls back to large on the phone; hero-only widgets are forced and flagged', () => {
    const board = createBoard({
      sections: [section('main', [item('cams', 'hero', ['hero', 'large']), item('atlas', 'hero')])],
    });
    expect(layoutOf(board, 'tablet', 'main')).toEqual(['cams:hero@0,0', 'atlas:hero@0,4']);
    expect(layoutOf(board, 'phone', 'main')).toEqual(['cams:large@0,0', 'atlas:large@0,4!']);
    expect(checkInvariants(board)).toEqual([]);
  });

  test('an item wider than the 4-column grid always fits after fallback', () => {
    const board = createBoard({
      sections: [section('main', [item('ls', 'strip-long'), item('h', 'hero', ['hero', 'small'])])],
    });
    expect(layoutOf(board, 'phone', 'main')).toEqual(['ls:strip@0,0!', 'h:small@0,1']);
  });

  test('empty sections derive to zero rows', () => {
    const board = createBoard({ sections: [section('empty', []), section('also', [], 4)] });
    for (const bp of ['desktop', 'tablet', 'phone'] as const) {
      expect(deriveLayout(board, bp).sections.map((s) => [s.id, s.rows, s.items.length])).toEqual([
        ['empty', 0, 0],
        ['also', 0, 0],
      ]);
    }
  });

  test('sections flow across the board in bands without back-filling', () => {
    const board = createBoard({
      sections: [
        section('s8', [], 8),
        section('s4', [], 4),
        section('s6a', [], 6),
        section('s6b', [], 6),
        section('s12', [], 12),
      ],
    });
    const flow = (bp: Breakpoint) => deriveLayout(board, bp).sections.map((s) => `${s.id}:${s.band}/${s.column}+${s.span}`);
    expect(flow('desktop')).toEqual(['s8:0/0+8', 's4:0/8+4', 's6a:1/0+6', 's6b:1/6+6', 's12:2/0+12']);
    expect(flow('tablet')).toEqual(['s8:0/0+8', 's4:1/0+4', 's6a:2/0+6', 's6b:3/0+6', 's12:4/0+8']);
    expect(flow('phone')).toEqual(['s8:0/0+4', 's4:1/0+4', 's6a:2/0+4', 's6b:3/0+4', 's12:4/0+4']);
  });

  test('section heights are reported in rows', () => {
    const board = six();
    expect(deriveLayout(board, 'desktop').sections[0]?.rows).toBe(2);
    expect(deriveLayout(board, 'tablet').sections[0]?.rows).toBe(4);
    expect(deriveLayout(board, 'phone').sections[0]?.rows).toBe(6);
  });

  test('derivation is deterministic and includes every breakpoint', () => {
    const board = six();
    const all = deriveAll(board);
    expect(Object.keys(all)).toEqual(['desktop', 'tablet', 'phone']);
    expect(all.phone).toEqual(deriveLayout(six(), 'phone'));
    expect(all.tablet.columns).toBe(8);
  });
});

describe('per-breakpoint overrides', () => {
  test('span, order and sizes overrides are honoured', () => {
    const board = createBoard({
      sections: [
        section('main', [item('a', 'wide', ['wide', 'small']), item('b', 'small'), item('c', 'small')], 12, {
          overrides: { phone: { span: 2, order: ['c', 'b', 'a'] }, tablet: { sizes: { a: 'small' } } },
        }),
      ],
    });
    expect(sectionColumns(board.sections[0]!, 'phone')).toBe(2);
    expect(layoutOf(board, 'phone', 'main')).toEqual(['c:small@0,0', 'b:small@0,2', 'a:small@0,4']);
    expect(layoutOf(board, 'tablet', 'main')).toEqual(['a:small@0,0', 'b:small@2,0', 'c:small@4,0']);
    expect(checkInvariants(board)).toEqual([]);
  });

  test('a narrow span override forces the closest system size when nothing supported fits', () => {
    const board = createBoard({
      sections: [section('main', [item('w', 'wide')], 12, { overrides: { phone: { span: 2 } } })],
    });
    expect(layoutOf(board, 'phone', 'main')).toEqual(['w:small@0,0!']);
  });

  test('an item added after a phone order override slots in after its canonical predecessor', () => {
    let board = createBoard({ sections: [section('main', ['a', 'b', 'c'].map((id) => item(id, 'small')))] });
    board = move(board, 'c', { x: 0, y: 0 }, { breakpoint: 'phone' });
    expect(board.sections[0]?.overrides?.phone?.order).toEqual(['c', 'a', 'b']);
    board = place(board, item('n', 'small'), { sectionId: 'main', x: 2, y: 0 });
    // Canonical order is a, n, b, c; n follows a in the phone order too.
    expect(board.sections[0]?.items.map((i) => i.id)).toEqual(['a', 'n', 'b', 'c']);
    expect(board.sections[0]?.overrides?.phone?.order).toEqual(['c', 'a', 'n', 'b']);
    expect(layoutOf(board, 'phone', 'main').map((s) => s.split(':')[0])).toEqual(['c', 'a', 'n', 'b']);
  });

  test('an override size that no longer fits is ignored', () => {
    const board = createBoard({
      sections: [
        section('main', [item('a', 'large', ['large', 'wide', 'small'])], 12, {
          overrides: { phone: { span: 2, sizes: { a: 'wide' } } },
        }),
      ],
    });
    expect(layoutOf(board, 'phone', 'main')).toEqual(['a:small@0,0']);
  });
});
