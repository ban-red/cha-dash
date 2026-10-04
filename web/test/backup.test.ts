import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import BackupPane from '../src/settings/BackupPane.vue';
import SettingsSheet from '../src/settings/SettingsSheet.vue';
import { resolve, route, settingsPath, syncRoute } from '../src/lib/router';
import { backupPreviewOf, hasBundle, pathLabel, valueText } from '../src/model/backupPlan';
import { resetIntegrations } from '../src/stores/integrations';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

/** A dry run in the hub's shape (internal/export: Plan). */
const PLAN = {
  plan: {
    format: 'chadash/v1alpha1',
    exported_at: '2026-10-04T09:30:00Z',
    hub_version: '0.1.0',
    changes: [
      { kind: 'board', id: 'b_home', name: 'Front page', action: 'update', fields: [{ path: '/name', op: 'replace', from: 'Home', to: 'Front page' }] },
      {
        kind: 'layout',
        id: 'b_home',
        name: 'Front page',
        action: 'update',
        fields: [
          { path: '/document/sections/media/items/service@homepage:media~1sonarr/size', op: 'replace', from: 'small', to: 'wide' },
          { path: '/document/sections/media/items/clock', op: 'add', to: { id: 'clock', type: 'clock', size: 'small' } },
          { path: '/document/sections', op: 'reorder', from: ['media', 'net'], to: ['net', 'media'] },
        ],
      },
      { kind: 'tiles', id: 'b_home', name: 'Front page', action: 'update', fields: [{ path: '/service@homepage:media~1radarr/name', op: 'replace', from: 'Radarr', to: 'Films' }] },
      { kind: 'board', id: 'b_lab', name: 'Lab', action: 'unchanged' },
      { kind: 'layout', id: 'b_lab', name: 'Lab', action: 'create', detail: '2 sections, 9 items' },
      {
        kind: 'instance',
        id: 'i_new',
        name: 'Proxy',
        integration: 'traefik',
        action: 'create',
        fields: [{ path: '/config/url', op: 'add', to: 'https://proxy.home.arpa' }],
        secrets: [{ field: 'password', secret: 'instance/i_new/password', action: 'needed', in_bundle: true }],
      },
      {
        kind: 'instance',
        id: 'i_sonarr',
        name: 'Sonarr',
        integration: 'sonarr',
        action: 'update',
        fields: [{ path: '/tls/pin', op: 'remove', from: 'abab' }],
        secrets: [{ field: 'api_key', secret: 'instance/i_sonarr/api_key', action: 'update', source: 'bundle' }],
      },
      { kind: 'setting', id: 'import/homepage', action: 'unchanged' },
      { kind: 'instance', id: 'i_old', name: 'Old Radarr', integration: 'radarr', action: 'stale' },
    ],
    secrets_needed: [{ instance: 'i_new', name: 'Proxy', integration: 'traefik', field: 'password', secret: 'instance/i_new/password', in_bundle: true }],
    bundle: { present: true, unlocked: false, secrets: 3 },
    summary: { create: 2, update: 4, unchanged: 2, stale: 1, secrets_restored: 1, secrets_needed: 1 },
    warnings: [{ code: 'unknown_integration', kind: 'instance', id: 'i_x', message: 'Legacy uses the "gone" integration, which this hub doesn’t include.' }],
  },
};

const UNCHANGED = {
  plan: {
    format: 'chadash/v1alpha1',
    changes: [{ kind: 'board', id: 'b_home', name: 'Home', action: 'unchanged' }],
    secrets_needed: [],
    bundle: { present: false, unlocked: false, secrets: 0 },
    summary: { create: 0, update: 0, unchanged: 1, stale: 0, secrets_restored: 0, secrets_needed: 0 },
    warnings: [],
  },
};

const HOME = 'apiVersion: chadash/v1alpha1\nkind: Home\nboards: []\n';
const BUNDLED = HOME + 'secrets_bundle:\n  cipher: xchacha20poly1305\n';
const PASS = 'the backup test passphrase';

beforeEach(() => {
  history.replaceState(null, '', '/settings/backup');
  syncRoute();
  resetIntegrations();
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  document.body.innerHTML = '';
});

