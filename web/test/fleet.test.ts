/**
 * Fleet stacks in Settings → Stacks (wave 14): Move with its review (the
 * rewrites line by line, what the first deploy recreates, a refusal, a hub
 * that can't be asked), Un-adopt and "A copy stays at …" on a stack's page,
 * and Update Cha Dash (the tag, the T3 ladder, the task across the hub's
 * restart, the last result). The hub's side is faked to the routes in
 * internal/stacks/fleet_http.go.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import ConfirmDialog from '../src/components/ConfirmDialog.vue';
import StacksPane from '../src/settings/StacksPane.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { toPointer } from '../src/lib/jsonpatch';
import { navigate } from '../src/lib/router';
import type { StackDetail, StackNode } from '../src/settings/stacks';
import type { MovePreview, SelfUpdateStatus } from '../src/settings/fleet';
import { shortImage, tagProblem } from '../src/settings/fleet';
import type { Task } from '../src/model/types';
import { axeViolations } from './axe';
import { engine, planFor, refuse, taskFor } from './fake-engine';
import { fakeFetch, type Handler } from './fake-fetch';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

beforeEach(() => {
  setDisplayTimeZone('UTC');
  navigate('/', { replace: true });
});
const mounted: { unmount: () => void }[] = [];
afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

async function settle(): Promise<void> {
  await vi.dynamicImportSettled();
  await flushPromises();
  await nextTick();
}

const NODES: StackNode[] = [
  {
    node: 'local',
    name: 'jupiter',
    online: true,
    error: null,
    root: '/opt/stacks',
    stacks: [{ name: 'dns', status: 'running', services: 1, running: 1, location: 'external', path: '/srv/dns', protected: false, drift: false, draft: false, head: null }],
    detected: [
      { name: 'pihole', status: 'running', services: 1, running: 1, path: '/srv/pihole', config_files: ['/srv/pihole/docker-compose.yml'], adoptable: true, reason: null, moveable: true },
      { name: 'apps', status: 'running', services: 1, running: 1, path: '/opt/stacks/apps', config_files: ['/opt/stacks/apps/compose.yaml'], adoptable: true, reason: null, moveable: false, move_refusal: 'It’s already in the stacks root: adopt it in place.' },
    ],
  },
];

const PREVIEW: MovePreview = {
  ok: true,
  rewrites: [
    { line: 7, from: './etc:/etc/pihole', to: '/srv/pihole/etc:/etc/pihole' },
    { line: 9, from: './dnsmasq.d:/etc/dnsmasq.d', to: '/srv/pihole/dnsmasq.d:/etc/dnsmasq.d' },
  ],
  plan: { services: [{ name: 'dns', action: 'recreate', image: 'pihole/pihole:2026.10', reason: 'its config changed' }], downtime_s: 1.5 },
  path: '/opt/stacks/pihole',
  refusal: null,
};

const HUB_NOW = 'ghcr.io/ban-red/chadash:0.2.0@sha256:' + 'a'.repeat(64);
const HUB_NEXT = 'ghcr.io/ban-red/chadash:0.3.0@sha256:' + 'b'.repeat(64);

const STATUS: SelfUpdateStatus = {
  node: 'local',
  allowed: true,
  available: true,
  running: HUB_NOW,
  last: { outcome: 'rolled_back', previous: HUB_NOW, current: HUB_NOW, image: 'ghcr.io/ban-red/chadash:0.2.1', reason: 'container chadash-chadash-1 is unhealthy. Rolled back on its own.', finished_at: '2026-10-29T12:00:00Z' },
};

function dns(over: Partial<StackDetail> = {}): StackDetail {
  return {
    node: 'local',
    name: 'dns',
    path: '/srv/dns',
    file: 'compose.yaml',
    location: 'external',
    managed: true,
    protected: false,
    protected_reason: null,
    drift: false,
    status: 'running',
    content: 'services:\n  dns:\n    image: pihole/pihole:2026.10\n',
    env: { present: false, keys: [] },
    meta: {},
    services: [{ name: 'dns', image: 'pihole/pihole:2026.10', state: 'running', health: 'healthy', restarts: 0 }],
    history: [{ commit: 'd'.repeat(40), short: 'ddddddd', subject: 'Adopt dns in place at /srv/dns', at: '2026-10-01T12:00:00Z', author: 'owner', kind: 'adopt' }],
    draft: null,
    secrets: [],
    moved_from: null,
    ...over,
  };
}

function hub(over: Record<string, Handler | { status: number; body?: unknown }> = {}) {
  const e = engine({
    plan: (action) =>
      action === 'chadash.update'
        ? { tier: 'T3', confirm: 'type', confirm_text: 'Cha Dash', label: 'Update Cha Dash to 0.3.0', steps: [{ label: 'Pull ghcr.io/ban-red/chadash:0.3.0 on jupiter and pin the digest it points at' }] }
        : action === 'stack.move'
          ? { tier: 'T1', confirm: 'undo', label: 'Move pihole into the stacks root', undo: { action: 'stack.unadopt', label: 'Un-adopt pihole (both copies stay)' } }
          : undefined,
  });
  const f = fakeFetch({
    ...e.routes,
    'GET /api/stacks': { status: 200, body: { nodes: NODES } },
    'GET /api/stacks/secrets': { status: 200, body: { secrets: [] } },
    'GET /api/stacks/local/dns': { status: 200, body: dns() },
    'POST /api/stacks/local/dns/validate': { status: 200, body: { ok: true, messages: [], secrets: [], plan: null, diff: [] } },
    'POST /api/stacks/local/pihole/move/preview': { status: 200, body: PREVIEW },
    'GET /api/nodes/local/self-update': { status: 200, body: STATUS },
    ...over,
  });
  return { e, f };
}

function open(path: string) {
  navigate(path, { replace: true });
  const h = harness(docs(withIncidents));
  const mountIn: typeof h.mountIn = (c, p) => {
    const w = h.mountIn(c, p);
    mounted.push(w);
    return w;
  };
  return { ...h, mountIn };
}

describe('Settings → Stacks: Move', () => {
  it('reviews the move (rewrites line by line, what the first deploy recreates) and runs stack.move at T1', async () => {
    const { e, f } = hub();
    const h = open('/settings/stacks');
    const w = h.mountIn(StacksPane);
    await settle();
    const rows = w.findAll('div.sk-row');
    // Only a project the node can move offers it.
    expect(rows[0]!.find('button[aria-label="Move pihole into the stacks root"]').exists()).toBe(true);
    expect(rows[1]!.find('button[aria-label="Move apps into the stacks root"]').exists()).toBe(false);

    await rows[0]!.find('button[aria-label="Move pihole into the stacks root"]').trigger('click');
    await settle();
    expect(f.last('POST', '/api/stacks/local/pihole/move/preview')?.body).toEqual({ from_path: '/srv/pihole/docker-compose.yml' });
    const dlg = document.querySelector('dialog.skm')!;
    expect(dlg).not.toBeNull();
    const lines = [...dlg.querySelectorAll('.skm-rewrites li')].map((li) => li.textContent!.replace('→', '').replace(/\s+/g, ' ').trim());
    expect(lines).toEqual(['Line 7 was ./etc:/etc/pihole becomes /srv/pihole/etc:/etc/pihole', 'Line 9 was ./dnsmasq.d:/etc/dnsmasq.d becomes /srv/pihole/dnsmasq.d:/etc/dnsmasq.d']);
    expect(dlg.textContent).toContain('The next deploy recreates dns');
    expect(dlg.textContent).toContain('A copy stays at /srv/pihole/docker-compose.yml; the tool that wrote it may still deploy it');
    expect(dlg.querySelector('.sk-table tbody')!.textContent).toContain('recreate');
    expect(e.plans).toEqual([]);
    expect(await axeViolations()).toEqual([]);

    const moveBtn = [...dlg.querySelectorAll('button')].find((b) => b.textContent?.startsWith('Move pihole'))!;
    moveBtn.click();
    await settle();
    expect(e.plans).toEqual([{ action: 'stack.move', target: 'stack:local/pihole', params: { from_path: '/srv/pihole/docker-compose.yml' } }]);
    expect(e.approvals).toHaveLength(1);
    expect(h.ui.state.toasts.at(-1)?.action?.label).toBe('Undo');
    expect(document.querySelector('dialog.skm')).toBeNull();
  });

  it('shows a refusal as the review’s answer, and a hub that couldn’t be asked with Try again', async () => {
    let n = 0;
    hub({
      'POST /api/stacks/local/pihole/move/preview': () =>
        ++n === 1
          ? refuse(502, 'node_error', 'The node couldn’t do that: dial timeout.')
          : { status: 200, body: { ok: false, rewrites: [], plan: null, path: '', refusal: { code: 'exists', message: '/opt/stacks/pihole already exists.' } } },
    });
    const h = open('/settings/stacks');
    const w = h.mountIn(StacksPane);
    await settle();
    await w.find('button[aria-label="Move pihole into the stacks root"]').trigger('click');
    await settle();
    const dlg = () => document.querySelector('dialog.skm')!;
    expect(dlg().querySelector('.st-note.bad')!.textContent).toContain('dial timeout');
    const primary = () => [...dlg().querySelectorAll('button')].find((b) => b.textContent?.startsWith('Move pihole')) as HTMLButtonElement;
    expect(primary().disabled).toBe(true);
    ([...dlg().querySelectorAll('button')].find((b) => b.textContent === 'Try again') as HTMLButtonElement).click();
    await settle();
    expect(dlg().querySelector('.skm-refusal')!.textContent).toContain('It can’t be moved. /opt/stacks/pihole already exists.');
    expect(primary().disabled).toBe(true);
    expect(await axeViolations()).toEqual([]);
    // Cancel closes it; nothing ran.
    ([...dlg().querySelectorAll('button')].find((b) => b.textContent === 'Cancel') as HTMLButtonElement).click();
    await settle();
    expect(document.querySelector('dialog.skm')).toBeNull();
  });
});

describe('Settings → Stacks: one stack’s place', () => {
  it('a stack adopted in place offers Move and Un-adopt', async () => {
    const { e } = hub({ 'POST /api/stacks/local/dns/move/preview': { status: 200, body: { ...PREVIEW, rewrites: [], plan: { services: [], downtime_s: 0 } } } });
    const h = open('/settings/stacks/local/dns');
    const w = h.mountIn(StacksPane);
    await settle();
    await settle();
    expect(w.find('.skg').text()).toContain('Adopted where it is');
    expect(w.find('button[aria-label="Move dns into the stacks root"]').exists()).toBe(true);
    await w.find('.skg button[aria-label^="Un-adopt dns"]').trigger('click');
    await settle();
    expect(e.plans).toEqual([{ action: 'stack.unadopt', target: 'stack:local/dns' }]);
  });

  it('a moved stack says where the old copy is', async () => {
    hub({ 'GET /api/stacks/local/dns': { status: 200, body: dns({ location: 'root', path: '/opt/stacks/dns', moved_from: '/srv/dns/compose.yaml' }) } });
    const h = open('/settings/stacks/local/dns');
    const w = h.mountIn(StacksPane);
    await settle();
    await settle();
    expect(w.find('.skg-copy').text().replace(/\s+/g, ' ')).toBe('A copy stays at /srv/dns/compose.yaml; the tool that wrote it may still deploy it.');
    expect(w.find('button[aria-label="Move dns into the stacks root"]').exists()).toBe(false);
    expect(w.find('.skg').text()).toContain('Un-adopting stops versioning dns');
    expect(await axeViolations()).toEqual([]);
  });

  it('a protected stack offers neither', async () => {
    hub({ 'GET /api/stacks/local/dns': { status: 200, body: dns({ protected: true }) } });
    const h = open('/settings/stacks/local/dns');
    const w = h.mountIn(StacksPane);
    await settle();
    await settle();
    expect(w.find('.skg').exists()).toBe(false);
  });
});

describe('Settings → Stacks: Update Cha Dash', () => {
  it('shows what runs and the last result, checks the tag, and plans chadash.update through the typed T3 ladder', async () => {
    const { e, f } = hub();
    const h = open('/settings/stacks');
    const w = h.mountIn(StacksPane);
    h.mountIn(ConfirmDialog);
    await settle();
    expect(f.calls.some((c) => c.path === '/api/nodes/local/self-update')).toBe(true);
    const sec = w.find('.sku');
    expect(sec.find('h3').text()).toBe('Cha Dash itself');
    expect(sec.text()).toContain('on jupiter');
    expect(sec.find('.st-lede').text()).toContain('0.2.0 · sha256:aaaaaaaa');
    expect(sec.find('.sku-last').text().replace(/\s+/g, ' ')).toContain('Last update: Rolled back on its own · 0.2.1');
    expect(await axeViolations()).toEqual([]);

    // A digest, or another image, isn't a tag.
    const input = sec.find('input');
    await input.setValue('ghcr.io/ban-red/chadash-node:0.3.0');
    await sec.find('form').trigger('submit');
    await settle();
    expect(sec.find('[role="alert"]').text()).toContain('Only ghcr.io/ban-red/chadash can be updated here');
    expect(e.plans).toEqual([]);

    await input.setValue('0.3.0');
    await sec.find('form').trigger('submit');
    await settle();
    expect(e.plans).toEqual([{ action: 'chadash.update', target: 'node:local', params: { image: '0.3.0' } }]);
    // T3: nothing runs until "Cha Dash" is typed.
    expect(h.ui.state.confirm?.kind).toBe('type');
    expect(e.approvals).toHaveLength(0);
    await h.ui.confirmed('Cha Dash');
    await settle();
    expect(e.approvals).toEqual([expect.objectContaining({ typed: 'Cha Dash' })]);
  });

  it('follows the task while the hub restarts, then shows how it ended and asks for the result again', async () => {
    const { f } = hub();
    const h = open('/settings/stacks');
    const w = h.mountIn(StacksPane);
    await settle();
    const p = planFor('chadash.update', 'node:local', { label: 'Update Cha Dash to 0.3.0', tier: 'T3' });
    const t: Task = taskFor(p, { step: 'Recreate the hub with ' + HUB_NEXT, progress: null });
    h.ws.push({ op: 'diff', id: h.ws.subId('tasks'), seq: 2, patch: [{ op: 'add', path: toPointer('tasks', 'tasks', t.id), value: t }] });
    await settle();
    const live = w.find('.sku .sk-task');
    expect(live.classes()).toContain('live');
    expect(live.text()).toContain('Update Cha Dash to 0.3.0');
    expect(live.text()).toContain('Recreate the hub with');
    expect(live.find('[role="status"]').text()).toContain('this page loses the hub for a moment');
    expect(w.find('.sku input').attributes('disabled')).toBeDefined();
    expect(await axeViolations()).toEqual([]);

    const before = f.calls.filter((c) => c.path === '/api/nodes/local/self-update').length;
    h.ws.push({
      op: 'diff',
      id: h.ws.subId('tasks'),
      seq: 3,
      patch: [{ op: 'replace', path: toPointer('tasks', 'tasks', t.id), value: { ...t, state: 'failed', error: { code: 'rolled_back', message: 'The new version failed its health check, so the node put the previous one back on its own: container chadash-chadash-1 is unhealthy.' } } }],
    });
    await settle();
    const done = w.find('.sku .sk-task');
    expect(done.classes()).toContain('attn');
    expect(done.text()).toContain('The new version failed its health check, so the previous one is back.');
    expect(done.text()).toContain('chadash-chadash-1 is unhealthy');
    expect(f.calls.filter((c) => c.path === '/api/nodes/local/self-update').length).toBe(before + 1);
  });

  it('says why a node that may update can’t, and shows nothing when no node may', async () => {
    hub({ 'GET /api/nodes/local/self-update': { status: 200, body: { node: 'local', allowed: true, available: false, reason: 'This node doesn’t run in a compose project beside the hub.', last: null } } });
    const h = open('/settings/stacks');
    const w = h.mountIn(StacksPane);
    await settle();
    expect(w.find('.sku .st-note').text()).toContain('beside the hub');
    expect(w.find('.sku form').exists()).toBe(false);

    document.body.innerHTML = '';
    hub({ 'GET /api/nodes/local/self-update': { status: 200, body: { node: 'local', allowed: false, available: false, reason: 'Its policy doesn’t grant stacks.selfupdate.', last: null } } });
    const h2 = open('/settings/stacks');
    const w2 = h2.mountIn(StacksPane);
    await settle();
    expect(w2.find('.sku').exists()).toBe(false);
  });
});

describe('fleet words', () => {
  it('reads tags and shortens images', () => {
    expect(tagProblem('0.3.0')).toBe('');
    expect(tagProblem('ghcr.io/ban-red/chadash:0.3.0')).toBe('');
    expect(tagProblem('')).toContain('Give the tag');
    expect(tagProblem('ghcr.io/ban-red/chadash@sha256:abc')).toContain('not a digest');
    expect(tagProblem('0.3.0; rm -rf /')).toBe('Only ghcr.io/ban-red/chadash can be updated here. Give one of its tags.');
    expect(shortImage(HUB_NEXT)).toBe('0.3.0 · sha256:bbbbbbbb');
    expect(shortImage('ghcr.io/ban-red/chadash@sha256:' + 'c'.repeat(64))).toBe('sha256:cccccccc');
    expect(shortImage(undefined)).toBe('unknown');
  });
});
