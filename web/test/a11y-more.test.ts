/**
 * WCAG 2.2 A/AA (axe-core) over the views added late in wave 6: the Inbox's
 * Discovered and Suggestions groups, the widget picker and the tile editor in
 * edit mode, the first-run wizard's steps, and the Backup pane. Contrast is
 * checked on the tokens (see test/axe.ts).
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, type VueWrapper } from '@vue/test-utils';
import { defineComponent, h, nextTick, reactive, shallowRef } from 'vue';
import { createBoard, toDocument, type Board } from '@chadash/layout';
import { LayoutBoard } from '@chadash/layout-vue';
import BoardSurface from '../src/board/BoardSurface.vue';
import Briefing from '../src/components/Briefing.vue';
import InboxSheet from '../src/components/InboxSheet.vue';
import BackupPane from '../src/settings/BackupPane.vue';
import OnboardingWizard from '../src/onboarding/OnboardingWizard.vue';
import { cache, type Candidate } from '../src/onboarding/discover';
import { openWizard, resetOnboarding } from '../src/onboarding/state';
import { setDisplayTimeZone } from '../src/lib/format';
import { syncRoute } from '../src/lib/router';
import { defaultBoard } from '../src/model/defaultLayout';
import type { Incident, Resource } from '../src/model/types';
import { provideBoard, type BoardStore } from '../src/stores/board';
import { resetIntegrations } from '../src/stores/integrations';
import type { TilesDoc } from '../src/stores/tiles';
import { axeViolations } from './axe';
import { fakeFetch } from './fake-fetch';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

const mounted: { unmount: () => void }[] = [];

async function settle(): Promise<void> {
  await vi.dynamicImportSettled();
  await flushPromises();
  await nextTick();
}

beforeEach(() => {
  setDisplayTimeZone('UTC');
  history.replaceState(null, '', '/');
  syncRoute();
  resetIntegrations();
  resetOnboarding();
  delete cache.sidecar;
});
afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('the Inbox’s Discovered and Suggestions groups', () => {
  const container = (id: string, labels: Record<string, string>): Resource => ({ id, kind: 'container', name: id.split('/').pop()!, source: 'integration:i_d', state: 'running', health: 'ok', labels });
  const suggestion: Incident = {
    id: 'correlate.0123456789abcdef',
    scenario: 'merge-suggestion',
    class: 'suggestion',
    severity: 'info',
    status: 'open',
    phase: 'open',
    title: 'These look like the same thing. Merge?',
    summary: 'Sonarr and the container sonarr have matching names.',
    target: 'app:sonarr',
    opened_at: '2026-10-03T14:00:00Z',
    facts: { from: 'app:sonarr', to: 'docker:i_d/container/sonarr', relation: 'runs_on', rule: 'name', confidence: 'weak' },
    causes: [],
    blast_radius: [],
    suggested: { id: 'correlate.confirm', label: 'Merge', tier: 'T0', target: 'app:sonarr' },
    alternatives: [{ id: 'correlate.reject', label: 'Keep separate', tier: 'T0', target: 'app:sonarr' }],
    resolves_when: 'Resolves when you merge them or keep them separate.',
  };

  it('passes axe with both groups showing', async () => {
    const d = docs(withIncidents);
    for (const r of [
      container('docker:i_d/container/mealie', { 'homepage.group': 'Apps', 'homepage.name': 'Mealie', 'homepage.description': 'Recipes' }),
      container('docker:i_d/container/immich_server', { 'homepage.group': 'Apps', 'homepage.name': 'Immich', 'homepage.href': 'https://photos.example.com' }),
    ])
      d.graph.resources[r.id] = r;
    d.inbox.incidents[suggestion.id] = structuredClone(suggestion);
    const board = createBoard({ sections: [{ id: 'apps', title: 'Apps', span: 4, items: [{ id: 'plex', type: 'plex', size: 'wide', supportedSizes: ['small', 'wide'] }] }] });
    fakeFetch({
      'GET /api/boards': { status: 200, body: { boards: [{ id: 'b1', name: 'Home' }] } },
      'GET /api/boards/b1/layout': { status: 200, body: { revision: 2, format_version: 1, layout: toDocument(board), tiles: {} } },
      'GET /api/boards/b1/discovery': { status: 200, body: { dismissed: [] } },
    });
    const hs = harness(d, { demo: true });
    await hs.board.load();
    await settle();
    mounted.push(hs.mountIn(InboxSheet), hs.mountIn(Briefing));
    hs.ui.openInbox();
    await settle();
    expect(document.querySelector('.inbox .disc')).not.toBeNull();
    expect([...document.querySelectorAll('.ib-grp')].map((g) => g.textContent)).toEqual(expect.arrayContaining(['Discovered', 'Suggestions · 1']));
    expect(await axeViolations()).toEqual([]);
    hs.live.stop();
  });
});

describe('edit mode: the widget picker and the tile editor', () => {
  /** A board store that keeps what's saved, like the real one after a 200. */
  function fakeStore(initial: Board, tiles: TilesDoc = {}) {
    const board = shallowRef<Board | undefined>(initial);
    const t = shallowRef<TilesDoc>(tiles);
    return {
      state: reactive({ unsaved: false, boardId: 'b1', status: 'ready' }),
      board,
      tiles: t,
      load: vi.fn(),
      save: vi.fn(async (b: Board, nt?: TilesDoc) => {
        board.value = b;
        if (nt !== undefined) t.value = nt;
        return true;
      }),
    } as unknown as BoardStore;
  }

  async function editing(): Promise<VueWrapper> {
    const store = fakeStore(defaultBoard(docs(withIncidents).graph, Date.parse(withIncidents.now)));
    const hs = harness(docs(withIncidents));
    const Page = defineComponent({
      setup() {
        provideBoard(store);
        return () => h('main', { class: 'page', 'aria-label': 'Front page' }, [h(BoardSurface, { board: store.board.value!, breakpoint: 'desktop' })]);
      },
    });
    const w = hs.mountIn(Page);
    mounted.push(w, { unmount: () => hs.live.stop() });
    hs.ui.state.editing = true;
    await settle();
    return w;
  }

  it('the picker', async () => {
    const w = await editing();
    await w.find('.sh-add').trigger('click');
    await settle();
    expect(document.querySelector('.pk-item')).not.toBeNull();
    expect(await axeViolations()).toEqual([]);
  });

  it('the tile editor', async () => {
    const w = await editing();
    w.findComponent(LayoutBoard).vm.$emit('select', 'plex');
    await settle();
    await w.findAll('button').find((b) => b.text() === 'Edit')!.trigger('click');
    await settle();
    expect(document.querySelector('dialog.editor')?.hasAttribute('open')).toBe(true);
    expect(await axeViolations()).toEqual([]);
    // With a link it refuses, its error showing.
    const url = document.querySelector<HTMLInputElement>('dialog.editor input[type="url"]')!;
    url.value = 'ftp://nope';
    url.dispatchEvent(new Event('input'));
    await settle();
    expect(await axeViolations()).toEqual([]);
  });

  it('the dismissed services, and the widget picker ⌘K opens', async () => {
    fakeFetch({ 'GET /api/boards/b1/discovery': { status: 200, body: { dismissed: ['docker:magpie/mealie', 'docker:gone/x'] } } });
    const store = fakeStore(defaultBoard(docs(withIncidents).graph, Date.parse(withIncidents.now)));
    const hs = harness(docs(withIncidents));
    hs.board.state.boardId = 'b1';
    hs.board.state.status = 'ready';
    const Page = defineComponent({
      setup() {
        provideBoard(store);
        return () => h('main', { class: 'page', 'aria-label': 'Front page' }, [h(BoardSurface, { board: store.board.value!, breakpoint: 'desktop' })]);
      },
    });
    const w = hs.mountIn(Page);
    mounted.push(w, { unmount: () => hs.live.stop() });
    hs.ui.state.editing = true;
    await settle();
    await settle();
    await w.findAll('.edit-bar button').find((b) => b.text().startsWith('Dismissed'))!.trigger('click');
    await settle();
    expect(document.querySelectorAll('dialog.dismissed li')).toHaveLength(2);
    expect(await axeViolations()).toEqual([]);
    await w.findAll('dialog.dismissed button').find((b) => b.text() === 'Done')!.trigger('click');
    await settle();
    hs.ui.state.addingWidget = true;
    await settle();
    await settle();
    expect(document.querySelector('dialog.picker')?.hasAttribute('open')).toBe(true);
    expect(await axeViolations()).toEqual([]);
  });

  it('the calendar’s tile editor, with its unmonitored option', async () => {
    const w = await editing();
    w.findComponent(LayoutBoard).vm.$emit('select', 'calendar');
    await settle();
    await w.findAll('button').find((b) => b.text() === 'Edit')!.trigger('click');
    await settle();
    expect(document.querySelector('dialog.editor input[type="checkbox"]')).not.toBeNull();
    expect(await axeViolations()).toEqual([]);
  });
});

