import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import { createBoard, toDocument, type Board } from '@chadash/layout';
import Briefing from '../src/components/Briefing.vue';
import InboxSheet from '../src/components/InboxSheet.vue';
import { addLabelServices, labelServices, placementSentence, slug } from '../src/model/discovery';
import { setDisplayTimeZone } from '../src/lib/format';
import type { Resource } from '../src/model/types';
import type { TilesDoc } from '../src/stores/tiles';
import { docs, withIncidents } from './fixtures';
import { fakeFetch, type Handler } from './fake-fetch';
import { harness } from './harness';

beforeEach(() => setDisplayTimeZone('UTC'));
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

const container = (id: string, labels: Record<string, string>): Resource => ({
  id,
  kind: 'container',
  name: id.split('/').pop()!,
  source: `integration:${id.split(':')[1]!.split('/')[0]}`,
  state: 'running',
  health: 'ok',
  labels,
});

/** The Demo Home plus containers as the Docker integration publishes them, labels and all. */
function labelled(): ReturnType<typeof docs> {
  const d = docs(withIncidents);
  for (const r of [
    container('docker:i_d/container/immich_server', {
      'homepage.group': 'Apps',
      'homepage.name': 'Immich',
      'homepage.href': 'https://photos.lab.example',
      'homepage.icon': 'immich.png',
      'homepage.weight': '2',
      'com.docker.compose.project': 'immich',
    }),
    container('docker:i_d/container/mealie', { 'homepage.group': 'apps', 'homepage.name': 'Mealie', 'homepage.weight': '1', 'homepage.description': 'Recipes' }),
    container('docker:i_d/container/proxmox-backup-server', { 'homepage.group': 'Backup', 'homepage.name': 'Proxmox Backup Server', 'homepage.href': 'ftp://pbs' }),
    // The same service seen by a second source: offered once.
    container('docker:i_u/container/mealie', { 'homepage.group': 'Apps', 'homepage.name': 'Mealie' }),
    // Homepage needs both labels.
    container('docker:i_d/container/half', { 'homepage.group': 'Apps' }),
    // The import already brought it from services.yaml.
    container('docker:i_d/container/linkding', { 'homepage.group': 'Archive/Backup', 'homepage.name': 'Linkding' }),
  ])
    d.graph.resources[r.id] = r;
  return d;
}

function importedBoard(): { board: Board; tiles: TilesDoc } {
  const board = createBoard({
    sections: [
      { id: 'media', title: 'Media', span: 4, items: [{ id: 'plex', type: 'plex', size: 'wide', supportedSizes: ['small', 'wide'] }] },
      { id: 'apps', title: 'Apps', span: 4, items: [] },
      {
        id: 'archive-backup',
        title: 'Archive/Backup',
        span: 4,
        items: [{ id: 'service@homepage:archive-backup/linkding', type: 'service', size: 'small', supportedSizes: ['small', 'wide'] }],
      },
    ],
  });
  return { board, tiles: { 'service@homepage:archive-backup/linkding': { name: 'Linkding', bind: { check: 'Linkding' } } } };
}

