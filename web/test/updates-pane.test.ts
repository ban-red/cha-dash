/**
 * Settings → Updates (docs/plan/09-v0.3.md, the Update Center, minimal):
 * newer images by host, Update through the action engine, Hold, Snooze and
 * Release, and what can't be checked, folded away.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { setDisplayTimeZone } from '../src/lib/format';
import { navigate, route } from '../src/lib/router';
import { UPDATE_REVIEW, groupInbox } from '../src/model/inbox';
import UpdatesPane from '../src/settings/UpdatesPane.vue';
import { axeViolations } from './axe';
import { fakeFetch } from './fake-fetch';
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
  graph: { resources: {}, relations: {} },
  metrics: { series: {} },
  inbox: { now: '2026-10-31T12:05:00Z', incidents: {} },
});

const SONARR = 'docker:i_a/container/sonarr';
const row = (name: string, extra: Record<string, unknown>) => ({
  resource: `docker:i_a/container/${name}`,
  name,
  engine: 'docker:i_a/engine',
  host: 'magpie',
  image: `example/${name}:1`,
  current: 'sha256:aaaaaaaaaaaaaaaa',
  latest: 'sha256:bbbbbbbbbbbbbbbb',
  checked_at: '2026-10-31T12:00:00Z',
  ...extra,
});
const UPDATES = {
  updates: [
    row('sonarr', { state: 'available', stack: 'media', update: { id: 'docker.container.update', target: SONARR } }),
    row('radarr', { state: 'held', hold: { kind: 'hold', created_by: 'op', created_at: '2026-10-30T12:00:00Z' }, update: { id: 'docker.container.update', target: 'docker:i_a/container/radarr' } }),
    row('plex', { state: 'current', latest: 'sha256:aaaaaaaaaaaaaaaa' }),
    row('app', { state: 'cant_check', current: undefined, latest: undefined, reason: 'It was built or loaded on this host, so no registry has it.' }),
  ],
};

async function pane(role = 'owner', body: unknown = UPDATES) {
  const hub = fakeFetch({
    'GET /api/updates': { status: 200, body },
    'POST /api/updates/check': { status: 200, body: { asked: 1 } },
    'POST /api/updates/*': (b, url) => ({ status: 200, body: { ...UPDATES.updates[0], resource: decodeURIComponent(url.pathname.split('/')[3]!), state: 'held', hold: { kind: (b as { kind: string }).kind, created_at: '2026-10-31T12:05:00Z' } } }),
    'DELETE /api/updates/*': { status: 200, body: { ...UPDATES.updates[1], state: 'available', hold: undefined } },
    'POST /api/actions/plan': { status: 404, body: { error: { code: 'not_found', message: 'no' } } },
  });
  const h = harness(home());
  h.session.state.user!.role = role;
  await flushPromises();
  const w = h.mountIn(UpdatesPane);
  await flushPromises();
  return { ...h, w, hub };
}

describe('Settings → Updates', () => {
  it('lists what has a newer image by host, held ones marked, and folds away what can’t be checked', async () => {
    const { w } = await pane();
    expect(w.text()).toContain('1 update available · 1 current');
    const rows = w.findAll('section[aria-label="Updates on magpie"] .up-row');
    expect(rows.map((r) => r.find('.t').text())).toEqual(['sonarr media', 'radarr held']);
    expect(rows[0]!.text()).toContain('aaaaaaaaaaaa → bbbbbbbbbbbb');
    expect(rows[1]!.text()).toContain('Held by op');
    expect(rows[1]!.findAll('button').map((b) => b.text())).toEqual(['Release']);
    const cant = w.find('section[aria-labelledby="up-cc"]');
    expect(cant.text()).toContain('Can’t check · 1');
    expect(cant.text()).toContain('built or loaded on this host');
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
  });

  it('updates through the action engine, and holds, snoozes and releases', async () => {
    const { w, hub } = await pane();
    const button = (label: string) => w.findAll('button').find((b) => b.attributes('aria-label') === label)!;
    await button('Update sonarr').trigger('click');
    await flushPromises();
    expect(hub.last('POST', '/api/actions/plan')?.body).toMatchObject({ action: 'docker.container.update', target: SONARR });

    await button('Snooze sonarr\'s update for 7 days').trigger('click');
    await flushPromises();
    expect(hub.last('POST', `/api/updates/${encodeURIComponent(SONARR)}/hold`)?.body).toEqual({ kind: 'snooze' });
    expect(w.findAll('.up-row')[0]!.text()).toContain('snoozed');

    await button('Release radarr').trigger('click');
    await flushPromises();
    expect(hub.last('DELETE', `/api/updates/${encodeURIComponent('docker:i_a/container/radarr')}/hold`)).toBeTruthy();

    await w.findAll('button').find((b) => b.text() === 'Check now')!.trigger('click');
    await flushPromises();
    expect(hub.last('POST', '/api/updates/check')).toBeTruthy();
  });

  it('shows a viewer the list without the buttons', async () => {
    const { w } = await pane('viewer');
    expect(w.findAll('.up-row').length).toBe(3);
    expect(w.findAll('.up-acts button')).toEqual([]);
    expect(w.findAll('button').some((b) => b.text() === 'Check now')).toBe(false);
  });

  it('says what to add when nothing is checked', async () => {
    const { w } = await pane('owner', { updates: [] });
    expect(w.text()).toContain('Nothing checked yet');
    expect(w.text()).toContain('Settings → Nodes');
  });

  it('the Inbox’s quiet hint opens it', async () => {
    const item = {
      id: 'updates.available', scenario: 'updates-available', class: 'problem', severity: 'info', status: 'open', phase: 'open',
      title: '2 updates available', summary: 'New images are out for radarr and sonarr.', target: 'chadash:hub',
      opened_at: '2026-10-31T12:00:00Z', updated_at: '2026-10-31T12:00:00Z', causes: [], blast_radius: [],
      suggested: { id: UPDATE_REVIEW, label: 'Review in Settings → Updates', tier: 'T0', target: 'chadash:hub' }, resolves_when: '',
    };
    const g = groupInbox({ now: '2026-10-31T12:05:00Z', incidents: { [item.id]: item } } as never);
    expect(g.hints.map((i) => i.id)).toEqual(['updates.available']);
    expect(g.open).toEqual([]);
    const { ui } = await pane();
    navigate('/', { replace: true });
    expect(ui.canRun(item as never).ok).toBe(true);
    ui.act(item as never);
    expect(route.value.path).toBe('/settings/updates');
  });
});
