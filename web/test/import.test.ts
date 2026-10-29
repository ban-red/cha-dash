import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import goldenRaw from '../../testdata/importers/homepage/dogfood/expected-plan.json?raw';
import ImportFlow from '../src/import/ImportFlow.vue';
import HistoryList from '../src/import/HistoryList.vue';
import { choicesBody, counts, emptyChoices, included, reviewOf, setIncluded } from '../src/import/model';
import { isFresh } from '../src/import/fresh';
import { route, syncRoute } from '../src/lib/router';
import { resetIntegrations } from '../src/stores/integrations';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

/** The importer's golden dry run for the dogfood home (internal/importer/homepage). */
const golden = JSON.parse(goldenRaw) as { plan: Record<string, unknown>; hints: unknown[]; unmapped: unknown[]; plan_hash: string };
const SERVICES = '- Media:\n    - Plex:\n        href: https://plex.home.arpa\n';
const HA = 'instance:i_zsqwalwibofrelao';

beforeEach(() => {
  history.replaceState(null, '', '/settings/import');
  syncRoute();
  resetIntegrations();
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('the Review model', () => {
  it('reads rows from the dry run: connections with their change, tiles with what they need', () => {
    const r = reviewOf(golden);
    expect(r.connections).toHaveLength(23);
    expect(r.tiles).toHaveLength(22);
    expect(r.planHash).toMatch(/^sha256:/);
    const ha = r.connections.find((x) => x.object === HA)!;
    expect(ha).toMatchObject({ name: 'HomeAssistant', integration: 'homeassistant', change: 'create' });
    expect(r.tiles.find((t) => t.needs.includes(HA))?.name).toBe('HomeAssistant');
  });

  it('leaving a connection out leaves out the tiles that need only it; ticking a tile ticks its connection', () => {
    const r = reviewOf(golden);
    const c = emptyChoices();
    const all = counts(r, c);
    expect(setIncluded(r, c, HA, false)).toEqual(['HomeAssistant']);
    const tile = r.tiles.find((t) => t.needs.includes(HA))!;
    expect(included(r, c, tile.object)).toBe(false);
    expect(counts(r, c).tiles).toBe(all.tiles - 1);
    expect(choicesBody(c)).toEqual({ skip: [HA] });
    setIncluded(r, c, tile.object, true);
    expect(included(r, c, HA)).toBe(true);
    expect(choicesBody(c)).toEqual({});
  });

  it('sends typed keys only for connections that are in, and targets other than new', () => {
    const c = emptyChoices();
    c.secrets.set('i_a', { api_key: 'FAKE-key', other: '' });
    c.secrets.set('i_b', { token: 'FAKE-2' });
    c.skip.add('instance:i_b');
    c.target = 'board:b1';
    expect(choicesBody(c)).toEqual({ skip: ['instance:i_b'], target: 'board:b1', secrets: { i_a: { api_key: 'FAKE-key' } } });
  });
});

describe('Settings → Bring in → Import', () => {
  function mountFlow(over: Record<string, unknown> = {}) {
    const f = fakeFetch({
      'POST /api/import/homepage?dry_run=1': { status: 200, body: golden },
      'POST /api/import/homepage': { status: 200, body: { import_id: 'imp_1', board_id: 'b_ajrtwtlddnb3sgor', instance_ids: ['i1'], changes: [] } },
      'GET /api/integrations/instances': { status: 200, body: [] },
      'GET /api/boards': { status: 200, body: { boards: [{ id: 'b_ajrtwtlddnb3sgor', name: 'Alpaca Homepage' }] } },
      'GET /api/boards/b_ajrtwtlddnb3sgor/layout': { status: 200, body: { revision: 2, format_version: 1, layout: (golden.plan['board'] as Record<string, unknown>)['layout'], tiles: {} } },
      ...over,
    });
    const h = harness();
    const w = h.mountIn(ImportFlow);
    return { ...h, w, f };
  }

  async function previewPasted(w: ReturnType<typeof mountFlow>['w']) {
    await w.find('#imp-services_yaml').setValue(SERVICES);
    await w.findAll('button').find((b) => b.text() === 'Preview import')!.trigger('click');
    await flushPromises();
  }

  it('needs services.yaml before a preview', async () => {
    const { w, f } = mountFlow();
    await w.findAll('button').find((b) => b.text() === 'Preview import')!.trigger('click');
    expect(w.find('.err').text()).toContain('services.yaml first');
    expect(f.calls).toHaveLength(0);
  });

  it('previews pasted files, then applies exactly the reviewed plan and lands on the board', async () => {
    const { w, f, ui } = mountFlow();
    await previewPasted(w);
    expect(f.calls[0]).toEqual({ method: 'POST', path: '/api/import/homepage?dry_run=1', body: { files: { services_yaml: SERVICES }, names: {} } });
    const review = w.find('.imp-review');
    expect(review.text()).toContain('45 of 45 selected');
    expect(review.text()).toContain('18 secrets move into the vault');
    expect(review.text()).toContain('The same password is used by Traefik and qBittorrent');
    expect(review.findAll('.bp-tile').length).toBe(22);

    await review.findAll('button').find((b) => b.text().startsWith('Apply 45'))!.trigger('click');
    await flushPromises();
    expect(f.last('POST', '/api/import/homepage')!.body).toEqual({ files: { services_yaml: SERVICES }, names: {}, choices: {}, plan_hash: golden.plan_hash });
    expect(route.value.path).toBe('/boards/b_ajrtwtlddnb3sgor');
    expect(ui.state.toasts[0]!.text).toContain('Imported from Homepage: 22 tiles, 23 connections. 18 secrets in the vault. 2 connections still need a key.');
    expect(isFresh('homeassistant')).toBe(true);
  });

  it('re-checks the plan with the choices before applying, and sends them', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { w, f } = mountFlow();
    await previewPasted(w);
    await w.find(`#row-${HA.replace(':', '\\:')}`).setValue(false);
    expect(w.find('.imp-summary').text()).toContain('Also leaves out HomeAssistant');
    expect(w.find('.imp-apply button').text()).toContain('Checking');
    await vi.advanceTimersByTimeAsync(300);
    await flushPromises();
    const recheck = f.calls.filter((c) => c.path === '/api/import/homepage?dry_run=1')[1]!;
    expect(recheck.body).toMatchObject({ choices: { skip: [HA] } });
    await w.find('.imp-apply button').trigger('click');
    await flushPromises();
    expect(f.last('POST', '/api/import/homepage')!.body).toMatchObject({ choices: { skip: [HA] } });
  });

  it('shows what changed when the hub says the plan moved, and stays in review', async () => {
    const { w } = mountFlow({ 'POST /api/import/homepage': { status: 409, body: { error: { code: 'plan_changed', message: 'changed' }, result: golden } } });
    await previewPasted(w);
    await w.find('.imp-apply button').trigger('click');
    await flushPromises();
    expect(w.find('.imp-review').text()).toContain('Something changed since the preview');
    expect(w.find('.imp-apply button').text()).toContain('Apply');
  });

  it('names a dropped file’s source, then previews it', async () => {
    const { w, f } = mountFlow({ 'POST /api/import/detect': { status: 200, body: { source: 'homepage', name: 'Homepage', files: { 'services.yaml': 'services_yaml' }, unused: ['notes.txt'], missing: [] } } });
    const input = w.find<HTMLInputElement>('input[type="file"]');
    const files = [new File([SERVICES], 'services.yaml', { type: 'text/yaml' }), new File(['hi'], 'notes.txt')];
    Object.defineProperty(input.element, 'files', { value: files, configurable: true });
    await input.trigger('change');
    await flushPromises();
    expect(f.calls[0]).toMatchObject({ path: '/api/import/detect', body: { files: [{ name: 'services.yaml', head: SERVICES }, { name: 'notes.txt', head: 'hi' }] } });
    expect(f.calls[1]).toMatchObject({ path: '/api/import/homepage?dry_run=1', body: { names: { services_yaml: 'services.yaml' } } });
    expect(w.find('.imp-review').exists()).toBe(true);
  });

  it('sends a Cha Dash export to Backup, and says what a Homarr 0.x config needs', async () => {
    const { w } = mountFlow({
      'POST /api/import/detect': (body: unknown) => {
        const name = (body as { files: { name: string }[] }).files[0]!.name;
        return name === 'home.yaml'
          ? { status: 200, body: { source: 'chadash', files: {}, unused: [], missing: [] } }
          : { status: 200, body: { source: 'homarr_legacy', files: {}, unused: [], missing: [], message: 'This is a Homarr 0.x config. Upgrade to Homarr 2.' } };
      },
    });
    const input = w.find<HTMLInputElement>('input[type="file"]');
    Object.defineProperty(input.element, 'files', { value: [new File(['{}'], 'default.json')], configurable: true });
    await input.trigger('change');
    await flushPromises();
    expect(w.find('.err').text()).toContain('Upgrade to Homarr 2');
    Object.defineProperty(input.element, 'files', { value: [new File(['kind: Home'], 'home.yaml')], configurable: true });
    await input.trigger('change');
    await flushPromises();
    expect(route.value.path).toBe('/settings/backup');
  });

  it('shows the hub’s YAML error, which names the file and line', async () => {
    const { w } = mountFlow({
      'POST /api/import/homepage?dry_run=1': { status: 422, body: { error: { code: 'invalid_config', message: 'services.yaml line 12: mapping values are not allowed here' } } },
    });
    await previewPasted(w);
    expect(w.find('.err').text()).toBe('Services.yaml line 12: mapping values are not allowed here.');
    expect(w.find('.imp-review').exists()).toBe(false);
  });
});

describe('Settings → Bring in → History', () => {
  const entry = {
    id: 'imp_1', source: 'yaml', created_at: Date.parse('2026-10-29T09:00:00Z'), files: ['board.yaml'], target: 'new', board_id: 'b1',
    created: { board: 'b1', instances: ['i1'], secrets: [], items: ['x'] }, counts: { created: 3, updated: 0, tiles: 1, instances: 1, secrets: 0 },
  };

  it('plans a revert, steps up when it removes connections, then runs it', async () => {
    let tries = 0;
    const f = fakeFetch({
      'GET /api/imports': { status: 200, body: { imports: [entry] } },
      'POST /api/imports/imp_1/revert?dry_run=1': { status: 200, body: { remove: [{ kind: 'instance', id: 'i1', name: 'Checks' }], keep: [], connections: true } },
      'POST /api/imports/imp_1/revert': () =>
        ++tries === 1 ? { status: 403, body: { error: { code: 'step_up_required', message: 'Confirm it’s you first.' } } } : { status: 200, body: { remove: [{ kind: 'instance', id: 'i1' }], keep: [], connections: true } },
      'GET /api/integrations/instances': { status: 200, body: [] },
    });
    const h = harness();
    const w = h.mountIn(HistoryList);
    await flushPromises();
    expect(w.text()).toContain('Plain YAML');
    await w.findAll('button').find((b) => b.text() === 'Revert…')!.trigger('click');
    await flushPromises();
    expect(w.find('.hist-plan').text()).toContain('Removes the connection Checks');
    const done = w.find('.hist-plan .btn.primary').trigger('click');
    await flushPromises();
    expect(h.ui.state.stepUp?.reason).toContain('removes connections');
    h.ui.stepUpDone(true);
    await done;
    await flushPromises();
    expect(f.calls.filter((c) => c.path === '/api/imports/imp_1/revert')).toHaveLength(2);
    expect(h.ui.state.toasts[0]!.text).toContain('Reverted: 1 thing removed');
  });

  it('Import again hands the source back', async () => {
    fakeFetch({ 'GET /api/imports': { status: 200, body: { imports: [entry] } } });
    const h = harness();
    const w = h.mountIn(HistoryList);
    await flushPromises();
    await w.findAll('button').find((b) => b.text() === 'Import again')!.trigger('click');
    expect(w.findComponent(HistoryList).emitted('again')).toEqual([['yaml']]);
  });
});
