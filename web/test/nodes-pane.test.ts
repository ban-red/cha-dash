/**
 * Settings → Nodes (docs/plan/10-resource-sheet.md, wave 3): each node's
 * state and policy in words, its Docker integration, Revoke with the name
 * typed out, and the pairing token's Regenerate on a hold.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { setDisplayTimeZone } from '../src/lib/format';
import { navigate } from '../src/lib/router';
import NodesPane from '../src/settings/NodesPane.vue';
import { axeViolations } from './axe';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

beforeEach(() => {
  setDisplayTimeZone('UTC');
  navigate('/settings/nodes', { replace: true });
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

/** One engine read through the node `local`, by the Docker integration i_d. */
const home = () => ({
  graph: {
    resources: { 'docker:i_d/engine': { id: 'docker:i_d/engine', kind: 'engine', name: 'magpie', source: 'integration:i_d', state: 'running', health: 'ok', attrs: { node: 'local' } } },
    relations: {},
  },
  metrics: { series: {} },
  inbox: { now: '2026-10-03T14:05:00Z', incidents: {} },
});

describe('Settings → Nodes', () => {
  const NODES = {
    nodes: [
      {
        id: 'n_local',
        name: 'local',
        fingerprint: 'SHA256:abcd',
        state: 'approved',
        enrolled_via: 'local',
        approved_by: 'pairing',
        approved_at: '2026-10-01T10:00:00Z',
        last_seen_at: '2026-10-03T14:04:00Z',
        address: '172.20.0.3',
        version: '0.2.0',
        connected: true,
        hub_key_current: true,
        verbs: [
          { name: 'container.list', policy: 'docker.read', allowed: true },
          { name: 'container.restart', policy: 'docker.lifecycle', allowed: true },
          { name: 'container.remove', policy: 'docker.remove', allowed: false },
        ],
      },
      { id: 'n_old', name: 'attic', fingerprint: 'SHA256:ffff', state: 'revoked', revoked_by: 'owner', revoked_at: '2026-10-02T09:00:00Z', connected: false },
    ],
    hub_pin: 'ed25519:hubkey',
    pairing: true,
  };

  async function pane(body: unknown = NODES) {
    const posts: string[] = [];
    fakeFetch({
      'GET /api/nodes': { status: 200, body },
      'POST /api/nodes/*': (_b, url) => {
        posts.push(url.pathname);
        return url.pathname.endsWith('/regenerate') ? { status: 204 } : { status: 200, body: { ...NODES.nodes[0], state: 'revoked' } };
      },
    });
    const h = harness(home());
    await flushPromises();
    const w = h.mountIn(NodesPane);
    await flushPromises();
    return { ...h, w, posts };
  }

  it('lists each node with its state, and its policy in words', async () => {
    const { w } = await pane();
    const rows = w.findAll('.nd-row');
    expect(rows.map((r) => r.find('.t').text())).toEqual(['local', 'attic']);
    expect(rows[0]!.text()).toContain('connected · 0.2.0 · 172.20.0.3');
    expect(rows[1]!.text()).toContain('revoked by owner');
    await rows[0]!.find('button').trigger('click');
    const detail = w.find('.nd-detail');
    expect(detail.text()).toContain('SHA256:abcd');
    expect(detail.text()).toContain('paired with the hub’s pairing token · approved by pairing');
    const grants = detail.findAll('.nd-grants li').map((li) => [li.find('.g').attributes('aria-label'), li.find('span:not(.g)').text()]);
    expect(grants).toEqual([
      ['allowed', 'Start, stop and restart containers'],
      ['allowed', 'Read containers and the engine'],
      ['refused', 'Remove containers'],
    ]);
    expect(detail.text()).toContain('the hub can’t');
    // Its Docker integration, found by the engine that reads through it.
    expect(detail.find('a.btn').attributes('href')).toBe('/settings/integrations/i_d');
    expect(w.text()).toContain('docker run --rm -v chadash_pairing:/p:ro busybox cat /p/token');
    expect(w.text()).toContain('ed25519:hubkey');
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
  });

  it('revokes only once the name is typed', async () => {
    const { w, posts, ui } = await pane();
    await w.findAll('.nd-row')[0]!.find('button').trigger('click');
    await w.findAll('.nd-detail button').find((b) => b.text() === 'Revoke local')!.trigger('click');
    const confirm = w.find('.nd-confirm');
    const go = confirm.findAll('button').find((b) => b.text() === 'Revoke')!;
    expect(go.attributes('aria-disabled')).toBe('true');
    await confirm.find('input').setValue('loca');
    expect(go.attributes('aria-disabled')).toBe('true');
    await go.trigger('click');
    expect(posts).toEqual([]);
    await confirm.find('input').setValue('local');
    await go.trigger('click');
    await flushPromises();
    expect(posts).toEqual(['/api/nodes/n_local/revoke']);
    expect(ui.state.toasts.at(-1)?.text).toBe('Revoked local. Its key won’t work again.');
  });

  it('regenerates the pairing token on a confirmed hold, and says when pairing is off', async () => {
    const { w, posts } = await pane();
    const hold = () => w.findAll('button').find((b) => (b.attributes('aria-label') ?? '').endsWith('regenerate the pairing token'))!;
    expect(hold().attributes('aria-label')).toBe('Hold to regenerate the pairing token');
    // Without a held press, a click arms a two-step and the next one confirms.
    await hold().trigger('click');
    expect(hold().attributes('aria-label')).toBe('Confirm: regenerate the pairing token');
    await hold().trigger('click');
    await new Promise((r) => setTimeout(r, 500));
    await flushPromises();
    expect(posts).toEqual(['/api/nodes/pairing/regenerate']);

    document.body.innerHTML = '';
    const off = await pane({ nodes: [], hub_pin: 'ed25519:hubkey', pairing: false });
    expect(off.w.text()).toContain('No nodes yet');
    expect(off.w.text()).toContain('Pairing is off on this hub');
  });
});
