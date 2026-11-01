/**
 * The Atlas (docs/plan/09-v0.3.md, "The Atlas"): the layout on the Demo
 * Home, the same for the same graph; folds, layers and aliases; the cause
 * path; the lens's list and canvas holding the same nodes; keyboard paths;
 * the route; the Hero tile; axe on both.
 */
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import AtlasLens from '../src/atlas/AtlasLens.vue';
import WidgetHost from '../src/widgets/WidgetHost.vue';
import { preloadWidgets } from '../src/widgets/registry';
import { buildAtlas, DEFAULT_LAYERS, LAYERS, lit, neighbour, type Atlas } from '../src/model/atlas';
import { buildCommands } from '../src/model/commands';
import { navigate, route } from '../src/lib/router';
import { setDisplayTimeZone } from '../src/lib/format';
import type { CauseHint, GraphDoc } from '../src/model/types';
import { axeViolations } from './axe';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

beforeAll(() => preloadWidgets());
beforeEach(() => {
  setDisplayTimeZone('UTC');
  navigate('/', { replace: true });
});
afterEach(() => {
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

const ALL = LAYERS.map((l) => l.id);
const rows = (a: Atlas): string[][] => {
  const by = new Map<number, string[]>();
  for (const id of a.order) {
    const n = a.nodes.get(id)!;
    by.set(n.y, [...(by.get(n.y) ?? []), n.label]);
  }
  return [...by].sort((p, q) => p[0] - q[0]).map(([, l]) => l);
};
const positions = (a: Atlas) => JSON.stringify([...a.nodes.values()].map((n) => [n.id, n.x, n.y]).sort());

/** g with its resources and relations in another key order. */
function shuffled(g: GraphDoc): GraphDoc {
  const rev = <T>(o: Record<string, T>) => Object.fromEntries(Object.entries(o).reverse());
  return { resources: rev(g.resources), relations: rev(g.relations) };
}

describe('the layout', () => {
  const g = docs(withIncidents).graph;

  it('puts the router on top, then hosts, guests, engines, containers, apps and routes, each under what it runs on', () => {
    const a = buildAtlas(g, { layers: DEFAULT_LAYERS });
    expect(rows(a)).toEqual([
      ['OPNsense'],
      ['WAN', 'egret', 'heron', 'magpie', 'owl', 'stork', 'wren'],
      ['rook', 'frigate', 'homeassistant', 'array', 'homework', 'studio', 'win-games', 'files', 'sandbox', 'adguard', 'opnsense', 'tailscale'],
      ['rook', 'magpie'],
      ['13 more containers', 'nextcloud', 'speedtest-tracker', '11 more containers', 'qbittorrent', 'radarr', 'sonarr'],
      ['Nextcloud', 'Speedtest Tracker', 'Frigate', 'Home Assistant', 'qBittorrent', 'Radarr', 'Sonarr', 'Cockpit', 'AdGuard Home'],
      ['cloud', 'speed', 'ha', 'torrents', 'radarr', 'sonarr', 'adguard'],
    ]);
    // A parent sits over its children.
    const sonarr = a.nodes.get('app:sonarr')!;
    expect(sonarr.parent).toBe('docker:magpie/sonarr');
    expect(a.nodes.get('docker:magpie/sonarr')!.x).toBe(sonarr.x);
  });

  it('is the same for the same graph, whatever order it arrives in', () => {
    for (const layers of [DEFAULT_LAYERS, ALL]) {
      expect(positions(buildAtlas(shuffled(g), { layers }))).toBe(positions(buildAtlas(g, { layers })));
    }
  });

  it('folds an engine’s healthy containers, never one in trouble, and opens the fold on request', () => {
    const a = buildAtlas(g, { layers: DEFAULT_LAYERS });
    const fold = a.nodes.get('group:docker:magpie/engine') ?? [...a.nodes.values()].find((n) => n.folded && n.parent?.includes('magpie'))!;
    expect(fold.folded!.length).toBeGreaterThan(0);
    for (const id of fold.folded!) expect(a.foldedInto.get(id)).toBe(fold.id);
    expect(a.nodes.has('docker:magpie/qbittorrent')).toBe(true); // attention: never folded
    const engine = fold.parent!;
    const open = buildAtlas(g, { layers: DEFAULT_LAYERS, expanded: new Set([engine]) });
    expect(open.nodes.has(fold.id)).toBe(false);
    expect(fold.folded!.filter((id) => g.resources[id]?.kind === 'container').every((id) => open.nodes.has(id))).toBe(true);
  });

  it('draws only the layers that are on', () => {
    const a = buildAtlas(g, { layers: ['compute'] });
    expect([...a.nodes.values()].some((n) => n.kind === 'route' || n.kind === 'disk')).toBe(false);
    expect(a.edges.every((e) => e.layer === 'compute')).toBe(true);
    const all = buildAtlas(g, { layers: ALL });
    expect([...all.nodes.values()].some((n) => n.kind === 'disk')).toBe(true);
    expect(new Set(all.edges.map((e) => e.layer))).toEqual(new Set(ALL));
    expect(buildAtlas(g, { layers: [] }).nodes.size).toBe(0);
  });

  it('draws one node per thing: an alias’s relations go to its primary', () => {
    const h: GraphDoc = {
      resources: {
        'host:a': { id: 'host:a', kind: 'host', name: 'a', health: 'ok' },
        'docker:ct': { id: 'docker:ct', kind: 'container', name: 'web', health: 'ok' },
        'unraid:ct': { id: 'unraid:ct', kind: 'container', name: 'web', health: 'down' },
        'docker:engine': { id: 'docker:engine', kind: 'engine', name: 'e', health: 'ok' },
      },
      relations: {
        a: { from: 'unraid:ct', type: 'same_as', to: 'docker:ct' },
        b: { from: 'unraid:ct', type: 'runs_on', to: 'host:a' },
        c: { from: 'docker:engine', type: 'runs_on', to: 'host:a' },
      },
    };
    const a = buildAtlas(h, { layers: ['compute'] });
    expect([...a.nodes.keys()].sort()).toEqual(['docker:ct', 'docker:engine', 'host:a']);
    expect(a.nodes.get('docker:ct')!.health).toBe('down'); // the worst of its members
    expect(a.edges.map((e) => e.key)).toContain('docker:ct runs_on host:a');
  });

  it('lights a cause’s path, a folded end lighting its group', () => {
    const a = buildAtlas(g, { layers: DEFAULT_LAYERS });
    const folded = [...a.foldedInto.keys()][0]!;
    const l = lit(a, 'app:sonarr', [
      { from: 'app:sonarr', type: 'depends_on', to: 'app:qbittorrent' },
      { from: 'app:qbittorrent', type: 'runs_on', to: 'docker:magpie/qbittorrent' },
      { from: 'docker:magpie/qbittorrent', type: 'depends_on', to: folded },
    ]);
    expect([...l.nodes].sort()).toEqual(['app:qbittorrent', 'app:sonarr', 'docker:magpie/qbittorrent', a.foldedInto.get(folded)!].sort());
    expect(l.edges.has('app:sonarr depends_on app:qbittorrent')).toBe(true);
  });

  it('moves along the map with the arrows', () => {
    const a = buildAtlas(g, { layers: DEFAULT_LAYERS });
    expect(neighbour(a, 'app:sonarr', 'up')).toBe('docker:magpie/sonarr');
    expect(neighbour(a, 'docker:magpie/sonarr', 'down')).toBe('app:sonarr');
    expect(neighbour(a, 'app:sonarr', 'left')).toBe('app:radarr');
    expect(neighbour(a, 'net:opnsense', 'up')).toBeUndefined();
  });
});

const QBIT = "qBittorrent's container was OOM-killed at 14:03.";
const hint: CauseHint = {
  text: `${QBIT} Sonarr depends on qBittorrent.`,
  resource: 'docker:magpie/qbittorrent',
  rule: 'oom_killed',
  args: { what: QBIT },
  path: [
    { from: 'app:sonarr', type: 'depends_on', to: 'app:qbittorrent' },
    { from: 'app:qbittorrent', type: 'runs_on', to: 'docker:magpie/qbittorrent' },
  ],
};

async function lens(focus: string) {
  navigate(`/?atlas=${encodeURIComponent(focus)}`);
  const h = harness({ ...docs(withIncidents), causes: { causes: { 'app:sonarr': [hint] } } });
  await flushPromises();
  h.mountIn(AtlasLens);
  await flushPromises();
  const dlg = () => document.querySelector<HTMLElement>('.atlas-lens')!;
  const canvasIds = () => [...dlg().querySelectorAll<HTMLElement>('.atlas-canvas [data-id]')].map((e) => e.dataset['id']!);
  const listIds = () => [...dlg().querySelectorAll<HTMLElement>('.atlas-list [data-id]')].map((e) => e.dataset['id']!);
  return { ...h, dlg, canvasIds, listIds };
}

describe('the lens', () => {
  it('holds the same nodes in the list as on the canvas, whatever the layers', async () => {
    const l = await lens('1');
    expect(l.canvasIds().length).toBeGreaterThan(30);
    expect(new Set(l.listIds())).toEqual(new Set(l.canvasIds()));
    // Storage on: more nodes, still the same in both.
    const storage = [...l.dlg().querySelectorAll<HTMLInputElement>('.atlas-layers input')][2]!;
    storage.click();
    await nextTick();
    expect(new Set(l.listIds())).toEqual(new Set(l.canvasIds()));
    expect(l.canvasIds().some((id) => id.includes('disk'))).toBe(true);
    // Opening a fold from the list opens it on the canvas too.
    const fold = l.dlg().querySelector<HTMLButtonElement>('.atlas-list [data-id^="group:"]')!;
    fold.click();
    await nextTick();
    expect(l.canvasIds()).not.toContain(fold.dataset['id']);
    expect(new Set(l.listIds())).toEqual(new Set(l.canvasIds()));
  });

  it('opens on the route’s resource with its cause path lit, and says why', async () => {
    const l = await lens('app:sonarr');
    const node = (id: string) => l.dlg().querySelector<HTMLElement>(`.atlas-canvas [data-id="${id}"]`)!;
    expect(node('app:sonarr').getAttribute('aria-current')).toBe('true');
    for (const id of ['app:sonarr', 'app:qbittorrent', 'docker:magpie/qbittorrent']) expect(node(id).classList).toContain('lit');
    expect(node('app:radarr').classList).toContain('dim');
    expect(l.dlg().querySelector('.atlas-bar')!.textContent).toContain(`Because ${QBIT.charAt(0).toLowerCase()}${QBIT.slice(1)}`);
    expect(l.dlg().querySelector('.atlas-list [data-id="app:sonarr"]')!.textContent).toContain(`because ${QBIT.charAt(0).toLowerCase()}`);
    expect(await axeViolations(l.dlg())).toEqual([]);
  });

  it('moves the selection with the arrows, and the list follows', async () => {
    const l = await lens('app:sonarr');
    const sonarr = l.dlg().querySelector<HTMLElement>('.atlas-canvas [data-id="app:sonarr"]')!;
    expect(sonarr.tabIndex).toBe(0);
    sonarr.focus();
    sonarr.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }));
    await nextTick();
    await nextTick();
    expect(document.activeElement?.getAttribute('data-id')).toBe('docker:magpie/sonarr');
    expect(l.dlg().querySelector('.atlas-list [aria-current="true"]')!.getAttribute('data-id')).toBe('docker:magpie/sonarr');
    expect(l.dlg().querySelector('.atlas-bar')!.textContent).toContain('sonarr');
  });

  it('closes to the page it was opened over', async () => {
    const l = await lens('1');
    l.dlg().querySelector<HTMLButtonElement>('.dlg-head .x')!.click();
    await nextTick();
    expect(route.value.query['atlas']).toBeUndefined();
    expect(l.ui.atlas.value).toBeUndefined();
  });
});

