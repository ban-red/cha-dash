import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import InboxSheet from '../src/components/InboxSheet.vue';
import NeedsAttention from '../src/components/NeedsAttention.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { resolve, route, settingsPath, syncRoute } from '../src/lib/router';
import { toPointer } from '../src/lib/jsonpatch';
import { groupInbox, isBeingFixed, isHint } from '../src/model/inbox';
import { findInstance, focusField, repairTarget } from '../src/model/integrations';
import type { Incident } from '../src/model/types';
import { docs, inboxDoc, withIncidents } from './fixtures';
import { engine, refuse } from './fake-engine';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
  vi.setSystemTime(new Date('2026-10-03T14:05:10Z'));
  setDisplayTimeZone('UTC');
  history.replaceState(null, '', '/');
  syncRoute();
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('grouping by status', () => {
  it('puts open problems first (down, then attention), then repairs; in-hand ones apart', () => {
    const g = groupInbox(inboxDoc(withIncidents));
    expect(g.open.map((i) => i.id)).toEqual([
      'nextcloud-maintenance',
      'proxmox-aviary.guest.221',
      'proxmox-wren.backup.daily',
      'backup-failed',
      'proxmox-owl.backup.daily',
      'pbs-datastore-filling',
      'speedtest-slow',
      'host-memory-pressure',
      'karakeep-api-changed',
      'unraid-magpie.repair',
      'plex-token-revoked',
    ]);
    // qBittorrent's phase is "restarting"; the hints are standing advice, in their own group.
    expect(g.fixing.map((i) => i.id)).toEqual(['qbittorrent-oom']);
    expect(g.hints.map((i) => i.id)).toEqual(['adguard.hint.plain_http', 'docker-magpie.hint.docker_endpoint', 'frigate.unauthenticated-admin', 'traefik.hint.api_no_auth']);
    expect(g.resolved).toEqual([]);
  });

  it('counts an accepted fix as being fixed, and resolved as resolved', () => {
    const inbox = inboxDoc(withIncidents);
    inbox.incidents['plex-token-revoked']!.status = 'resolved';
    inbox.incidents['plex-token-revoked']!.resolved_at = '2026-10-03T14:00:00Z';
    const g = groupInbox(inbox, { 'backup-failed': { state: 'accepted' } });
    expect(g.fixing.map((i) => i.id)).toEqual(['backup-failed', 'qbittorrent-oom']);
    expect(g.hints).toHaveLength(4);
    expect(g.resolved.map((i) => i.id)).toEqual(['plex-token-revoked']);
    // Info with nothing working on it is a hint, not "being fixed"; in a fixing phase it's settling.
    const info = { ...inbox.incidents['backup-failed']!, severity: 'info', phase: 'open' } as Incident;
    expect(isBeingFixed(info)).toBe(false);
    expect(isHint(info)).toBe(true);
    expect(isHint({ ...info, phase: 'recovering' })).toBe(false);
    expect(isBeingFixed({ ...info, phase: 'recovering' })).toBe(true);
  });
});

