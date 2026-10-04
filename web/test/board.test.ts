import { afterEach, describe, expect, it, vi } from 'vitest';
import { LAYOUT_VERSION, checkInvariants, deriveLayout, fromDocument, toDocument } from '@chadash/layout';
import { defaultBoard } from '../src/model/defaultLayout';
import { parseItemId, WIDGETS } from '../src/model/widgets';
import { createBoardStore } from '../src/stores/board';
import { docs, healthy, withIncidents } from './fixtures';

afterEach(() => vi.unstubAllGlobals());

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

describe('default layout', () => {
  const now = Date.parse(withIncidents.now);

  it('builds the Demo Home board with valid, supported sizes', () => {
    const board = defaultBoard(docs(withIncidents).graph, now);
    expect(checkInvariants(board)).toEqual([]);
    expect(board.sections.map((s) => [s.id, s.span])).toEqual([
      ['home', 4],
      ['infra', 8],
      ['media', 8],
      ['activity', 4],
    ]);
    const ids = board.sections.flatMap((s) => s.items.map((i) => i.id));
    expect(ids).toEqual([
      'cameras',
      'homeassistant',
      'fleet',
      'opnsense',
      'backups',
      'containers',
      'host@unraid:jupiter',
      'traefik',
      'uptimekuma',
      'plex',
      'arr@app:sonarr',
      'arr@app:radarr',
      'downloads',
      'calendar',
      'incidents',
    ]);
    for (const s of board.sections) {
      for (const i of s.items) {
        const w = WIDGETS[i.type]!;
        expect(w.sizes).toContain(i.size);
        expect(Boolean(parseItemId(i.id).target)).toBe(Boolean(w.bound));
      }
    }
  });

  it('packs the desktop bands evenly and derives tablet and phone without forcing sizes', () => {
    const board = defaultBoard(docs(withIncidents).graph, now);
    const desk = deriveLayout(board, 'desktop');
    expect(desk.sections.map((s) => [s.id, s.band, s.column, s.rows])).toEqual([
      ['home', 0, 0, 6],
      ['infra', 0, 4, 6],
      ['media', 1, 0, 4],
      ['activity', 1, 8, 4],
    ]);
    for (const bp of ['tablet', 'phone'] as const) {
      const l = deriveLayout(board, bp);
      expect(l.sections.flatMap((s) => s.items).filter((i) => i.forced)).toEqual([]);
    }
    expect(deriveLayout(board, 'phone').sections.every((s) => s.span === 4)).toBe(true);
  });

  it('round-trips through the stored document', () => {
    const board = defaultBoard(docs(healthy).graph, now);
    expect(fromDocument(JSON.parse(JSON.stringify(toDocument(board))))).toEqual(board);
  });

  it('never paints an empty first page, even without a graph', () => {
    const board = defaultBoard(undefined, now);
    expect(board.sections.flatMap((s) => s.items).length).toBeGreaterThan(8);
    expect(checkInvariants(board)).toEqual([]);
  });
});

describe('board store', () => {
  const graph = docs(withIncidents).graph;
  const waitForGraph = () => Promise.resolve(graph);

  it('creates and saves the default layout when the board has none', async () => {
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/boards') return json(200, { boards: [{ id: 'home', name: 'Home' }] });
      if (url === '/api/boards/home/layout' && (!init?.method || init.method === 'GET')) {
        return json(200, { revision: 0, format_version: LAYOUT_VERSION, layout: null });
      }
      if (url === '/api/boards/home/layout' && init?.method === 'PUT') return json(200, { revision: 1 });
      return json(404, { error: { code: 'not_found', message: 'no' } });
    });
    vi.stubGlobal('fetch', fetchMock);
    const store = createBoardStore({ waitForGraph, now: () => Date.parse(withIncidents.now) });
    await store.load();
    expect(store.state.status).toBe('ready');
    expect(store.state.revision).toBe(1);
    expect(store.state.unsaved).toBe(false);
    const put = fetchMock.mock.calls.find((c) => c[1]?.method === 'PUT')!;
    const body = JSON.parse(put[1]!.body as string);
    expect(body.revision).toBe(0);
    expect(body.format_version).toBe(LAYOUT_VERSION);
    expect(fromDocument(body.layout)).toEqual(store.board.value);
  });

  it('loads a stored layout as is', async () => {
    const stored = toDocument(defaultBoard(undefined));
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => (url === '/api/boards' ? json(200, { boards: [{ id: 'b1', name: 'Home' }] }) : json(200, { revision: 7, format_version: 1, layout: stored }))),
    );
    const store = createBoardStore({ waitForGraph });
    await store.load();
    expect(store.state.revision).toBe(7);
    expect(toDocument(store.board.value!)).toEqual(stored);
  });

  it('takes the other writer’s layout on 409', async () => {
    const theirs = toDocument(defaultBoard(undefined));
    let gets = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        if (url === '/api/boards') return json(200, { boards: [{ id: 'home', name: 'Home' }] });
        if (init?.method === 'PUT') return json(409, { error: { code: 'conflict', message: 'stale revision' }, revision: 1 });
        gets++;
        return gets === 1 ? json(200, { revision: 0, format_version: 1, layout: null }) : json(200, { revision: 1, format_version: 1, layout: theirs });
      }),
    );
    const store = createBoardStore({ waitForGraph });
    await store.load();
    expect(store.state.revision).toBe(1);
    expect(toDocument(store.board.value!)).toEqual(theirs);
    expect(store.state.unsaved).toBe(false);
  });

  it('saves an edited board with the current revision, and yields to a newer one on 409', async () => {
    const stored = toDocument(defaultBoard(undefined));
    let putCount = 0;
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/boards') return json(200, { boards: [{ id: 'home', name: 'Home' }] });
      if (init?.method === 'PUT') {
        putCount++;
        return putCount === 1 ? json(200, { revision: 4 }) : json(409, { error: { code: 'conflict', message: 'stale' }, revision: 9 });
      }
      return json(200, { revision: putCount ? 9 : 3, format_version: 1, layout: stored });
    });
    vi.stubGlobal('fetch', fetchMock);
    const store = createBoardStore({ waitForGraph });
    await store.load();
    expect(store.state.revision).toBe(3);
    const edited = defaultBoard(graph, Date.parse(withIncidents.now));
    expect(await store.save(edited)).toBe(true);
    expect(JSON.parse(fetchMock.mock.calls.find((c) => c[1]?.method === 'PUT')![1]!.body as string).revision).toBe(3);
    expect(store.state.revision).toBe(4);
    expect(await store.save(edited)).toBe(false);
    expect(store.state.revision).toBe(9);
    expect(toDocument(store.board.value!)).toEqual(stored);
  });

  it('reports a hub it can’t reach', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')));
    const store = createBoardStore({ waitForGraph });
    await store.load();
    expect(store.state.status).toBe('error');
    expect(store.state.error).toMatch(/Can't reach the hub/);
  });
});