describe('the first-run wizard', () => {
  const SIDECAR: Candidate = {
    integration: 'docker',
    name: 'Docker',
    config: { endpoint: 'tcp://socket-proxy:2375' },
    source: 'sidecar',
    target: 'tcp://socket-proxy:2375',
    ready: true,
    version: '28.5.1',
    grants: [{ name: 'containers', access: 'read' }],
    warnings: [],
  };
  const RECIPE = (access: string) => ({ title: 'T', summary: 'S', access, params: [], steps: [{ title: 'Step', blocks: [{ kind: 'text', text: 'x' }] }], notes: [], fills: {}, grants: [] });

  function hub() {
    fakeFetch({
      'GET /api/onboarding': { status: 200, body: { status: 'new', step: '', show: true, sidecar: SIDECAR.target, updated_at: null } },
      'PUT /api/onboarding': (body) => ({ status: 200, body: { ...(body as object), show: false } }),
      'GET /api/integrations/catalog': {
        status: 200,
        body: [
          { id: 'docker', name: 'Docker', config_schema: { type: 'object', properties: {} }, recipe: RECIPE('read-only') },
          { id: 'proxmox', name: 'Proxmox VE', config_schema: { type: 'object', properties: {} }, recipe: RECIPE('read-only') },
          { id: 'sonarr', name: 'Sonarr', config_schema: { type: 'object', properties: {} }, recipe: RECIPE('full') },
        ],
      },
      'GET /api/integrations/instances': { status: 200, body: [{ id: 'i_d', integration: 'docker', name: 'Docker', status: 'ok', warnings: [] }] },
      'POST /api/onboarding/discover': { status: 200, body: { found: [SIDECAR], checked: [{ target: SIDECAR.target, source: 'sidecar', found: 1 }, { target: 'gone.example.com', source: 'host', found: 0 }] } },
    });
  }

  for (const step of ['start', 'discover', 'add'] as const) {
    it(step, async () => {
      hub();
      const hs = harness();
      openWizard(step);
      mounted.push(hs.mountIn(OnboardingWizard), { unmount: () => hs.live.stop() });
      await settle();
      expect(document.getElementById('ob-h')).not.toBeNull();
      expect(await axeViolations()).toEqual([]);
    });
  }
});