describe('repairs', () => {
  const plex = () => structuredClone(inboxDoc(withIncidents).incidents['plex-token-revoked']!);

  it('know where Reconnect goes: the instance, and the field that failed', () => {
    // The Demo Home's repairs carry the hub's facts: instance, field, status.
    expect(repairTarget(plex())).toEqual({ ref: 'plex', field: 'token', reason: 'auth' });
    expect(repairTarget({ ...plex(), facts: { status: 401 } })).toEqual({ ref: 'plex', reason: 'auth' });
    const real = { ...plex(), target: 'integration:01HZY', facts: { field: 'token', status: 401 } };
    expect(repairTarget(real)).toEqual({ ref: '01HZY', field: 'token', reason: 'auth' });
    const viaFacts = { ...plex(), facts: { instance: 'abc' }, phase: 'api-changed' };
    expect(repairTarget(viaFacts)).toEqual({ ref: 'abc' });
    const list = [
      { id: 'a1', integration: 'plex', name: 'Plex', status: 'repair' },
      { id: 'a2', integration: 'sonarr', name: 'Sonarr 4K', status: 'ok' },
      { id: 'a3', integration: 'sonarr', name: 'Sonarr', status: 'ok' },
    ];
    expect(findInstance(list, 'a2')?.id).toBe('a2');
    expect(findInstance(list, 'plex')?.id).toBe('a1'); // name, case-insensitive
    expect(findInstance(list, 'sonarr')?.id).toBe('a3'); // name before integration
    expect(findInstance(list, 'radarr')).toBeUndefined();
    const schema = { type: 'object' as const, properties: { url: { type: 'string' as const }, token: { type: 'string' as const, writeOnly: true } } };
    expect(focusField(schema, undefined, 'auth')).toBe('token');
    expect(focusField(schema, 'url', 'auth')).toBe('url');
    expect(focusField(schema, undefined, undefined)).toBeUndefined();
  });

  it('offer Reconnect in the Inbox, which opens the integration’s settings on the failing field', async () => {
    const h = harness(docs(withIncidents));
    h.mountIn(InboxSheet);
    h.ui.openInbox('plex-token-revoked');
    await flushPromises();
    await nextTick();
    const item = [...document.body.querySelectorAll('.ib')].find((el) => el.querySelector('.ib-title')?.textContent === 'Plex rejected its token')!;
    expect(item.querySelector('.when')!.textContent).toContain('Repair');
    const reconnect = [...item.querySelectorAll<HTMLButtonElement>('.btn')].find((b) => b.textContent?.startsWith('Reconnect'))!;
    expect(reconnect.classList.contains('primary')).toBe(true);
    reconnect.click();
    expect(h.ui.state.inboxOpen).toBe(false);
    expect(route.value.path).toBe('/settings/integrations/plex');
    expect(route.value.query).toEqual({ field: 'token', reason: 'auth' });
    expect(resolve(route.value)).toEqual({ name: 'settings', pane: 'integrations', id: 'plex' });
  });

  it('show a quick Reconnect while collapsed, and the demo action only in demo mode', async () => {
    const h = harness(docs(withIncidents));
    h.mountIn(InboxSheet);
    h.ui.openInbox();
    await flushPromises();
    const kk = [...document.body.querySelectorAll('.ib')].find((el) => el.querySelector('.ib-title')?.textContent === "Karakeep's API changed")!;
    expect(kk.querySelector('.ib-quick .btn')!.textContent).toBe('Reconnect');
    (kk.querySelector('.ib-title') as HTMLButtonElement).click();
    await nextTick();
    expect([...kk.querySelectorAll('.acts .btn')].map((b) => b.textContent)).toEqual(['ReconnectT0', 'Open KarakeepT0']);

    const d = harness(docs(withIncidents), { demo: true });
    document.body.innerHTML = '';
    d.mountIn(InboxSheet);
    d.ui.openInbox('karakeep-api-changed');
    await flushPromises();
    await nextTick();
    const open = document.body.querySelector('.ib.open')!;
    expect([...open.querySelectorAll('.acts .btn')].map((b) => b.textContent)).toEqual(['ReconnectT0', 'Update the Karakeep integrationT1', 'Open KarakeepT0']);
  });
});