describe('the Home file plan', () => {
  it('groups objects by kind, changes first, with field and secret rows', () => {
    const p = backupPreviewOf(PLAN);
    expect(p.sections.map((s) => [s.title, s.changed.length, s.unchanged.length])).toEqual([
      ['Boards', 1, 1],
      ['Layouts', 2, 0],
      ['Tile settings', 1, 0],
      ['Connections', 3, 0],
      ['Settings', 0, 1],
    ]);
    const layout = p.sections[1]!.changed[0]!;
    expect(layout.fields).toEqual([
      { path: 'sections › media › items › service@homepage:media/sonarr › size', op: 'replace', from: '"small"', to: '"wide"' },
      { path: 'sections › media › items › clock', op: 'add', to: '{"id":"clock","type":"clock","size":"small"}' },
      { path: 'sections', op: 'reorder', from: 'media, net', to: 'net, media' },
    ]);
    const conns = p.sections[3]!.changed;
    expect(conns.map((o) => [o.name, o.action])).toEqual([
      ['Proxy', 'create'],
      ['Sonarr', 'update'],
      ['Old Radarr', 'stale'],
    ]);
    expect(conns[0]!.secrets).toEqual([{ field: 'password', action: 'needed', inBundle: true }]);
    expect(conns[1]!.secrets).toEqual([{ field: 'api_key', action: 'update', source: 'bundle', inBundle: false }]);
    expect(p.needed).toEqual([{ instance: 'i_new', name: 'Proxy', integration: 'traefik', field: 'password', inBundle: true }]);
    expect(p.bundle).toEqual({ present: true, unlocked: false, secrets: 3 });
    expect(p.summary).toEqual({ create: 2, update: 4, unchanged: 2, stale: 1, secretsRestored: 1, secretsNeeded: 1 });
    expect(p.nothingNew).toBe(false);
    expect(backupPreviewOf(UNCHANGED).nothingNew).toBe(true);
    // Unknown kinds and actions are dropped, never thrown.
    expect(backupPreviewOf({ plan: { changes: [{ kind: 'user', action: 'create' }, { kind: 'board', action: 'delete' }] } }).sections).toEqual([]);
    expect(backupPreviewOf(null).sections).toEqual([]);
  });

  it('labels paths and cuts values', () => {
    expect(pathLabel('/config/url')).toBe('config › url');
    expect(pathLabel('/document/a~0b/c~1d')).toBe('a~b › c/d');
    expect(pathLabel('')).toBe('(whole)');
    expect(valueText('x'.repeat(100))).toHaveLength(72);
    expect(valueText(false)).toBe('false');
    expect(hasBundle(BUNDLED)).toBe(true);
    expect(hasBundle(HOME + '# secrets_bundle: in a comment\n')).toBe(false);
  });
});