describe('getting there', () => {
  it('is a ⌘K command, and a sheet over the lens keeps it', () => {
    const opened: (string | undefined)[] = [];
    const cmds = buildCommands({
      do: {
        openInbox: () => {}, toggleEdit: () => {}, openBoard: () => {}, reveal: () => {}, openSettings: () => {},
        addIntegration: () => {}, setTheme: () => {}, signOut: () => {}, openAtlas: (f) => opened.push(f),
      },
    });
    cmds.find((c) => c.id === 'atlas')!.run();
    expect(opened).toEqual([undefined]);
    const h = harness(docs(withIncidents));
    h.ui.openAtlas('app:sonarr');
    expect(route.value.query).toEqual({ atlas: 'app:sonarr' });
    h.ui.openSheet('docker:magpie/sonarr');
    expect(route.value.query).toEqual({ atlas: 'app:sonarr', r: 'docker:magpie/sonarr' });
    h.ui.closeSheet();
    expect(route.value.query).toEqual({ atlas: 'app:sonarr' });
  });
});

describe('the Hero tile', () => {
  it('draws the map with its saved layers, and a node opens the lens on it', async () => {
    const h = harness(docs(withIncidents));
    await flushPromises();
    const w = h.mountIn(WidgetHost, { item: { id: 'atlas', type: 'atlas' }, size: 'hero' });
    await flushPromises();
    const tile = w.find('article.tile');
    expect(tile.findAll('.atlas-node').length).toBeGreaterThan(30);
    expect(tile.find('.atlas-node.k-disk').exists()).toBe(false); // storage is off by default
    await tile.find('.atlas-node[data-id="app:sonarr"]').trigger('click');
    expect(route.value.query).toEqual({ atlas: 'app:sonarr' });
    expect(await axeViolations(tile.element)).toEqual([]);
  });
});

describe('the chunk', () => {
  it('is lazy: only the Atlas and its tile import it, and they are loaded on demand', () => {
    const src = join(__dirname, '../src');
    const files = (readdirSync(src, { recursive: true }) as string[]).filter((f) => /\.(ts|vue)$/.test(f));
    const statics = files.filter((f) => {
      if (f.startsWith('atlas/') || f === 'widgets/AtlasWidget.vue') return false;
      return /^import\s[^;]*from\s+'[^']*(model\/atlas|atlas\/Atlas\w*\.vue|AtlasWidget\.vue)'/m.test(readFileSync(join(src, f), 'utf8'));
    });
    expect(statics).toEqual([]);
    expect(readFileSync(join(src, 'views/FrontPage.vue'), 'utf8')).toContain("import('../atlas/AtlasLens.vue')");
    expect(readFileSync(join(src, 'widgets/registry.ts'), 'utf8')).toContain("lazy(() => import('./AtlasWidget.vue'))");
  });
});