describe('the Backup pane', () => {
  const PLAN = {
    plan: {
      format: 'chadash/v1alpha1',
      changes: [
        { kind: 'board', id: 'b_home', name: 'Home', action: 'unchanged' },
        { kind: 'tiles', id: 'b_home', name: 'Home', action: 'update', fields: [{ path: '/plex/name', op: 'replace', from: 'Plex', to: 'Films' }] },
        {
          kind: 'instance',
          id: 'i_new',
          name: 'Proxy',
          integration: 'traefik',
          action: 'create',
          fields: [{ path: '/config/url', op: 'add', to: 'https://proxy.example.com' }],
          secrets: [{ field: 'password', secret: 'instance/i_new/password', action: 'needed', in_bundle: true }],
        },
      ],
      secrets_needed: [{ instance: 'i_new', name: 'Proxy', integration: 'traefik', field: 'password', secret: 'instance/i_new/password', in_bundle: true }],
      bundle: { present: true, unlocked: false, secrets: 1 },
      summary: { create: 1, update: 1, unchanged: 1, stale: 0, secrets_restored: 0, secrets_needed: 1 },
      warnings: [{ code: 'unknown_integration', kind: 'instance', id: 'i_x', message: 'Legacy uses an integration this hub doesn’t include.' }],
    },
  };

  it('exporting, and previewing a Home file', async () => {
    fakeFetch({ 'POST /api/import/chadash?dry_run=1': { status: 200, body: PLAN }, 'GET /api/integrations/instances': { status: 200, body: [] } });
    const hs = harness();
    const w = hs.mountIn(BackupPane);
    mounted.push(w, { unmount: () => hs.live.stop() });
    await settle();
    expect(await axeViolations()).toEqual([]);

    await w.find('#bk-yaml').setValue('apiVersion: chadash/v1alpha1\nkind: Home\nboards: []\nsecrets_bundle:\n  cipher: xchacha20poly1305\n');
    await w.findAll('button').find((b) => b.text().startsWith('Preview import'))!.trigger('click');
    await settle();
    expect(document.querySelector('.bk-preview')).not.toBeNull();
    expect(await axeViolations()).toEqual([]);
  });
});
