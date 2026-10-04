import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { LAYOUT_VERSION, toDocument } from '@chadash/layout';
import goldenRaw from '../../testdata/importers/homepage/dogfood/expected-plan.json?raw';
import WidgetHost from '../src/widgets/WidgetHost.vue';
import { defaultBoard } from '../src/model/defaultLayout';
import { serviceStatus } from '../src/model/service';
import type { GraphDoc } from '../src/model/types';
import { setDisplayTimeZone } from '../src/lib/format';
import { createBoardStore } from '../src/stores/board';
import { pruneTiles, tilesFromDoc, withTile, type TilesDoc } from '../src/stores/tiles';
import { docs, withIncidents } from './fixtures';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

/** The importer's golden dry run for the dogfood home (internal/importer/homepage). */
const golden = JSON.parse(goldenRaw) as { plan: { tiles: Record<string, unknown>[]; board: { layout: unknown } } };

/**
 * The tiles document the importer stores for the golden plan, built the way
 * internal/importer.TileConfigs builds it (the Go side tests that mapping).
 */
function importedTiles(): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const t of golden.plan.tiles) {
    const o = (t['origin'] ?? {}) as Record<string, string>;
    const c: Record<string, unknown> = { origin: { source: 'homepage', file: o['file'], group: o['group'], entry: o['entry'] || undefined } };
    for (const k of ['name', 'icon', 'description', 'options']) if (t[k]) c[k] = t[k];
    if (typeof t['href'] === 'string' && /^https?:\/\//i.test(t['href'])) c['href'] = t['href'];
    if (t['chromeless']) c['chromeless'] = true;
    if (t['instance'] || t['check'] || t['sources']) c['bind'] = { instance: t['instance'], check: t['check'], sources: t['sources'] };
    out[t['item_id'] as string] = JSON.parse(JSON.stringify(c));
  }
  return out;
}

beforeEach(() => setDisplayTimeZone('UTC'));
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('the tiles adapter', () => {
  it('reads the board’s tiles document forgivingly', () => {
    const t = tilesFromDoc(importedTiles());
    expect(Object.keys(t)).toHaveLength(22);
    expect(t['service@homepage:servers/proxmoxpluto']).toEqual({
      name: 'Pluto PVE (main opnsense)',
      icon: 'https://api.iconify.design/simple-icons:proxmox.svg?color=%23888888',
      href: 'https://pluto.lan:8006',
      bind: { instance: 'i_o2asa67xbuwy4huh' },
      origin: { source: 'homepage', file: 'services.yaml', group: 'Servers', entry: 'ProxmoxPluto' },
    });
    expect(t['calendar']!.chromeless).toBe(true);
    expect(t['calendar']!.bind!.sources).toHaveLength(2);

    const odd = tilesFromDoc({
      a: { href: 'javascript:alert(1)', name: 'A', extra: 1, bind: { resource: '', sources: [{ instance: 'i' }] } },
      b: 'not a tile',
      '': { name: 'no id' },
      c: { options: [1], chromeless: 'yes' },
    });
    expect(odd).toEqual({ a: { name: 'A' }, c: {} });
    expect(tilesFromDoc(null)).toEqual({});
    expect(tilesFromDoc([1, 2])).toEqual({});
  });

  it('edits immutably: empty entries disappear, removed items take their config along', () => {
    const t: TilesDoc = { a: { name: 'A' }, b: { name: 'B' } };
    const next = withTile(t, 'a', { name: 'A2', href: 'https://a.example' });
    expect(next).toEqual({ a: { name: 'A2', href: 'https://a.example' }, b: { name: 'B' } });
    expect(t).toEqual({ a: { name: 'A' }, b: { name: 'B' } });
    expect(withTile(t, 'a', { name: '' })).toEqual({ b: { name: 'B' } });
    expect(withTile(t, 'a', undefined)).toEqual({ b: { name: 'B' } });
    expect(pruneTiles(t, ['b'])).toEqual({ b: { name: 'B' } });
    expect(pruneTiles(t, ['a', 'b', 'c'])).toBe(t);
  });
});