describe('label discovery', () => {
  it('suggests labelled containers for the section their group names', () => {
    const { board, tiles } = importedBoard();
    const s = labelServices(labelled().graph, board, tiles, new Set());
    expect(s.map((x) => [x.resource, x.sectionId, x.newSection])).toEqual([
      ['docker:i_d/container/mealie', 'apps', false],
      ['docker:i_d/container/immich_server', 'apps', false],
      ['docker:i_d/container/proxmox-backup-server', 'backup', true],
    ]);
    expect(s[1]).toMatchObject({ name: 'Immich', group: 'Apps', href: 'https://photos.lab.example', icon: 'immich.png', sectionTitle: 'Apps' });
    expect(s[2]!.href).toBeUndefined(); // http(s) links only
    expect(placementSentence(s)).toBe('Mealie and Immich in Apps; Proxmox Backup Server in a new Backup section.');
    expect(slug('Archive/Backup')).toBe('archive-backup');
  });

  it('leaves out what was dismissed and what the board already shows', () => {
    const { board, tiles } = importedBoard();
    const g = labelled().graph;
    // Mealie is one service seen by two sources: dismissing either one dismisses it
    expect(labelServices(g, board, tiles, new Set()).find((x) => x.name === 'Mealie')!.duplicates).toEqual(['docker:i_u/container/mealie']);
    expect(labelServices(g, board, tiles, new Set(['docker:i_u/container/mealie'])).map((x) => x.name)).toEqual(['Immich', 'Proxmox Backup Server']);
    const added = addLabelServices({ board, tiles }, labelServices(g, board, tiles, new Set()));
    expect(labelServices(g, added.board, added.tiles, new Set())).toEqual([]);
    // a tile bound to the container by hand counts too
    const bound: TilesDoc = { ...tiles, plex: { bind: { resource: 'docker:i_d/container/immich_server' } } };
    expect(labelServices(g, board, bound, new Set()).map((x) => x.name)).toEqual(['Mealie', 'Proxmox Backup Server']);
    expect(labelServices(undefined, board, tiles, new Set())).toEqual([]);
  });

  it('adds them as service tiles: into the matching section, or a new one at the end', () => {
    const { board, tiles } = importedBoard();
    const s = labelServices(labelled().graph, board, tiles, new Set());
    const next = addLabelServices({ board, tiles }, s);
    expect(next.added).toHaveLength(3);
    expect(next.board.sections.map((x) => [x.id, x.title, x.span, x.items.map((i) => i.id)])).toEqual([
      ['media', 'Media', 4, ['plex']],
      ['apps', 'Apps', 4, ['service@docker:i_d/container/mealie', 'service@docker:i_d/container/immich_server']],
      ['archive-backup', 'Archive/Backup', 4, ['service@homepage:archive-backup/linkding']],
      ['backup', 'Backup', 4, ['service@docker:i_d/container/proxmox-backup-server']],
    ]);
    expect(next.board.sections[1]!.items.every((i) => i.type === 'service' && i.size === 'small')).toBe(true);
    expect(next.tiles['service@docker:i_d/container/immich_server']).toEqual({
      name: 'Immich',
      icon: 'immich.png',
      href: 'https://photos.lab.example',
      bind: { resource: 'docker:i_d/container/immich_server' },
      origin: { source: 'labels', group: 'Apps', entry: 'Immich' },
    });
    expect(next.tiles['service@docker:i_d/container/mealie']!.description).toBe('Recipes');
    expect(next.tiles['service@homepage:archive-backup/linkding']).toEqual(tiles['service@homepage:archive-backup/linkding']);
  });
});

