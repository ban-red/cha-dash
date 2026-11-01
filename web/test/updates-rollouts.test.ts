/**
 * Settings → Updates in v0.4 (docs/plan/14-v0.4.md, wave 26): Update all or
 * a selection as one rollout, scheduled into a maintenance window or not;
 * rollouts and windows; host packages and Unraid's version; release notes
 * as untrusted text.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { setDisplayTimeZone } from '../src/lib/format';
import { navigate } from '../src/lib/router';
import UpdatesPane from '../src/settings/UpdatesPane.vue';
import { inRepo, noteParts, windowProblem } from '../src/settings/updates';
import { axeViolations } from './axe';
import { fakeFetch, type Handler } from './fake-fetch';
import { harness } from './harness';

beforeEach(() => {
  setDisplayTimeZone('UTC');
  navigate('/settings/updates', { replace: true });
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

const home = () => ({
  graph: {
    resources: {
      'docker:i_a/engine': { id: 'docker:i_a/engine', kind: 'engine', name: 'magpie' },
      'proxmox:i_p/node/pve-a': { id: 'proxmox:i_p/node/pve-a', kind: 'host', name: 'pve-a' },
    },
    relations: {},
  },
  metrics: { series: {} },
  inbox: { now: '2026-10-31T12:05:00Z', incidents: {} },
});

const row = (name: string, extra: Record<string, unknown> = {}) => ({
  resource: `docker:i_a/container/${name}`,
  name,
  kind: 'image',
  risk: 'unknown',
  engine: 'docker:i_a/engine',
  host: 'magpie',
  image: `example/${name}:1`,
  state: 'available',
  current: 'sha256:aaaaaaaaaaaaaaaa',
  latest: 'sha256:bbbbbbbbbbbbbbbb',
  checked_at: '2026-10-31T12:00:00Z',
  update: { id: 'docker.container.update', target: `docker:i_a/container/${name}` },
  ...extra,
});

const UPDATES = {
  updates: [
    row('sonarr', { source: 'https://github.com/acme/sonarr', version: '4.0.1', risk: 'minor' }),
    row('radarr'),
    row('lidarr', { state: 'held', hold: { kind: 'hold', created_at: '2026-10-30T12:00:00Z' } }),
    {
      resource: 'proxmox:i_p/node/pve-a', name: 'pve-a', kind: 'apt', risk: 'major', engine: '', host: 'pve-a', state: 'available', latest: '2 packages',
      why: 'Cha Dash doesn’t upgrade a host’s packages.', link: { title: 'Open in Proxmox', url: 'https://pve-a.lab.example:8006/' },
      packages: [
        { name: 'libc6', from: '2.36-9', to: '2.36-10', risk: 'patch' },
        { name: 'pve-manager', from: '8.4.1', to: '9.0.0', risk: 'major' },
      ],
    },
    {
      resource: 'unraid:i_u/host', name: 'jupiter', kind: 'os', risk: 'unknown', engine: '', host: 'jupiter', image: 'Unraid', state: 'cant_check', current: '7.1.4',
      reason: 'Unraid doesn’t tell Cha Dash whether a newer version is out.', link: { title: 'Open in Unraid', url: 'https://jupiter.lab.example' },
    },
  ],
};

const WINDOW = {
  id: 'mw_1', name: 'Sunday night', hosts: ['docker:i_a/engine'], starts_at: '2026-11-01T02:00:00Z', ends_at: '2026-11-01T05:00:00Z', weekly: true,
  created_by: 'owner', created_at: '2026-10-30T12:00:00Z', open: false, next_open: '2026-11-01T02:00:00Z', next_end: '2026-11-01T05:00:00Z',
};

const ROLLOUTS = {
  rollouts: [
    {
      id: 'rol_1', created_by: 'owner', plan_hash: 'sha256:x', state: 'failed', created_at: '2026-10-31T11:00:00Z', started_at: '2026-10-31T11:00:00Z', finished_at: '2026-10-31T11:20:00Z',
      steps: [
        { target: 'docker:i_a/container/bazarr', name: 'bazarr', host: 'magpie', action: 'docker.container.update', snapshot: 'pve', state: 'succeeded' },
        { target: 'docker:i_a/container/sonarr', name: 'sonarr', host: 'magpie', action: 'docker.container.update', state: 'rolled_back', reason: 'It didn’t come up healthy.' },
        { target: 'docker:i_a/container/radarr', name: 'radarr', host: 'magpie', action: 'docker.container.update', state: 'not_started' },
      ],
    },
    { id: 'rol_2', created_by: 'owner', plan_hash: 'sha256:y', state: 'waiting', window_id: 'mw_1', created_at: '2026-10-30T11:00:00Z', steps: [] },
  ],
};

const PLAN = {
  steps: [
    { target: 'docker:i_a/container/sonarr', name: 'sonarr', action: 'docker.container.update', state: 'pending' },
    { target: 'docker:i_a/container/lidarr', name: 'lidarr', action: 'docker.container.update', state: 'skipped', reason: 'lidarr is held on its image.' },
  ],
  hash: 'sha256:' + 'a'.repeat(64),
  tier: 'T2',
  ordered: true,
};

async function pane(role = 'owner', extra: Record<string, Handler | { status: number; body?: unknown }> = {}) {
  const hub = fakeFetch({
    'GET /api/updates': { status: 200, body: UPDATES },
    'GET /api/updates/settings': { status: 200, body: { release_notes: true } },
    'PUT /api/updates/settings': (b) => ({ status: 200, body: b }),
    'GET /api/updates/rollouts': { status: 200, body: ROLLOUTS },
    'POST /api/updates/rollouts/preview': { status: 200, body: PLAN },
    'POST /api/updates/rollouts/rol_2/cancel': { status: 200, body: { ...ROLLOUTS.rollouts[1], state: 'cancelled' } },
    'GET /api/windows': { status: 200, body: { windows: [WINDOW] } },
    'POST /api/windows': (b) => ({ status: 201, body: { ...WINDOW, ...(b as object), id: 'mw_2' } }),
    'POST /api/actions/plan': { status: 404, body: { error: { code: 'not_found', message: 'no' } } },
    ...extra,
  });
  const h = harness(home());
  h.session.state.user!.role = role;
  await flushPromises();
  const w = h.mountIn(UpdatesPane);
  await flushPromises();
  return { ...h, w, hub };
}

const byLabel = (w: Awaited<ReturnType<typeof pane>>['w'], label: string) => w.findAll('button').find((b) => b.attributes('aria-label') === label || b.text().startsWith(label));

describe('Settings → Updates: rollouts', () => {
  it('runs Update all as one rollout, bound to the plan it previewed', async () => {
    const { w, hub } = await pane();
    const all = byLabel(w, 'Update all')!;
    expect(all.text()).toContain('Update all (2)');
    await all.trigger('click');
    await flushPromises();
    expect(hub.last('POST', '/api/updates/rollouts/preview')?.body).toEqual({ targets: [] });
    expect(hub.last('POST', '/api/actions/plan')?.body).toEqual({ action: 'updates.rollout', target: 'chadash:updates', params: { targets: [], hash: PLAN.hash } });
  });

  it('updates a selection, or schedules it into a window', async () => {
    const { w, hub } = await pane();
    await w.find('input[aria-label="Select radarr"]').setValue(true);
    await w.find('select').setValue('mw_1');
    const sel = byLabel(w, 'Schedule selected')!;
    expect(sel.text()).toContain('(1)');
    await sel.trigger('click');
    await flushPromises();
    expect(hub.last('POST', '/api/updates/rollouts/preview')?.body).toEqual({ targets: ['docker:i_a/container/radarr'] });
    expect(hub.last('POST', '/api/actions/plan')?.body).toMatchObject({ params: { targets: ['docker:i_a/container/radarr'], window: 'mw_1' } });
    // A held one can't be picked.
    expect(w.find('input[aria-label="Select lidarr"]').exists()).toBe(false);
  });

  it('shows what each rollout did, and runs or cancels one that waits', async () => {
    const { w, hub } = await pane();
    const ro = w.find('section[aria-labelledby="up-ro"]');
    expect(ro.text()).toContain('Stopped');
    expect(ro.text()).toContain('1 updated · 1 rolled back · 1 didn’t start');
    expect(ro.text()).toContain('sonarr on magpie · rolled back');
    expect(ro.text()).toContain('It didn’t come up healthy.');
    expect(ro.text()).toContain('Waits for you · was for Sunday night');
    await byLabel(w, 'Cancel the rollout was for Sunday night')!.trigger('click');
    await flushPromises();
    expect(hub.last('POST', '/api/updates/rollouts/rol_2/cancel')).toBeTruthy();
    await byLabel(w, 'Run the rollout was for Sunday night now')!.trigger('click');
    await flushPromises();
    expect(hub.last('POST', '/api/actions/plan')?.body).toMatchObject({ action: 'updates.rollout', params: { rollout: 'rol_2' } });
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
  });

  it('lists host packages and Unraid with links, and risks', async () => {
    const { w } = await pane();
    const pve = w.find('section[aria-label="Updates on pve-a"]');
    expect(pve.text()).toContain('Host packages · 2 packages');
    expect(pve.text()).toContain('major');
    expect(pve.find('a[href="https://pve-a.lab.example:8006/"]').text()).toBe('Open in Proxmox');
    expect(pve.find('input[type="checkbox"]').exists()).toBe(false);
    expect(pve.findAll('button').map((b) => b.text())).not.toContain('Hold');
    expect(pve.find('details').text()).toContain('pve-manager 8.4.1 → 9.0.0');
    const cant = w.find('section[aria-labelledby="up-cc"]');
    expect(cant.text()).toContain('jupiter · Unraid 7.1.4');
    expect(cant.find('a[href="https://jupiter.lab.example"]').exists()).toBe(true);
    expect(w.find('section[aria-label="Updates on magpie"]').text()).toContain('minor');
  });

  it('adds a maintenance window, checked before it’s sent', async () => {
    const { w, hub } = await pane();
    const sec = w.findAll('section').find((s) => s.text().startsWith('Maintenance windows'))!;
    expect(sec.text()).toContain('Sunday night');
    expect(sec.text()).toContain('For magpie');
    await byLabel(w, 'Add window')!.trigger('click');
    await flushPromises();
    const dlg = document.querySelector('dialog')!;
    const name = dlg.querySelector<HTMLInputElement>('input:not([type])')!;
    name.value = 'Patch night';
    name.dispatchEvent(new Event('input'));
    dlg.querySelector('form')!.dispatchEvent(new Event('submit'));
    await flushPromises();
    expect(dlg.textContent).toContain('Pick at least one host.');
    expect(hub.last('POST', '/api/windows')).toBeUndefined();
    const pveBox = [...dlg.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')].find((i) => i.value === 'proxmox:i_p/node/pve-a')!;
    pveBox.checked = true;
    pveBox.dispatchEvent(new Event('change'));
    dlg.querySelector('form')!.dispatchEvent(new Event('submit'));
    await flushPromises();
    expect(hub.last('POST', '/api/windows')?.body).toMatchObject({ name: 'Patch night', hosts: ['proxmox:i_p/node/pve-a'], weekly: true });
  });

  it('shows a viewer the list, rollouts and windows without the buttons', async () => {
    const { w } = await pane('viewer');
    expect(byLabel(w, 'Update all')).toBeUndefined();
    expect(byLabel(w, 'Add window')).toBeUndefined();
    expect(w.find('input[aria-label="Select radarr"]').exists()).toBe(false);
    expect(w.text()).toContain('Rollouts');
    expect(w.find('section[aria-labelledby="up-rn"]').exists()).toBe(false);
  });

  it('reads release notes as plain text, linking only into the same repository', async () => {
    const notes = {
      repo: 'https://github.com/acme/sonarr',
      from: '4.0.1',
      newer: true,
      fetched_at: '2026-10-31T12:00:00Z',
      releases: [
        {
          tag: 'v4.1.0', name: 'Four one', url: 'https://github.com/acme/sonarr/releases/tag/v4.1.0', published_at: '2026-10-30T10:00:00Z',
          notes: '<b>Bold</b> fix, see https://github.com/acme/sonarr/pull/9 and https://github.com/acme/other/x and https://evil.example',
        },
      ],
    };
    const { w, hub } = await pane('owner', { 'GET /api/updates/docker%3Ai_a%2Fcontainer%2Fsonarr/notes': { status: 200, body: notes } });
    await byLabel(w, 'Release notes for sonarr')!.trigger('click');
    await flushPromises();
    expect(hub.calls.some((c) => c.path === '/api/updates/docker%3Ai_a%2Fcontainer%2Fsonarr/notes')).toBe(true);
    const region = w.find('[aria-label="Release notes for sonarr"][role="region"]');
    expect(region.text()).toContain('Releases newer than 4.0.1');
    expect(region.find('.up-note-text').text()).toContain('<b>Bold</b> fix');
    expect(region.find('.up-note-text b').exists()).toBe(false);
    const links = region.findAll('.up-note-text a').map((a) => a.attributes('href'));
    expect(links).toEqual(['https://github.com/acme/sonarr/pull/9']);
    // The admin's switch.
    const box = w.find('section[aria-labelledby="up-rn"] input[type="checkbox"]');
    expect((box.element as HTMLInputElement).checked).toBe(true);
    await box.setValue(false);
    await flushPromises();
    expect(hub.last('PUT', '/api/updates/settings')?.body).toEqual({ release_notes: false });
  });
});

describe('Update Center helpers', () => {
  it('links only into the repository', () => {
    const repo = 'https://github.com/acme/app';
    expect(inRepo('https://github.com/acme/app/pull/1', repo)).toBe(true);
    expect(inRepo('https://github.com/acme/app', repo)).toBe(true);
    expect(inRepo('https://github.com/acme/application', repo)).toBe(false);
    expect(inRepo('https://github.com/acme/app/../../evil', repo)).toBe(false);
    expect(inRepo('http://github.com/acme/app/x', repo)).toBe(false);
    expect(inRepo('https://user@github.com/acme/app/x', repo)).toBe(false);
    expect(noteParts('a https://github.com/acme/app/x. b https://github.com/other/y', repo)).toEqual([
      { text: 'a ' },
      { text: 'https://github.com/acme/app/x', href: 'https://github.com/acme/app/x' },
      { text: '. b https://github.com/other/y' },
    ]);
  });

  it('checks a window before it’s sent', () => {
    const ok = { name: 'x', hosts: ['h'], starts_at: '2026-11-01T02:00:00Z', ends_at: '2026-11-01T04:00:00Z', weekly: true };
    expect(windowProblem(ok)).toBe('');
    expect(windowProblem({ ...ok, ends_at: ok.starts_at })).toBe('It has to end after it starts.');
    expect(windowProblem({ ...ok, ends_at: '2026-11-09T04:00:00Z' })).toBe('A weekly window can’t be longer than a week.');
    expect(windowProblem({ ...ok, weekly: false, ends_at: '2026-11-09T04:00:00Z' })).toBe('');
  });
});
