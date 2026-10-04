import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import goldenRaw from '../../testdata/importers/homepage/dogfood/expected-plan.json?raw';
import ImportPane from '../src/settings/ImportPane.vue';
import { route, syncRoute } from '../src/lib/router';
import { previewOf } from '../src/model/importPlan';
import { resetIntegrations } from '../src/stores/integrations';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

/** The importer's golden dry run for the dogfood home (internal/importer/homepage). */
const golden = JSON.parse(goldenRaw) as { plan: Record<string, unknown>; hints: unknown[]; unmapped: unknown[] };

beforeEach(() => {
  history.replaceState(null, '', '/settings/import');
  syncRoute();
  resetIntegrations();
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('the dry-run preview', () => {
  it('reads the importer’s plan: sections and tiles, connections, secrets as a count, hints', () => {
    const p = previewOf(golden);
    expect(p.boardName).toBe('Alpaca Homepage');
    expect(p.sections.map((s) => s.title)).toEqual(expect.arrayContaining(['Home', 'Servers']));
    expect(p.tileCount).toBe(22);
    expect(p.instances).toHaveLength(21);
    expect(p.secretsMoved).toBe(18);
    expect(p.checks).toBe(3);
    const servers = p.sections.find((s) => s.id === 'servers')!;
    expect(servers.tiles[1]).toEqual({ id: 'service@homepage:servers/proxmoxpluto', label: 'Pluto PVE (main opnsense)', widget: 'service', live: true });
    expect(p.hints[0]).toMatchObject({ code: 'reused_secret', severity: 'attention', inbox: true });
    expect(p.discovery.length).toBe(3);
    expect(p.ignored.find((g) => g.what === 'settings.yaml › background')?.reason).toContain('wallpaper');
    expect(p.notes[0]).toContain('Apps has no services');
    expect(p.changes.create).toBeGreaterThan(20);
    expect(p.nothingNew).toBe(false);
    // values never ride along: the preview has names and counts only
    expect(JSON.stringify(p)).not.toMatch(/\$\{secret:/);
  });

  it('reads unmapped entries, missing secrets and a re-import', () => {
    const p = previewOf({
      plan: {
        board: { name: 'B', layout: { sections: [{ id: 's', title: 'S', items: [{ id: 'service@homepage:s/x', type: 'service' }] }] } },
        tiles: [],
        instances: [{ id: 'i1', integration: 'sonarr', name: 'Sonarr', config: { url: 'http://sonarr:8989' }, secret_fields: [], missing_secrets: ['api_key'] }],
        changes: [
          { kind: 'board', id: 'b', action: 'unchanged' },
          { kind: 'instance', id: 'i1', name: 'Sonarr', action: 'unchanged' },
          { kind: 'instance', id: 'i0', name: 'Old Radarr', action: 'stale' },
        ],
      },
      hints: [],
      unmapped: [{ kind: 'widget', origin: { file: 'services.yaml', group: 'Media', entry: 'Jellyseerr' }, type: 'jellyseerr', fields: ['key'], reason: 'no integration for jellyseerr yet' }],
    });
    expect(p.sections[0]!.tiles[0]!.label).toBe('x');
    expect(p.instances[0]!.missingSecrets).toEqual(['api_key']);
    expect(p.unmapped[0]).toEqual({ name: 'Jellyseerr', kind: 'widget', where: 'services.yaml › Media', reason: 'no integration for jellyseerr yet', fields: ['key'] });
    expect(p.stale).toEqual(['Old Radarr']);
    expect(p.nothingNew).toBe(true);
  });
});

describe('Settings → Import from Homepage', () => {
  const SERVICES = '- Media:\n    - Plex:\n        href: https://plex.home.arpa\n';

  function mountPane(over: Record<string, unknown> = {}) {
    const f = fakeFetch({
      'POST /api/import/homepage?dry_run=1': { status: 200, body: golden },
      'POST /api/import/homepage': { status: 200, body: { board_id: 'b_ajrtwtlddnb3sgor', instance_ids: ['i1'], changes: [] } },
      'GET /api/integrations/instances': { status: 200, body: [] },
      'GET /api/boards': { status: 200, body: { boards: [{ id: 'b_ajrtwtlddnb3sgor', name: 'Alpaca Homepage' }] } },
      'GET /api/boards/b_ajrtwtlddnb3sgor/layout': {
        status: 200,
        body: { revision: 2, format_version: 1, layout: (golden.plan['board'] as Record<string, unknown>)['layout'], tiles: { plex: { name: 'Plex' } } },
      },
      ...over,
    });
    const h = harness();
    const w = h.mountIn(ImportPane);
    return { ...h, w, f };
  }

  it('picks Homepage as the source; Homarr and Plain YAML are v0.2', () => {
    const { w } = mountPane();
    const seg = w.findAll('.import-src button');
    expect(seg.map((b) => [b.text(), b.attributes('aria-pressed'), b.attributes('disabled') !== undefined])).toEqual([
      ['Homepage', 'true', false],
      ['Homarrv0.2', 'false', true],
      ['Plain YAMLv0.2', 'false', true],
    ]);
  });

  it('needs services.yaml before a preview', async () => {
    const { w, f } = mountPane();
    await w.findAll('button').find((b) => b.text() === 'Preview import')!.trigger('click');
    expect(w.find('.err').text()).toContain('services.yaml first');
    expect(f.calls).toHaveLength(0);
  });

  it('previews with a dry run, then applies and shows the new board', async () => {
    const { w, f, ui } = mountPane();
    await w.find('#yaml-services_yaml').setValue(SERVICES);
    await w.find('#yaml-settings_yaml').setValue('title: Alpaca Homepage\n');
    expect(w.text()).toContain('Pasted · 4 lines');
    await w.findAll('button').find((b) => b.text() === 'Preview import')!.trigger('click');
    await flushPromises();
    expect(f.calls[0]).toEqual({ method: 'POST', path: '/api/import/homepage?dry_run=1', body: { services_yaml: SERVICES, docker_yaml: '', settings_yaml: 'title: Alpaca Homepage\n' } });
    const plan = w.find('.import-preview');
    expect(plan.text()).toContain('Alpaca Homepage');
    expect(plan.find('.kv').text()).toContain('Secrets → vault18');
    expect(plan.text()).toContain('18 credentials move into the encrypted vault');
    expect(plan.text()).toContain('The same password is used by Traefik and qBittorrent');
    expect(plan.text()).toContain('to the Inbox');
    expect(plan.text()).toContain('Pluto PVE (main opnsense)');
    expect(plan.text()).toContain('Fills in later');

    await plan.findAll('button').find((b) => b.text().startsWith('Import 22 tiles'))!.trigger('click');
    await flushPromises();
    expect(f.last('POST', '/api/import/homepage')!.body).toEqual({ services_yaml: SERVICES, docker_yaml: '', settings_yaml: 'title: Alpaca Homepage\n' });
    expect(route.value.path).toBe('/boards/b_ajrtwtlddnb3sgor');
    expect(ui.state.toasts[0]!.text).toContain('22 tiles and 21 connections');
    expect(ui.state.toasts[0]!.text).toContain('18 secrets are in the vault');
    // landing on another board loads it (the front page follows the route); nothing reads the import record
    expect(f.calls.some((c) => c.path === '/api/import/homepage' && c.method === 'GET')).toBe(false);
  });

  it('reloads the board on screen, with its tiles, after re-importing it', async () => {
    const { w, f, board } = mountPane();
    board.state.boardId = 'b_ajrtwtlddnb3sgor';
    await w.find('#yaml-services_yaml').setValue(SERVICES);
    await w.findAll('button').find((b) => b.text() === 'Preview import')!.trigger('click');
    await flushPromises();
    await w.find('.import-preview').findAll('button').find((b) => b.text().startsWith('Import'))!.trigger('click');
    await flushPromises();
    expect(f.last('GET', '/api/boards/b_ajrtwtlddnb3sgor/layout')).toBeDefined();
    expect(board.state.revision).toBe(2);
    expect(board.tiles.value).toEqual({ plex: { name: 'Plex' } });
  });

  it('shows the hub’s YAML error, which names the file and line', async () => {
    const { w } = mountPane({
      'POST /api/import/homepage?dry_run=1': { status: 422, body: { error: { code: 'invalid_config', message: 'services.yaml line 12: mapping values are not allowed here' } } },
    });
    await w.find('#yaml-services_yaml').setValue('- broken: yaml: here');
    await w.findAll('button').find((b) => b.text() === 'Preview import')!.trigger('click');
    await flushPromises();
    expect(w.find('.err').text()).toBe('Services.yaml line 12: mapping values are not allowed here.');
    expect(w.find('.import-preview').exists()).toBe(false);
  });

  it('says when the files are too large', async () => {
    const { w } = mountPane({ 'POST /api/import/homepage?dry_run=1': { status: 413, body: { error: { code: 'too_large', message: 'too large' } } } });
    await w.find('#yaml-services_yaml').setValue(SERVICES);
    await w.findAll('button').find((b) => b.text() === 'Preview import')!.trigger('click');
    await flushPromises();
    expect(w.find('.err').text()).toContain('larger than 1 MB');
  });

  it('reads an uploaded file', async () => {
    const { w } = mountPane();
    const input = w.find<HTMLInputElement>('input[type="file"]');
    const file = new File([SERVICES], 'services.yaml', { type: 'text/yaml' });
    Object.defineProperty(input.element, 'files', { value: [file], configurable: true });
    await input.trigger('change');
    await flushPromises();
    expect((w.find('#yaml-services_yaml').element as HTMLTextAreaElement).value).toBe(SERVICES);
    expect(w.text()).toContain('services.yaml · 4 lines');
  });

  it('goes back to editing when a file changes after the preview', async () => {
    const { w } = mountPane();
    await w.find('#yaml-services_yaml').setValue(SERVICES);
    await w.findAll('button').find((b) => b.text() === 'Preview import')!.trigger('click');
    await flushPromises();
    expect(w.find('.import-preview').exists()).toBe(true);
    await w.find('#yaml-services_yaml').setValue(SERVICES + '#\n');
    await nextTick();
    expect(w.find('.import-preview').exists()).toBe(false);
  });
});