describe('Settings → Backup', () => {
  /** fakeFetch for the JSON routes, plus /api/export answering YAML. */
  function mountPane(routes: Parameters<typeof fakeFetch>[0] = {}, exportAnswer?: (init?: RequestInit) => Response) {
    const f = fakeFetch({
      'POST /api/import/chadash?dry_run=1': { status: 200, body: PLAN },
      'POST /api/import/chadash': { status: 200, body: { ...PLAN, changed: true, board_ids: ['b_home'], instance_ids: ['i_new'] } },
      'GET /api/integrations/instances': { status: 200, body: [] },
      ...routes,
    });
    const exports: (RequestInit | undefined)[] = [];
    const json = f.fn;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: string, init?: RequestInit) => {
        if (new URL(input, 'http://hub.test').pathname === '/api/export') {
          exports.push(init);
          return (
            exportAnswer?.(init) ??
            new Response('apiVersion: chadash/v1alpha1\n', {
              status: 200,
              headers: { 'Content-Type': 'application/yaml', 'Content-Disposition': `attachment; filename="chadash-home-2026-10-04${init?.method === 'POST' ? '-with-secrets' : ''}.yaml"` },
            })
          );
        }
        return json(input, init);
      }),
    );
    const createObjectURL = vi.fn(() => 'blob:hub.test/1');
    const revokeObjectURL = vi.fn();
    Object.assign(URL, { createObjectURL, revokeObjectURL });
    const clicks: { href: string; download: string }[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      clicks.push({ href: this.getAttribute('href') ?? '', download: this.download });
    });
    const h = harness();
    const w = h.mountIn(BackupPane);
    return { ...h, w, f, exports, clicks, createObjectURL };
  }
  const button = (w: ReturnType<typeof mountPane>['w'], text: string) => w.findAll('button').find((b) => b.text().startsWith(text))!;

  it('downloads an export with placeholders only', async () => {
    const { w, exports, clicks, createObjectURL } = mountPane();
    expect(w.text()).toContain('Secrets stay out');
    await button(w, 'Download export').trigger('submit');
    await flushPromises();
    expect(exports).toHaveLength(1);
    expect(exports[0]?.method).toBeUndefined();
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    expect(clicks).toEqual([{ href: 'blob:hub.test/1', download: 'chadash-home-2026-10-04.yaml' }]);
    expect(w.text()).toContain('Saved chadash-home-2026-10-04.yaml');
  });

  it('asks for a passphrase twice before adding the encrypted secrets', async () => {
    const { w, exports, clicks } = mountPane();
    await w.find('.bk-out input[type="checkbox"]').setValue(true);
    expect(w.find('#bk-pass').attributes('type')).toBe('password');
    await w.find('#bk-pass').setValue('short');
    expect(w.find('#bk-pass').attributes('aria-invalid')).toBe('true');
    await w.find('form.bk-out').trigger('submit');
    expect(w.find('.err').text()).toContain('at least 12 characters');
    await w.find('#bk-pass').setValue(PASS);
    await w.find('#bk-pass2').setValue(PASS + '!');
    await w.find('form.bk-out').trigger('submit');
    expect(w.find('.err').text()).toBe('The two passphrases differ.');
    expect(exports).toHaveLength(0);

    await w.find('#bk-pass2').setValue(PASS);
    await w.find('form.bk-out').trigger('submit');
    await flushPromises();
    expect(exports[0]?.method).toBe('POST');
    expect(JSON.parse(String(exports[0]?.body))).toEqual({ passphrase: PASS });
    expect(clicks[0]?.download).toBe('chadash-home-2026-10-04-with-secrets.yaml');
    // The passphrase isn't kept once it's been used.
    expect((w.find('#bk-pass').element as HTMLInputElement).value).toBe('');
    expect((w.find('#bk-pass2').element as HTMLInputElement).value).toBe('');
  });

  it('says why the hub refused an export', async () => {
    const { w, clicks } = mountPane({}, () =>
      new Response(JSON.stringify({ error: { code: 'secret_in_plain_text', message: 'refusing to write a Home file that holds the value of secret instance/i_x/api_key in plain text' } }), {
        status: 409,
      }),
    );
    await w.find('form.bk-out').trigger('submit');
    await flushPromises();
    expect(w.find('.err').text()).toContain('Refusing to write a Home file that holds the value of secret instance/i_x/api_key in plain text. Nothing was saved.');
    expect(clicks).toHaveLength(0);
  });

  it('previews a Home file field by field, then applies it', async () => {
    const { w, f, ui } = mountPane();
    await button(w, 'Preview import').trigger('click');
    expect(w.find('.err').text()).toContain('Choose a Home file');
    expect(f.calls).toHaveLength(0);

    await w.find('#bk-yaml').setValue(HOME);
    expect(w.find('#bk-unlock').exists()).toBe(false);
    await button(w, 'Preview import').trigger('click');
    await flushPromises();
    expect(f.calls[0]).toEqual({ method: 'POST', path: '/api/import/chadash?dry_run=1', body: { yaml: HOME } });

    const plan = w.find('.bk-preview');
    expect(plan.text()).toContain('by Cha Dash 0.1.0');
    expect(plan.find('.bk-summary').text()).toContain('New2Updated4Unchanged2Not in the file1Secrets restored1Secrets needed1');
    expect(plan.text()).toContain('The file’s 3 secrets are encrypted');
    expect(plan.text()).toContain('Legacy uses the "gone" integration');
    expect(plan.findAll('.bk-kind h4').map((h) => h.text())).toEqual(['Boards · 2', 'Layouts · 2', 'Tile settings · 1', 'Connections · 3', 'Settings · 1']);
    const layout = plan.findAll('.bk-kind')[1]!.find('.bk-obj');
    expect(layout.find('.bk-act').text()).toBe('Updated');
    expect(layout.findAll('.bk-fields li').map((li) => li.text())).toEqual([
      '~sections › media › items › service@homepage:media/sonarr › sizechanged"small" → "wide"',
      '+sections › media › items › clockadded{"id":"clock","type":"clock","size":"small"}',
      '⇅sectionsreorderedmedia, net → net, media',
    ]);
    const conns = plan.findAll('.bk-kind')[3]!;
    expect(conns.text()).toContain('passwordneeded: in the file’s encrypted bundle');
    expect(conns.text()).toContain('api_keyrestored from the bundle');
    expect(conns.text()).toContain('On this hub but not in the file. Kept as it is.');
    expect(plan.find('.bk-same summary').text()).toBe('1 unchanged');
    expect(plan.text()).toContain('Secrets needed · 1');

    await button(w, 'Import 6 changes').trigger('click');
    await flushPromises();
    expect(f.last('POST', '/api/import/chadash')?.body).toEqual({ yaml: HOME });
    expect(ui.state.toasts[0]!.text).toBe('Imported: 2 new, 4 updated. 1 secret restored. 1 still needed.');
    expect(w.find('.bk-preview').exists()).toBe(false);
    expect(w.text()).toContain('1 secret is still needed');
    expect(f.last('GET', '/api/integrations/instances')).toBeDefined();
    expect((w.find('#bk-yaml').element as HTMLTextAreaElement).value).toBe('');

    await w.find('.st-note a').trigger('click');
    expect(route.value.path).toBe('/settings/integrations');
  });

  it('asks for the passphrase of a file with encrypted secrets, and says when it’s wrong', async () => {
    const { w, f } = mountPane({
      'POST /api/import/chadash?dry_run=1': (body) =>
        (body as { passphrase?: string }).passphrase === PASS
          ? { status: 200, body: PLAN }
          : { status: 422, body: { error: { code: 'wrong_passphrase', message: 'the passphrase is wrong, or the secrets bundle was changed' } } },
    });
    await w.find('#bk-yaml').setValue(BUNDLED);
    expect(w.text()).toContain('has encrypted secrets');
    await w.find('#bk-unlock').setValue('not it at all, sorry');
    await button(w, 'Preview import').trigger('click');
    await flushPromises();
    expect(w.find('#bk-unlock').attributes('aria-invalid')).toBe('true');
    expect(w.find('#bk-unlock-help').text()).toContain('That passphrase didn’t open the secrets');
    expect(w.find('.err').exists()).toBe(false);
    expect(w.find('.bk-preview').exists()).toBe(false);

    await w.find('#bk-unlock').setValue(PASS);
    expect(w.find('#bk-unlock').attributes('aria-invalid')).toBeUndefined();
    await button(w, 'Preview import').trigger('click');
    await flushPromises();
    expect(f.last('POST', '/api/import/chadash?dry_run=1')?.body).toEqual({ yaml: BUNDLED, passphrase: PASS });
    expect(w.find('.bk-preview').exists()).toBe(true);
    // The passphrase never shows up as text.
    expect(document.body.textContent).not.toContain(PASS);
  });

  it('shows the hub’s reason for a file it refuses, which names a line and a path', async () => {
    const { w } = mountPane({
      'POST /api/import/chadash?dry_run=1': {
        status: 422,
        body: { error: { code: 'invalid_export', message: 'line 8, instances[0].secrets.api_key: must be a placeholder (!secret <vault name>, or ${secret:<vault name>}); Home files never hold secret values' } },
      },
    });
    await w.find('#bk-yaml').setValue(HOME);
    await button(w, 'Preview import').trigger('click');
    await flushPromises();
    expect(w.find('.err').text()).toBe(
      'Line 8, instances[0].secrets.api_key: must be a placeholder (!secret <vault name>, or ${secret:<vault name>}); Home files never hold secret values.',
    );
  });

  it('has nothing to apply when the hub already matches', async () => {
    const { w } = mountPane({ 'POST /api/import/chadash?dry_run=1': { status: 200, body: UNCHANGED } });
    await w.find('#bk-yaml').setValue(HOME);
    await button(w, 'Preview import').trigger('click');
    await flushPromises();
    expect(w.text()).toContain('This hub already matches the file');
    expect(button(w, 'Nothing to import').attributes('disabled')).toBeDefined();
  });

  it('is a Settings pane of its own, loaded when it opens', async () => {
    expect(resolve({ path: '/settings/backup', query: {} })).toEqual({ name: 'settings', pane: 'backup' });
    expect(settingsPath('backup')).toBe('/settings/backup');
    mountPane();
    const h = harness();
    h.mountIn(SettingsSheet);
    await flushPromises();
    await new Promise((r) => setTimeout(r, 0));
    await flushPromises();
    const nav = [...document.querySelectorAll('.st-nav a')].map((a) => [a.textContent, a.getAttribute('aria-current')]);
    expect(nav).toContainEqual(['Backupexport and restore', 'page']);
    expect(document.querySelector('#settings-h')?.textContent).toBe('Backup');
    expect(document.querySelector('.bk-out')).not.toBeNull();
  });

  it('reads a dropped file', async () => {
    const { w } = mountPane();
    const file = new File([BUNDLED], 'home.yaml', { type: 'application/yaml' });
    await w.find('.bk-file').trigger('drop', { dataTransfer: { files: [file] } });
    await flushPromises();
    expect(w.find('.if-name').text()).toBe('home.yaml');
    expect(w.find('#bk-unlock').exists()).toBe(true);
  });
});
