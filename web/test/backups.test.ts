/**
 * Backup coverage (docs/plan/14-v0.4.md, "Backups"): the model, Settings →
 * Backups and the coverage widget, all read from the `backups` topic.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { setDisplayTimeZone } from '../src/lib/format';
import { navigate } from '../src/lib/router';
import { groups, objectiveWords, runNow, verifyNow } from '../src/model/backups';
import type { BackupItem, BackupsDoc, GraphDoc } from '../src/model/types';
import BackupsPane from '../src/settings/BackupsPane.vue';
import WidgetHost from '../src/widgets/WidgetHost.vue';
import { preloadWidgets } from '../src/widgets/registry';
import { axeViolations } from './axe';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

const NOW = '2026-10-31T12:00:00Z';
const GUEST = 'proxmox:i_p/qemu/100';
const STORE = 'pbs:i_b/datastore/main';

const daily = { id: 'proxmox:i_p/backup/daily', kind: 'backup_job', name: 'daily', schedule: 'daily 03:00', last_ok: '2026-10-31T03:10:00Z', last_status: 'ok', target: STORE, target_name: 'main' };
const item = (over: Partial<BackupItem>): BackupItem => ({
  resource: GUEST,
  kind: 'vm',
  name: 'pvedocker',
  state: 'fresh',
  by: [daily],
  last_ok: '2026-10-31T03:10:00Z',
  max_age_ms: 26 * 3_600_000,
  objective: 'schedule',
  text: 'Backed up within its objective of 26 hours.',
  message: 'backups.state_fresh',
  ...over,
});

/** An item with no good backup. */
const never = (over: Partial<BackupItem>): BackupItem => {
  const { last_ok: _, ...rest } = item(over);
  return rest;
};

const MAP: BackupsDoc = {
  items: {
    [GUEST]: item({}),
    'docker:i_d/container/paperless': item({
      resource: 'docker:i_d/container/paperless', kind: 'container', name: 'paperless', through: GUEST, through_name: 'pvedocker',
      text: 'Backed up within its objective of 26 hours. Through pvedocker’s backup.',
    }),
    'proxmox:i_p/lxc/101': item({
      resource: 'proxmox:i_p/lxc/101', kind: 'lxc', name: 'dns', state: 'stale', last_ok: '2026-10-29T03:10:00Z', objective: 'override', max_age_ms: 24 * 3_600_000,
      text: 'Its newest good backup is older than its objective of 24 hours.', message: 'backups.state_stale',
      cause: { text: 'Its backups go to main, which isn’t ok: 92% used.', resource: STORE, message: 'backups.cause_target' },
    }),
    'unraid:i_u/share/appdata': never({ resource: 'unraid:i_u/share/appdata', kind: 'share', name: 'appdata', state: 'uncovered', by: [], objective: 'default', text: 'Nothing backs it up.', message: 'backups.state_uncovered' }),
  },
};

const GRAPH: GraphDoc = {
  resources: {
    [GUEST]: { id: GUEST, kind: 'vm', name: 'pvedocker', source: 'integration:i_p', attrs: { vmid: 100 } },
    'proxmox:i_p/lxc/101': { id: 'proxmox:i_p/lxc/101', kind: 'lxc', name: 'dns', source: 'integration:i_p', attrs: { vmid: 101 } },
    [STORE]: { id: STORE, kind: 'backup_target', name: 'main' },
  },
  relations: {},
};

const home = (backups: BackupsDoc | undefined = MAP) => ({
  graph: GRAPH,
  metrics: { series: {} },
  inbox: { now: NOW, incidents: {} },
  ...(backups ? { backups } : {}),
});