describe('problems outside demo mode', () => {
  it('run through the engine, and never call the demo endpoint', async () => {
    const e = engine();
    const hub = fakeFetch(e.routes);
    const h = harness(docs(withIncidents)); // demo off
    const w = h.mountIn(NeedsAttention);
    await w.find('.attn-more').trigger('click');
    const backup = w.findAll('.strip').find((s) => s.text().includes('Backup of homeassistant failed'))!;
    const act = backup.findAll('.acts button')[1]!;
    expect(act.text()).toBe('Back up homeassistant now T1');
    expect(act.attributes('disabled')).toBeUndefined();
    await act.trigger('click');
    await flushPromises();
    expect(e.plans).toEqual([{ action: 'pve.guest.backup', target: 'pve:heron/qemu/200' }]);
    expect(e.approvals).toHaveLength(1);
    expect(hub.calls.some((c) => c.path.includes('/api/demo/'))).toBe(false);
  });

  it('a 404 from planning is said as is, with no demo fallback outside demo mode', async () => {
    const e = engine({ plan: () => refuse(404, 'not_found', 'Cha Dash doesn’t know how to back up guests on heron.') });
    const hub = fakeFetch(e.routes);
    const h = harness(docs(withIncidents));
    h.ui.act(h.live.inbox.value!.incidents['backup-failed']!);
    await flushPromises();
    expect(h.ui.state.toasts[0]).toMatchObject({ text: 'Cha Dash doesn’t know how to back up guests on heron.', tone: 'error' });
    expect(hub.calls.some((c) => c.path.includes('/api/demo/'))).toBe(false);
  });

  it('open links (T0) anywhere', async () => {
    const open = vi.fn();
    vi.stubGlobal('open', open);
    const h = harness(docs(withIncidents));
    const inc = h.live.inbox.value!.incidents['pbs-datastore-filling']!;
    const link = inc.alternatives!.find((a) => a.id === 'link.open')!;
    expect(h.ui.canRun(inc, link).ok).toBe(true);
    h.ui.act(inc, link);
    expect(open).toHaveBeenCalledWith(expect.stringMatching(/^https?:\/\//), '_blank', 'noopener,noreferrer');
  });
});

describe('the Inbox holds still while you read it', () => {
  it('keeps an item in its group when a fix starts, then re-sorts on the next open', async () => {
    fakeFetch(engine().routes);
    const h = harness(docs(withIncidents), { demo: true });
    h.mountIn(InboxSheet);
    h.ui.openInbox('backup-failed');
    await flushPromises();
    await nextTick();
    const groups = () => [...document.body.querySelectorAll('.ib-grp')].map((g) => g.textContent?.trim());
    expect(groups()).toEqual(['Open · 11', 'Being fixed · 1', 'Security hints · 4']);
    h.ui.act(h.live.inbox.value!.incidents['backup-failed']!);
    await flushPromises();
    h.ws.push({ op: 'diff', id: h.ws.subId('inbox'), seq: 4000, patch: [{ op: 'replace', path: toPointer('inbox', 'incidents', 'backup-failed', 'phase'), value: 'retrying' }] });
    await nextTick();
    // Still under Open while you read: the list holds still.
    expect(groups()).toEqual(['Open · 11', 'Being fixed · 1', 'Security hints · 4']);
    h.ui.closeInbox();
    await nextTick();
    h.ui.openInbox();
    await flushPromises();
    await nextTick();
    expect(groups()).toEqual(['Open · 10', 'Being fixed · 2', 'Security hints · 4']);
  });
});

describe('routes', () => {
  it('resolve to the front page or a settings pane', () => {
    const r = (path: string) => resolve({ path, query: {} });
    expect(r('/')).toEqual({ name: 'front' });
    expect(r('/boards/b2')).toEqual({ name: 'front', board: 'b2' });
    expect(r('/settings')).toEqual({ name: 'settings', pane: 'integrations' });
    expect(r('/settings/import')).toEqual({ name: 'settings', pane: 'import' });
    expect(r('/settings/security')).toEqual({ name: 'settings', pane: 'security' });
    expect(r('/settings/integrations/new')).toEqual({ name: 'settings', pane: 'integrations', add: '' });
    expect(r('/settings/integrations/new/plex')).toEqual({ name: 'settings', pane: 'integrations', add: 'plex' });
    expect(r('/settings/integrations/a%2Fb')).toEqual({ name: 'settings', pane: 'integrations', id: 'a/b' });
    expect(r('/nope')).toEqual({ name: 'front' });
    expect(settingsPath('integrations', 'x y', { field: 'token', reason: undefined })).toBe('/settings/integrations/x%20y?field=token');
  });
});
