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

describe('Settings → Nodes: remote nodes', () => {
  const TOKEN = {
    id: 'njt_1',
    name: 'attic',
    add_docker: true,
    created_by: 'u1',
    created_by_name: 'owner',
    created_at: '2026-10-03T13:50:00Z',
    expires_at: '2026-10-03T14:05:00Z',
    used_at: '2026-10-03T13:55:00Z',
    node_id: 'n_attic',
    node_name: 'attic',
    state: 'used',
  };
  const PENDING = {
    id: 'n_attic',
    name: 'attic',
    fingerprint: '1a2b:3c4d:5e6f:7a8b:9c0d:1e2f:3a4b:5c6d',
    state: 'pending',
    enrolled_via: 'token',
    created_at: '2026-10-03T13:55:00Z',
    last_seen_at: '2026-10-03T14:04:30Z',
    address: '198.51.100.7',
    version: '0.3.0',
    connected: false,
    verbs: [
      { name: 'container.list', policy: 'docker.read', allowed: true },
      { name: 'stack.deploy', policy: 'stacks.deploy', allowed: false },
    ],
    join_token: TOKEN,
  };
  const ACTIVE = { ...TOKEN, id: 'njt_2', name: '', state: 'active', used_at: undefined, node_id: undefined, node_name: undefined, expires_at: '2026-10-03T14:20:00Z' };

  async function pane(routes: Parameters<typeof fakeFetch>[0] = {}) {
    const f = fakeFetch({
      'GET /api/nodes': { status: 200, body: { nodes: [PENDING], hub_pin: 'ed25519:hubkey', pairing: true } },
      'GET /api/nodes/tokens': { status: 200, body: { tokens: [ACTIVE, TOKEN] } },
      ...routes,
    });
    const h = harness(home());
    await flushPromises();
    const w = h.mountIn(NodesPane);
    await flushPromises();
    return { ...h, w, f };
  }
  const button = (w: Awaited<ReturnType<typeof pane>>['w'], text: string) => w.findAll('button').find((b) => b.text().replace(/\s+/g, ' ').trim().startsWith(text));

  it('shows a pending node with what an admin needs to approve it', async () => {
    const { w } = await pane();
    const card = w.find('.nd-pending');
    expect(card.find('h4').text()).toBe('attic wants to join');
    expect(card.text()).toContain('1a2b:3c4d:5e6f:7a8b:9c0d:1e2f:3a4b:5c6d');
    expect(card.text()).toContain('Compare it with the node’s log');
    expect(card.text()).toContain('198.51.100.7');
    expect(card.text()).toContain('0.3.0');
    expect(card.text()).toContain('made by owner');
    expect(card.text()).toContain('Its Docker integration is added when you approve it.');
    const grants = card.findAll('.nd-grants li').map((li) => [li.find('.g').attributes('aria-label'), li.find('span:not(.g)').text()]);
    expect(grants).toEqual([
      ['allowed', 'Read containers and the engine'],
      ['refused', 'Deploy and roll back stacks'],
    ]);
    // Pending nodes aren't in the list below, and don't count as nodes yet.
    expect(w.findAll('.nd-row .t').map((t) => t.text())).not.toContain('attic');
    expect(w.find('#nd-h + .st-meta').text()).toContain('0 nodes · 1 waiting');
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
  });

  it('approves with a step-up, and rejects after a confirm', async () => {
    let tries = 0;
    const { w, f, ui } = await pane({
      'POST /api/nodes/n_attic/approve': () =>
        ++tries === 1 ? { status: 403, body: { error: { code: 'step_up_required', message: 'Confirm it’s you first.' } } } : { status: 200, body: { ...PENDING, state: 'approved' } },
      'POST /api/nodes/n_attic/revoke': { status: 200, body: { ...PENDING, state: 'revoked' } },
    });
    const approving = button(w, 'Approve attic')!.trigger('click');
    await flushPromises();
    expect(ui.state.stepUp?.reason).toBe('Approve the node attic');
    ui.stepUpDone(true);
    await approving;
    await flushPromises();
    expect(f.calls.filter((c) => c.path === '/api/nodes/n_attic/approve')).toHaveLength(2);
    expect(ui.state.toasts.at(-1)?.text).toBe('Approved attic. It connects on its next try, within 30 seconds.');

    await button(w, 'Reject')!.trigger('click');
    expect(f.calls.some((c) => c.path === '/api/nodes/n_attic/revoke')).toBe(false);
    const confirm = w.find('.nd-pending .st-confirm');
    expect(confirm.text()).toContain('Its key never works on this hub again');
    await confirm.findAll('button').find((b) => b.text() === 'Reject attic')!.trigger('click');
    await flushPromises();
    expect(f.calls.filter((c) => c.path === '/api/nodes/n_attic/revoke')).toHaveLength(1);
    expect(ui.state.toasts.at(-1)?.text).toBe('Rejected attic. Its key won’t work again.');
  });

  it('says why an approve failed, and stays put when the step-up is cancelled', async () => {
    const { w, ui } = await pane({
      'POST /api/nodes/n_attic/approve': { status: 409, body: { error: { code: 'not_pending', message: 'That node isn’t waiting for approval.' } } },
    });
    await button(w, 'Approve attic')!.trigger('click');
    await flushPromises();
    expect(w.find('.nd-pending .err').text()).toBe('That node isn’t waiting for approval.');
    expect(ui.state.stepUp).toBeUndefined();
  });

  it('makes a join token once, with the .env block and an editable hub address', async () => {
    const secret = 'cdj_FAKEsecretFAKEsecretFAKEsecretFAKEsecret00.ed25519:hubkey';
    const { w, f } = await pane({
      'POST /api/nodes/tokens': (body) => ({
        status: 201,
        body: {
          token: { ...ACTIVE, id: 'njt_3', name: (body as { name: string }).name, add_docker: (body as { add_docker: boolean }).add_docker },
          secret,
          hub_url: 'ws://hub.test:3696/node',
          env: `CHADASH_HUB=ws://hub.test:3696/node\nCHADASH_NODE_NAME=pi\nCHADASH_NODE_TOKEN=${secret}\n`,
          command: 'docker compose -f compose.node.yaml up -d',
        },
      }),
    });
    await button(w, 'Add node')!.trigger('click');
    await vi.dynamicImportSettled(); // AddNode is its own chunk
    await flushPromises();
    const form = w.find('form.nd-add');
    expect(document.activeElement?.id).toBe(form.find('input[type="text"]').attributes('id'));
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
    await form.find('input[type="text"]').setValue('bad name');
    await form.trigger('submit');
    await flushPromises();
    expect(form.text()).toContain('Use 1–63 letters');
    expect(f.calls.some((c) => c.method === 'POST')).toBe(false);

    await form.find('input[type="text"]').setValue('pi');
    await form.find('select').setValue('60');
    await form.find('input[type="checkbox"]').setValue(false);
    await form.trigger('submit');
    await flushPromises();
    expect(f.last('POST', '/api/nodes/tokens')?.body).toEqual({ name: 'pi', ttl_minutes: 60, add_docker: false });

    const made = w.find('.nd-made');
    expect(made.find('h4').text()).toBe('Set up pi now');
    expect(made.text()).toContain('This is the only time Cha Dash shows the token');
    const env = () => made.findAll('pre')[0]!.text();
    expect(env()).toBe(`CHADASH_HUB=ws://hub.test:3696/node\nCHADASH_NODE_NAME=pi\nCHADASH_NODE_TOKEN=${secret}`);
    expect(made.findAll('pre')[1]!.text()).toBe('docker compose -f compose.node.yaml up -d');
    // The admin points it through their proxy instead.
    await made.find('input').setValue('wss://dash.lab.example/node');
    expect(env()).toContain('CHADASH_HUB=wss://dash.lab.example/node\n');
    await made.find('input').setValue('https://dash.lab.example');
    expect(made.text()).toContain('Use ws://host:port/node');
    // The new token joins the list.
    expect(w.findAll('.nd-tokens .t').map((t) => t.text())).toContain('For pi');
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);

    await button(w, 'Done')!.trigger('click');
    await flushPromises();
    expect(w.find('.nd-made').exists()).toBe(false);
    expect(w.text()).not.toContain(secret);
  });

  it('lists join tokens and revokes an unused one', async () => {
    const { w, f, ui } = await pane({
      'DELETE /api/nodes/tokens/njt_2': { status: 200, body: { ...ACTIVE, state: 'revoked' } },
    });
    const rows = w.findAll('.nd-tokens li');
    expect(rows.map((r) => r.find('.t').text())).toEqual(['For any name', 'For attic']);
    expect(rows[0]!.text()).toContain('unused · expires in 15m');
    expect(rows[1]!.text()).toContain('used by attic');
    expect(rows[1]!.find('button').exists()).toBe(false);
    await rows[0]!.find('button').trigger('click');
    await w.findAll('.nd-tokens button').find((b) => b.text() === 'Revoke token')!.trigger('click');
    await flushPromises();
    expect(f.calls.filter((c) => c.method === 'DELETE').map((c) => c.path)).toEqual(['/api/nodes/tokens/njt_2']);
    expect(ui.state.toasts.at(-1)?.text).toBe('Revoked the join token for any name. It won’t enroll a node.');
  });

  it('reads again while a node may turn up, and stops once nothing waits', async () => {
    // The token gets used, the node waits, then someone approves it elsewhere.
    let phase: 'token' | 'pending' | 'approved' = 'token';
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const { w, f } = await pane({
      'GET /api/nodes': () => ({ status: 200, body: { nodes: phase === 'token' ? [] : [{ ...PENDING, state: phase }], hub_pin: 'ed25519:hubkey', pairing: true } }),
      'GET /api/nodes/tokens': () => ({ status: 200, body: { tokens: phase === 'token' ? [ACTIVE] : [TOKEN] } }),
    });
    const reads = () => f.calls.filter((c) => c.path === '/api/nodes').length;
    try {
      expect(w.find('.nd-pending').exists()).toBe(false);
      phase = 'pending';
      await vi.advanceTimersByTimeAsync(5000);
      await flushPromises();
      expect(w.find('.nd-pending h4').text()).toBe('attic wants to join');
      phase = 'approved';
      await vi.advanceTimersByTimeAsync(5000);
      await flushPromises();
      expect(w.find('.nd-pending').exists()).toBe(false);
      expect(w.findAll('.nd-row .t').map((t) => t.text())).toContain('attic');
      const before = reads();
      await vi.advanceTimersByTimeAsync(20000);
      await flushPromises();
      expect(reads()).toBe(before);
      w.unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  it('opens Settings → Nodes from the pending node’s Inbox item', async () => {
    fakeFetch({});
    const h = harness(home());
    await flushPromises();
    navigate('/', { replace: true });
    const action = { id: 'node.review', label: 'Review in Settings → Nodes', tier: 'T0' as const, target: 'node:n_attic' };
    const incident = { id: 'node.n_attic.pending', class: 'node' as const, severity: 'attention' as const, status: 'open' as const, title: 'attic wants to join', summary: '', target: 'node:n_attic', opened_at: '2026-10-03T13:55:00Z', suggested: action };
    expect(h.ui.canRun(incident, action)).toEqual({ ok: true });
    h.ui.act(incident, action);
    expect(window.location.pathname).toBe('/settings/nodes');
  });
});