describe('the board store and tiles', () => {
  const graph = docs(withIncidents).graph;
  const stored = toDocument(defaultBoard(undefined));

  it('loads the tiles with the layout, and keeps them in the snapshot', async () => {
    fakeFetch({
      'GET /api/boards': { status: 200, body: { boards: [{ id: 'b1', name: 'Home' }] } },
      'GET /api/boards/b1/layout': { status: 200, body: { revision: 3, format_version: 1, layout: stored, tiles: { plex: { name: 'TV', href: 'ftp://x' } } } },
    });
    const store = createBoardStore({ waitForGraph: async () => graph });
    await store.load();
    expect(store.tiles.value).toEqual({ plex: { name: 'TV' } });
    expect(store.snapshot()).toEqual({ id: 'b1', name: 'Home', revision: 3, layout: stored, tiles: { plex: { name: 'TV' } } });

    const offline = createBoardStore({ waitForGraph: async () => graph });
    offline.restore(store.snapshot());
    expect(offline.tiles.value).toEqual({ plex: { name: 'TV' } });
  });

  it('sends tiles only when they changed, and saves one at a time on the latest revision', async () => {
    let rev = 3;
    const f = fakeFetch({
      'GET /api/boards': { status: 200, body: { boards: [{ id: 'b1', name: 'Home' }] } },
      'GET /api/boards/b1/layout': { status: 200, body: { revision: 3, format_version: 1, layout: stored, tiles: {} } },
      'PUT /api/boards/b1/layout': async () => {
        await new Promise((r) => setTimeout(r, 5));
        return { status: 200, body: { revision: ++rev } };
      },
    });
    const store = createBoardStore({ waitForGraph: async () => graph });
    await store.load();
    const b = store.board.value!;
    const first = store.save(b);
    const second = store.save(b, { plex: { name: 'TV' } });
    expect(store.tiles.value).toEqual({ plex: { name: 'TV' } }); // shown at once
    expect(await first).toBe(true);
    expect(await second).toBe(true);
    const puts = f.calls.filter((c) => c.method === 'PUT').map((c) => c.body as Record<string, unknown>);
    expect(puts.map((p) => [p['revision'], 'tiles' in p])).toEqual([
      [3, false],
      [4, true],
    ]);
    expect(puts[1]!['tiles']).toEqual({ plex: { name: 'TV' } });
    expect(puts[1]!['format_version']).toBe(LAYOUT_VERSION);
    expect(store.state.revision).toBe(5);
  });

  it('drops the saves queued behind a conflict instead of overwriting the other writer', async () => {
    let puts = 0;
    const f = fakeFetch({
      'GET /api/boards': { status: 200, body: { boards: [{ id: 'b1', name: 'Home' }] } },
      'GET /api/boards/b1/layout': () => ({ status: 200, body: { revision: puts ? 9 : 3, format_version: 1, layout: stored, tiles: puts ? { theirs: { name: 'T' } } : {} } }),
      'PUT /api/boards/b1/layout': () => {
        puts++;
        return { status: 409, body: { error: { code: 'conflict', message: 'stale' }, revision: 9 } };
      },
    });
    const store = createBoardStore({ waitForGraph: async () => graph });
    await store.load();
    const b = store.board.value!;
    const results = await Promise.all([store.save(b, { mine: { name: 'M' } }), store.save(b, { mine: { name: 'M2' } })]);
    expect(results).toEqual([false, false]);
    expect(f.calls.filter((c) => c.method === 'PUT')).toHaveLength(1);
    expect(store.state.revision).toBe(9);
    expect(store.tiles.value).toEqual({ theirs: { name: 'T' } });
  });
});

