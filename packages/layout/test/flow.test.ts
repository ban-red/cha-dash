import { describe, expect, test } from 'bun:test';
import fc from 'fast-check';
import {
  BREAKPOINTS,
  FLOWS,
  LayoutError,
  checkInvariants,
  createBoard,
  deriveLayout,
  deserialize,
  fromDocument,
  serialize,
  setFlow,
  toDocument,
  type Board,
  type Flow,
  type TrackArea,
} from '../src/index.ts';
import { arbBoard, arbBreakpoint, item, section } from './helpers.ts';

const box = (a: TrackArea | undefined) => (a ? `${a.column},${a.track} ${a.w}×${a.tracks}` : '-');

/**
 * The board from the screenshot that asked for this: a tall section beside
 * two short ones, then a full-width one. In bands, the short ones leave a
 * hole under them as tall as the tall one.
 */
const gappy = (flow?: Flow) =>
  createBoard({
    ...(flow && { flow }),
    sections: [
      section('activity', [item('inbox', 'wide')], 4, { title: 'Activity' }),
      section('home', [item('ha', 'wide')], 4, { title: 'Home' }),
      section('net', [item('pve', 'large')], 4, { title: 'Network' }),
      section('media', [item('plex', 'wide'), item('arr', 'wide')], 8, { title: 'Media' }),
    ],
  });