describe('the discovery Inbox item', () => {
  /** A hub with the imported board, saving whatever is PUT. */
  function hub(over: Record<string, Handler | { status: number; body?: unknown }> = {}) {
    const { board, tiles } = importedBoard();
    const saved: { layout: unknown; tiles: unknown; revision: number } = { layout: toDocument(board), tiles, revision: 4 };
    let dismissed: string[] = [];
    const f = fakeFetch({
      'GET /api/boards': { status: 200, body: { boards: [{ id: 'b1', name: 'Alpaca Homepage' }] } },
      'GET /api/boards/b1/layout': () => ({ status: 200, body: { revision: saved.revision, format_version: 1, layout: saved.layout, tiles: saved.tiles } }),
      'PUT /api/boards/b1/layout': (body) => {
        const b = body as { layout: unknown; tiles?: unknown; revision: number };
        if (b.revision !== saved.revision) return { status: 409, body: { error: { code: 'conflict', message: 'stale' }, revision: saved.revision } };
        saved.layout = b.layout;
        if (b.tiles !== undefined) saved.tiles = b.tiles;
        saved.revision++;
        return { status: 200, body: { revision: saved.revision } };
      },
      'GET /api/boards/b1/discovery': () => ({ status: 200, body: { dismissed } }),
      'POST /api/boards/b1/discovery/dismiss': (body) => {
        dismissed = [...new Set([...dismissed, ...(body as { resources: string[] }).resources])];
        return { status: 200, body: { dismissed } };
      },
      ...over,
    });
    return { f, saved, dismissedOnHub: () => dismissed };
  }

  async function open(data = labelled()) {
    const h = harness(data);
    await h.board.load();
    await flushPromises();
    h.mountIn(InboxSheet);
    const brief = h.mountIn(Briefing);
    h.ui.openInbox();
    await flushPromises();
    await nextTick();
    return { ...h, brief, panel: () => document.body.querySelector<HTMLElement>('.inbox')! };
  }

  it('asks before adding, and adds them all in one save', async () => {
    const { f, saved } = hub();
    const { panel, brief, ui, board, discovery } = await open();
    const item = panel().querySelector('.disc')!;
    expect(item.querySelector('.ib-title')!.textContent).toBe('3 services found by their labels. Add them?');
    expect(item.querySelector('.m')!.textContent).toBe('Mealie and Immich in Apps; Proxmox Backup Server in a new Backup section.');
    expect([...item.querySelectorAll('.disc-list li')].map((li) => li.textContent)).toEqual(['MealieApps', 'ImmichApps', 'Proxmox Backup ServerBackup (new section)']);
    // the question counts in the Inbox badge until it's answered
    const badge = Number(brief.find('.pill .badge').text());
    expect(badge).toBeGreaterThan(0);
    // nothing was added on its own
    expect(f.calls.filter((c) => c.method === 'PUT')).toHaveLength(0);

    item.querySelector<HTMLButtonElement>('.btn.primary')!.click();
    await flushPromises();
    const puts = f.calls.filter((c) => c.method === 'PUT');
    expect(puts).toHaveLength(1);
    expect(Object.keys(saved.tiles as object)).toEqual(
      expect.arrayContaining(['service@docker:i_d/container/mealie', 'service@docker:i_d/container/immich_server', 'service@docker:i_d/container/proxmox-backup-server']),
    );
    expect(board.board.value!.sections.map((s) => s.id)).toEqual(['media', 'apps', 'archive-backup', 'backup']);
    expect(discovery.suggestions.value).toEqual([]);
    expect(ui.state.toasts.at(-1)!.text).toBe('Added 3 services to the board. Edit the layout to move or remove them.');
    await nextTick();
    expect(panel().querySelector('.disc')).toBeNull();
    expect(Number(brief.find('.pill .badge').text())).toBe(badge - 1);
  });

  it('remembers a dismissal on the hub, so it isn’t offered again', async () => {
    const { f, dismissedOnHub } = hub();
    const first = await open();
    first.panel().querySelector<HTMLButtonElement>('.disc .btn.ghost')!.click();
    await flushPromises();
    expect(f.last('POST', '/api/boards/b1/discovery/dismiss')!.body).toEqual({
      resources: ['docker:i_d/container/mealie', 'docker:i_u/container/mealie', 'docker:i_d/container/immich_server', 'docker:i_d/container/proxmox-backup-server'],
    });
    expect(first.panel().querySelector('.disc')).toBeNull();
    expect(f.calls.filter((c) => c.method === 'PUT')).toHaveLength(0);
    document.body.innerHTML = '';

    // Another device, later, with one more labelled container: only that one is new.
    expect(dismissedOnHub()).toHaveLength(4);
    const data = labelled();
    data.graph.resources['docker:i_d/container/paperless'] = container('docker:i_d/container/paperless', { 'homepage.group': 'Apps', 'homepage.name': 'Paperless' });
    const again = await open(data);
    expect(again.discovery.suggestions.value.map((s) => s.resource)).toEqual(['docker:i_d/container/paperless']);
    expect(again.panel().querySelector('.disc .ib-title')!.textContent).toBe('One service found by its labels. Add it?');
  });

  it('costs no request when nothing has labels, and suggests nothing until the dismissals arrive', async () => {
    let release: () => void = () => undefined;
    const { f } = hub({
      'GET /api/boards/b1/discovery': () => new Promise((resolve) => (release = () => resolve({ status: 200, body: { dismissed: [] } }))),
    });
    const plain = harness(docs(withIncidents));
    await plain.board.load();
    await flushPromises();
    expect(plain.discovery.suggestions.value).toEqual([]);
    expect(f.calls.some((c) => c.path === '/api/boards/b1/discovery')).toBe(false);

    const h = harness(labelled());
    await h.board.load();
    await flushPromises();
    expect(f.calls.filter((c) => c.path === '/api/boards/b1/discovery')).toHaveLength(1);
    expect(h.discovery.suggestions.value).toEqual([]); // still waiting
    release();
    await flushPromises();
    expect(h.discovery.suggestions.value).toHaveLength(3);
  });

  it('suggests nothing while the hub is unreachable', async () => {
    hub();
    const h = harness(labelled());
    await h.board.load();
    await flushPromises();
    expect(h.discovery.suggestions.value).toHaveLength(3);
    h.board.state.lastKnown = true;
    expect(h.discovery.suggestions.value).toEqual([]);
  });
});