describe('widgets and their tile config', () => {
  const tiles = (): TilesDoc => tilesFromDoc(importedTiles());

  it('gives the service tile its name, link, description and status', async () => {
    const h = harness({ graph: { resources: { 'integration:i_o2asa67xbuwy4huh': { id: 'integration:i_o2asa67xbuwy4huh', kind: 'integration', health: 'ok' } }, relations: {} } });
    h.board.tiles.value = tiles();
    const a = h.mountIn(WidgetHost, { item: { id: 'service@homepage:servers/proxmoxpluto', type: 'service' }, size: 'wide' });
    const ld = h.mountIn(WidgetHost, { item: { id: 'service@homepage:archive-backup/linkding', type: 'service' }, size: 'wide' });
    await flushPromises();
    expect(a.find('.tn').text()).toBe('Pluto PVE (main opnsense)');
    expect(a.find('.tc').text()).toBe('pluto.lan:8006');
    expect(a.find('.svc-link').attributes('href')).toBe('https://pluto.lan:8006');
    expect(a.find('.svc-link').attributes('rel')).toBe('noopener noreferrer');
    expect(a.find('.tg').text()).toContain('reading');
    expect(a.find('.svc-origin').text()).toBe('Servers → ProxmoxPluto');
    // icons from other origins are never hot-linked (the CSP forbids it): a monogram instead
    expect(a.find('img').exists()).toBe(false);
    expect(ld.find('.svc-desc').text()).toBe('Shared bookmarks');
  });

  it('falls back to its id without a config', () => {
    const h = harness({ graph: { resources: {}, relations: {} } });
    const w = h.mountIn(WidgetHost, { item: { id: 'service@homepage:media/jellyfin', type: 'service' }, size: 'small' });
    expect(w.find('.tn').text()).toBe('jellyfin');
    expect(w.text()).toContain('Not set up yet');
  });

  it('applies name, link and chromeless to any widget, and binds its target from the config', async () => {
    const h = harness(docs(withIncidents));
    h.board.tiles.value = {
      'arr@app:sonarr': { name: 'TV shows', href: 'https://tv.lab.example', bind: { resource: 'app:radarr' } },
      calendar: { chromeless: true },
    };
    const arr = h.mountIn(WidgetHost, { item: { id: 'arr@app:sonarr', type: 'arr' }, size: 'small' });
    const cal = h.mountIn(WidgetHost, { item: { id: 'calendar', type: 'calendar' }, size: 'wide' });
    await flushPromises();
    expect(arr.find('.tn').text()).toBe('TV shows');
    expect(arr.find('a.mono').attributes('href')).toBe('https://tv.lab.example');
    expect(arr.find('.tile').attributes('aria-label')).toContain('TV shows');
    expect(arr.findComponent({ name: 'ArrWidget' }).props('target')).toBe('app:radarr');
    expect(cal.find('.th').exists()).toBe(false);
    expect(cal.find('.tile').attributes('aria-label')).toContain('Up next');
  });

  it('takes a service tile’s status from the check, the instance, or the resource it binds', () => {
    const g: GraphDoc = {
      resources: {
        'check:Plex': { id: 'check:Plex', kind: 'check', name: 'Plex', health: 'down', attrs: { 'health.reason': 'HTTP 502' } },
        'integration:i1': { id: 'integration:i1', kind: 'integration', health: 'repair' },
        'docker:i_d/container/immich_server': { id: 'docker:i_d/container/immich_server', kind: 'container', state: 'running', health: 'ok' },
      },
      relations: {},
    };
    expect(serviceStatus(g, { check: 'Plex', instance: 'i1' })).toEqual({ status: 'down', word: 'HTTP 502', via: 'check' });
    expect(serviceStatus(g, { instance: 'i1' })).toEqual({ status: 'repair', word: 'needs repair', via: 'integration' });
    expect(serviceStatus(g, { resource: 'docker:i_d/container/immich_server' })).toEqual({ status: 'ok', word: 'running', via: 'resource' });
    expect(serviceStatus(g, { instance: 'i2' })).toEqual({ status: 'unknown', word: 'no reading yet', via: 'none' });
    expect(serviceStatus(g, {})).toBeUndefined();
    expect(serviceStatus(g, undefined)).toBeUndefined();
  });
});