describe('flows', () => {
  test('sections is the default and keeps bands', () => {
    const layout = deriveLayout(gappy(), 'desktop');
    expect(layout.flow).toBe('sections');
    expect(layout.tracks).toBe(0);
    expect(layout.sections.map((s) => `${s.id}@${s.column}/${s.band}`)).toEqual(['activity@0/0', 'home@4/0', 'net@8/0', 'media@0/1']);
    expect(layout.sections.every((s) => s.area === undefined && s.items.every((i) => i.area === undefined))).toBe(true);
  });

  test('bento packs sections densely: a later one moves up into the hole', () => {
    const layout = deriveLayout(gappy('bento'), 'desktop');
    // A header track plus two tracks a row: the short sections take 5, Network 9.
    expect(layout.sections.map((s) => `${s.id} ${box(s.area)}`)).toEqual([
      'activity 0,0 4×5',
      'home 4,0 4×5',
      'net 8,0 4×9',
      // In bands it would start at track 9; bento puts it under Activity and Home.
      'media 0,5 8×5',
    ]);
    expect(layout.tracks).toBe(10);
    const media = layout.sections[3]!;
    expect(box(media.header)).toBe('0,5 8×1');
    expect(media.items.map((i) => `${i.id} ${box(i.area)}`)).toEqual(['plex 0,6 4×4', 'arr 4,6 4×4']);
    // Positions inside the section are unchanged.
    expect(media.items.map((i) => `${i.x},${i.y}`)).toEqual(['0,0', '4,0']);
  });

  test('mosaic packs every tile on the board, each label glued to its section’s first tile', () => {
    const layout = deriveLayout(gappy('mosaic'), 'desktop');
    const placed = layout.sections.flatMap((s) => [`${s.id}: ${box(s.header)}`, ...s.items.map((i) => `${i.id} ${box(i.area)}`)]);
    expect(placed).toEqual([
      'activity: 0,0 4×1',
      'inbox 0,1 4×4',
      'home: 4,0 4×1',
      'ha 4,1 4×4',
      'net: 8,0 4×1',
      'pve 8,1 4×8',
      'media: 0,5 4×1',
      'plex 0,6 4×4',
      'arr 4,5 4×4', // half a row up, into the gap beside Media's label
    ]);
    expect(layout.tracks).toBe(10);
    expect(layout.sections.every((s) => s.area === undefined)).toBe(true);
    // A label is at least two columns wide, even over a glyph.
    const glyph = createBoard({ flow: 'mosaic', sections: [section('s', [item('g', 'glyph')], 12, { title: 'S' })] });
    const s = deriveLayout(glyph, 'desktop').sections[0]!;
    expect([box(s.header), box(s.items[0]!.area)]).toEqual(['0,0 2×1', '0,1 1×2']);
  });

  test('untitled sections get a header track only when asked', () => {
    const board = createBoard({ flow: 'bento', sections: [section('a', [item('x', 'small')], 4), section('b', [], 4)] });
    // A block is only as wide as its tiles: one small is two columns.
    expect(deriveLayout(board, 'desktop').sections.map((s) => box(s.area))).toEqual(['0,0 2×4', '-']);
    expect(deriveLayout(board, 'desktop', { headers: 'always' }).sections.map((s) => box(s.area))).toEqual(['0,0 2×5', '2,0 4×1']);
    // An editor keeps a row for an empty section, so it can take a drop, and keeps spans.
    expect(deriveLayout(board, 'desktop', { headers: 'always', minRows: 1, fit: false }).sections.map((s) => box(s.area))).toEqual(['0,0 4×5', '4,0 4×3']);
  });

  test('a bento block trims empty columns and narrows into a hole that fits it', () => {
    // The dogfood board's shape: Services and Media span 5 with four columns of tiles,
    // Data spans 2, and Archive (two smalls, span 4) comes after it.
    const board = createBoard({
      flow: 'bento',
      sections: [
        section('activity', [item('inbox', 'large')], 4, { title: 'Activity' }),
        section('home', [item('ha', 'wide'), item('cams', 'large')], 4, { title: 'Home' }),
        section('servers', ['opn', 'a', 'b', 'c', 'd', 'e', 'f', 'g'].map((id, i) => item(id, i ? 'small' : 'wide')), 4, { title: 'Servers' }),
        section('services', [item('traefik', 'small'), item('kuma', 'small')], 5, { title: 'Services' }),
        section('media', [item('plex', 'wide'), item('radarr', 'small'), item('sonarr', 'small'), item('bazarr', 'small')], 5, { title: 'Media' }),
        section('data', [item('cal', 'tall'), item('tautulli', 'small')], 2, { title: 'Data' }),
        section('archive', [item('links', 'small'), item('hoard', 'small')], 4, { title: 'Archive' }),
        section('downloads', [item('qbit', 'wide'), item('prowlarr', 'small')], 4, { title: 'Downloads' }),
      ],
    });
    const layout = deriveLayout(board, 'desktop');
    expect(layout.sections.map((s) => `${s.id} ${box(s.area)}`)).toEqual([
      'activity 0,0 4×9',
      'home 4,0 4×13',
      'servers 8,0 4×21',
      'services 0,9 4×5', // trimmed from 5 columns, so it fits under Activity
      'media 4,13 4×13',
      'data 0,14 2×13',
      'archive 2,14 2×9', // narrowed from 4: its smalls stack beside Data
      'downloads 8,21 4×9',
    ]);
    expect(layout.tracks).toBe(30);
    // Narrowing repacks the tiles inside the block, in order.
    const archive = layout.sections[6]!;
    expect(archive.span).toBe(2);
    expect(archive.items.map((i) => `${i.id}@${i.x},${i.y}`)).toEqual(['links@0,0', 'hoard@0,2']);
    // While editing, blocks keep their spans and tiles their stored positions.
    const editing = deriveLayout(board, 'desktop', { fit: false });
    expect(editing.sections.map((s) => s.span)).toEqual([4, 4, 4, 5, 5, 2, 4, 4]);
    expect(editing.tracks).toBe(35);
  });

  test('the flow option overrides the board: an editor shows a mosaic as bento', () => {
    expect(deriveLayout(gappy('mosaic'), 'desktop', { flow: 'bento' }).flow).toBe('bento');
  });

  test('setFlow stores the default as no flow, and returns the same board when nothing changes', () => {
    const board = gappy();
    const bento = setFlow(board, 'bento');
    expect(bento.flow).toBe('bento');
    expect(setFlow(bento, 'bento')).toBe(bento);
    expect(setFlow(board, 'sections')).toBe(board);
    expect('flow' in setFlow(bento, 'sections')).toBe(false);
    expect(bento.sections).toBe(board.sections);
    expect(() => setFlow(board, 'masonry' as Flow)).toThrow(LayoutError);
    expect(() => createBoard({ flow: 'masonry' as Flow })).toThrow(LayoutError);
  });

  test('the document carries the flow, and leaves it out for the default', () => {
    expect('flow' in toDocument(gappy())).toBe(false);
    expect(toDocument(gappy('mosaic')).flow).toBe('mosaic');
    expect(deserialize(serialize(gappy('bento'))).flow).toBe('bento');
    // A flow from a newer app reads as the default instead of failing the board.
    const doc = { ...toDocument(gappy()), flow: 'spiral' };
    expect(fromDocument(doc).flow).toBeUndefined();
  });

  test('every flow keeps every invariant on random boards', () => {
    fc.assert(
      fc.property(arbBoard, fc.constantFrom(...FLOWS), (board, flow) => {
        expect(checkInvariants(setFlow(board, flow))).toEqual([]);
      }),
      { numRuns: 200 },
    );
  });

  test('a change to the last section never moves anything before it', () => {
    fc.assert(
      fc.property(arbBoard, arbBreakpoint, fc.constantFrom<Flow>('bento', 'mosaic'), (board, bp, flow) => {
        if (board.sections.length < 2) return;
        const without: Board = { ...board, sections: board.sections.slice(0, -1) };
        const all = deriveLayout(board, bp, { flow }).sections.slice(0, -1);
        expect(deriveLayout(without, bp, { flow }).sections).toEqual(all);
      }),
      { numRuns: 300 },
    );
  });

  test('each flow lays out every breakpoint', () => {
    for (const flow of FLOWS) {
      for (const bp of BREAKPOINTS) expect(deriveLayout(gappy(flow), bp).flow).toBe(flow);
    }
  });
});