beforeEach(() => {
  setDisplayTimeZone('UTC');
  vi.useFakeTimers({ toFake: ['Date'] });
  vi.setSystemTime(new Date(NOW));
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('the coverage model', () => {
  it('groups what needs you first, and words objectives', () => {
    expect(groups(MAP).map((g) => [g.state, g.items.map((i) => i.name)])).toEqual([
      ['stale', ['dns']],
      ['uncovered', ['appdata']],
      ['fresh', ['paperless', 'pvedocker']],
    ]);
    expect([objectiveWords(26 * 3_600_000), objectiveWords(8 * 86_400_000), objectiveWords(3_600_000)]).toEqual(['26 hours', '8 days', '1 hour']);
  });

  it('runs a container’s backup on its guest, and verifies the guest’s group on its datastore', () => {
    const ctr = MAP.items['docker:i_d/container/paperless']!;
    expect(runNow(ctr, GRAPH)).toEqual({ action: 'proxmox.guest.backup', target: GUEST, label: 'Back up pvedocker now' });
    expect(verifyNow(ctr, GRAPH)).toEqual({ action: 'pbs.datastore.verify', target: STORE, label: 'Verify pvedocker’s backups on main', params: { group: 'vm/100' } });
    expect(verifyNow(MAP.items['proxmox:i_p/lxc/101']!, GRAPH)?.params).toEqual({ group: 'ct/101' });
    const share = MAP.items['unraid:i_u/share/appdata']!;
    expect([runNow(share, GRAPH), verifyNow(share, GRAPH)]).toEqual([undefined, undefined]);
  });
});

async function pane(role = 'owner', backups: BackupsDoc | undefined = MAP) {
  navigate('/settings/backups', { replace: true });
  const hub = fakeFetch({
    'PUT /api/backups/objectives/*': (b, url) => ({ status: 200, body: { resource: decodeURIComponent(url.pathname.split('/')[4]!), max_age_ms: (b as { max_age_ms: number }).max_age_ms, set_at: NOW } }),
    'DELETE /api/backups/objectives/*': { status: 200, body: { resource: 'proxmox:i_p/lxc/101', max_age_ms: 86_400_000, set_at: NOW } },
  });
  const h = harness(home(backups));
  h.session.state.user!.role = role;
  await flushPromises();
  const w = h.mountIn(BackupsPane);
  await flushPromises();
  return { ...h, w, hub };
}

describe('Settings → Backups', () => {
  it('lists the map by state, with what covers each and why it’s stale', async () => {
    const { w } = await pane();
    expect(w.find('.bk-sum').text()).toBe('1 stale · 1 not covered · 2 fresh');
    expect(w.findAll('h3').map((h) => h.text())).toEqual(['Backup coverage', 'Covered but stale · 1', 'Not covered · 1', 'Covered and fresh · 2']);
    const stale = w.find('section[aria-labelledby="bk-stale"]');
    expect(stale.text()).toContain('own objective');
    expect(stale.text()).toContain('daily → main');
    expect(stale.text()).toContain('Its backups go to main, which isn’t ok: 92% used.');
    const fresh = w.find('section[aria-labelledby="bk-fresh"]');
    expect(fresh.text()).toContain('through pvedocker’s backup');
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
  });

  it('runs and verifies through the action engine', async () => {
    const { w, ui } = await pane();
    const run = vi.spyOn(ui, 'runAction').mockResolvedValue(undefined as never);
    await w.find('button[aria-label="Back up dns now"]').trigger('click');
    expect(run).toHaveBeenLastCalledWith({ action: 'proxmox.guest.backup', target: 'proxmox:i_p/lxc/101', label: 'Back up dns now', tier: 'T1' });
    await w.find('button[aria-label="Verify dns’s backups on main"]').trigger('click');
    expect(run).toHaveBeenLastCalledWith({ action: 'pbs.datastore.verify', target: STORE, label: 'Verify dns’s backups on main', tier: 'T1', params: { group: 'ct/101' } });
  });

  it('sets and clears an objective (admin and up)', async () => {
    const { w, hub } = await pane();
    const select = w.find('select[aria-label="Objective for pvedocker"]');
    await select.setValue('50');
    await flushPromises();
    expect(hub.last('PUT', `/api/backups/objectives/${encodeURIComponent(GUEST)}`)?.body).toEqual({ max_age_ms: 50 * 3_600_000 });
    const dns = w.find('select[aria-label="Objective for dns"]');
    expect((dns.element as HTMLSelectElement).value).toBe('24');
    await dns.setValue('');
    await flushPromises();
    expect(hub.last('DELETE', `/api/backups/objectives/${encodeURIComponent('proxmox:i_p/lxc/101')}`)).toBeTruthy();
    // Nothing to hold an uncovered share to.
    expect(w.find('select[aria-label="Objective for appdata"]').exists()).toBe(false);
  });

  it('a viewer reads the map but neither acts nor sets objectives', async () => {
    const { w } = await pane('viewer');
    expect(w.findAll('select').length).toBe(0);
    expect(w.text()).not.toContain('Run now');
    expect(w.text()).toContain('Covered but stale');
  });

  it('says so when there’s nothing to back up', async () => {
    const { w } = await pane('owner', { items: {} });
    expect(w.text()).toContain('Nothing to back up yet');
  });
});

describe('the coverage widget', () => {
  const tile = async (size: string, backups: BackupsDoc | undefined = MAP) => {
    await preloadWidgets();
    const h = harness(home(backups));
    const w = h.mountIn(WidgetHost, { item: { id: 'coverage', type: 'coverage' }, size });
    await flushPromises();
    return w;
  };

  it('says what needs you at every size', async () => {
    const small = await tile('small');
    expect(small.find('.v').text()).toBe('2 of 4 fresh');
    expect(small.text()).toContain('1 stale · 1 not covered');
    expect(small.find('article.tile').classes()).toContain('is-attn');
    const tall = await tile('tall');
    expect(tall.findAll('.li .t').map((t) => t.text())).toEqual(['dns', 'appdata']);
    const large = await tile('large');
    expect(large.find('.kv').text()).toContain('Covered but stale1');
    // axe: test/a11y-widgets-*.test.ts sweeps every size and state.
  });

  it('is quiet when everything is fresh', async () => {
    const fresh = { items: { [GUEST]: item({}) } };
    const w = await tile('strip', fresh);
    expect(w.text()).toContain('all fresh');
    expect(w.find('article.tile').classes()).not.toContain('is-attn');
  });
});
