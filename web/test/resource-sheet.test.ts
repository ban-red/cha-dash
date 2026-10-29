/**
 * The resource sheet, waves 1 and 2 (docs/plan/10-resource-sheet.md): the
 * `?r=` route, opening it from the containers tile, the command bar and the
 * Inbox, a container's Overview, Actions, Logs, Related and History, the tabs,
 * Esc and focus, a container that leaves the graph, and the open problem as
 * one line into the Inbox.
 * A small home of its own, so the sim's snapshots can change under it.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import CommandPalette from '../src/components/CommandPalette.vue';
import InboxSheet from '../src/components/InboxSheet.vue';
import ResourceSheet from '../src/components/ResourceSheet.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { toPointer } from '../src/lib/jsonpatch';
import { navigate, resolve, route, sheetOf, sheetPath } from '../src/lib/router';
import { permissions } from '../src/model/roles';
import type { GraphDoc, Incident, Resource } from '../src/model/types';
import AuditPane from '../src/settings/AuditPane.vue';
import ContainersWidget from '../src/widgets/ContainersWidget.vue';
import { axeViolations } from './axe';
import { engine, planFor, taskFor } from './fake-engine';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

beforeEach(() => {
  setDisplayTimeZone('UTC');
  // The sheet asks the hub what it could plan; tests that care install their own engine.
  vi.stubGlobal('fetch', vi.fn(async () => new Response('[]', { status: 200, headers: { 'content-type': 'application/json' } })));
  navigate('/', { replace: true });
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

const SONARR = 'docker:i_d/container/sonarr';
const DB = 'docker:i_d/container/sonarr-db';
const ENGINE = 'docker:i_d/engine';
const STACK = 'docker:i_d/stack/media';

function home(over: Partial<Record<string, Partial<Resource>>> = {}) {
  const resources: Record<string, Resource> = {};
  const add = (r: Resource) => (resources[r.id] = { ...r, ...(over[r.id] ?? {}) });
  add({
    id: ENGINE,
    kind: 'engine',
    name: 'magpie',
    source: 'integration:i_d',
    state: 'running',
    health: 'ok',
    attrs: { node: 'local', endpoint: 'node://local', via: 'node', access: 'node', version: '29.6.2', api_version: '1.54', containers: 2, running: 1 },
  });
  add({
    id: STACK,
    kind: 'stack',
    name: 'media',
    source: 'integration:i_d',
    state: 'partial',
    health: 'down',
    attrs: { project: 'media', containers: 2, running: 1, working_dir: '/opt/stacks/media', config_files: ['/opt/stacks/media/compose.yaml'] },
  });
  add({
    id: SONARR,
    kind: 'container',
    name: 'sonarr',
    source: 'integration:i_d',
    state: 'exited',
    health: 'down',
    labels: { 'com.docker.compose.project': 'media', 'com.docker.compose.service': 'sonarr', 'homepage.name': 'Sonarr' },
    attrs: {
      image: 'lscr.io/linuxserver/sonarr:4.0.17',
      finished_at: '2026-10-03T03:12:00Z',
      exit_code: 137,
      oom_killed: true,
      restart_policy: 'unless-stopped',
      restart_count: 2,
      published: ['18989->8989/tcp'],
      ports: ['8989/tcp'],
      networks: ['media_default'],
      links: [{ title: 'Sonarr', url: 'https://sonarr.home.example' }],
    },
  });
  add({ id: DB, kind: 'container', name: 'sonarr-db', source: 'integration:i_d', state: 'running', health: 'ok', attrs: { image: 'postgres:17', started_at: '2026-10-01T10:00:00Z' } });
  add({ id: 'sonarr:i_s/app', kind: 'app', name: 'Sonarr', source: 'integration:i_s', state: 'running', health: 'down' });
  const relations: GraphDoc['relations'] = {};
  const rel = (from: string, type: string, to: string) => (relations[`${from} ${type} ${to}`] = { from, type, to, source: 'integration:i_d' });
  rel(SONARR, 'runs_on', ENGINE);
  rel(DB, 'runs_on', ENGINE);
  rel(SONARR, 'depends_on', DB);
  rel('sonarr:i_s/app', 'runs_on', SONARR);
  rel(SONARR, 'part_of', STACK);
  rel(DB, 'part_of', STACK);
  rel(STACK, 'runs_on', ENGINE);
  const incident: Incident = {
    id: 'inc-sonarr',
    scenario: 'container-down',
    class: 'problem',
    severity: 'down',
    status: 'open',
    phase: 'open',
    title: 'sonarr exited after running out of memory',
    target: SONARR,
    opened_at: '2026-10-03T03:12:00Z',
  } as Incident;
  return {
    graph: { resources, relations },
    metrics: { series: {} },
    inbox: { now: '2026-10-03T14:05:00Z', incidents: { [incident.id]: incident } },
  };
}

async function sheetOn(id: string, data = home()) {
  const h = harness(data);
  await flushPromises();
  h.ui.openSheet(id);
  const w = h.mountIn(ResourceSheet);
  await flushPromises();
  const panel = () => document.querySelector<HTMLElement>('.rsheet');
  return { ...h, w, panel };
}

describe('the ?r= route', () => {
  it('names the sheet on front-page routes only, and keeps the board', () => {
    expect(sheetOf({ path: '/', query: { r: SONARR } })).toBe(SONARR);
    expect(sheetOf({ path: '/boards/b2', query: { r: SONARR } })).toBe(SONARR);
    expect(sheetOf({ path: '/settings/stacks', query: { r: SONARR } })).toBeUndefined();
    expect(sheetOf({ path: '/', query: {} })).toBeUndefined();
    expect(sheetPath({ path: '/boards/b2', query: {} }, SONARR)).toBe('/boards/b2?r=docker%3Ai_d%2Fcontainer%2Fsonarr');
    expect(sheetPath({ path: '/boards/b2', query: { r: SONARR } }, undefined)).toBe('/boards/b2');
    expect(resolve({ path: '/boards/b2', query: { r: SONARR } })).toEqual({ name: 'front', board: 'b2' });
  });

  it('opening pushes an entry, so Back closes it; closing replaces', async () => {
    const h = harness(home());
    await flushPromises();
    const depth = () => (history.state as { depth?: number } | null)?.depth ?? 0;
    const before = depth();
    h.ui.openSheet(SONARR);
    expect(h.ui.sheet.value).toBe(SONARR);
    expect(route.value.query['r']).toBe(SONARR);
    expect(depth()).toBe(before + 1);
    h.ui.closeSheet();
    expect(h.ui.sheet.value).toBeUndefined();
    expect(route.value.path).toBe('/');
    expect(depth()).toBe(before + 1);
  });

  it('the Inbox and Settings close it', async () => {
    const h = harness(home());
    await flushPromises();
    h.ui.openSheet(SONARR);
    h.ui.openInbox('inc-sonarr');
    expect(h.ui.sheet.value).toBeUndefined();
    expect(h.ui.state.inboxOpen).toBe(true);
    h.ui.openSheet(SONARR);
    expect(h.ui.state.inboxOpen).toBe(false);
    navigate('/settings/stacks');
    expect(h.ui.sheet.value).toBeUndefined();
  });
});

describe('opening it', () => {
  it('from the containers tile: each row is a button named for the container and its state', async () => {
    const h = harness(home());
    await flushPromises();
    const tall = h.mountIn(ContainersWidget, { size: 'tall' });
    await nextTick();
    const buttons = tall.findAll('.list .li button.lk');
    expect(buttons.map((b) => b.text())).toEqual(['sonarr', 'sonarr-db']);
    expect(buttons[0]!.attributes('aria-label')).toMatch(/^sonarr, exited \(137\) at 03:12 · OOM-killed/);
    await buttons[0]!.trigger('click');
    expect(h.ui.sheet.value).toBe(SONARR);
    // The tile opens its incident on a click, but not one meant for a row.
    expect(h.ui.state.inboxOpen).toBe(false);

    h.ui.closeSheet();
    const large = h.mountIn(ContainersWidget, { size: 'large' });
    await nextTick();
    await large.findAll('.sr:not(.head) button.lk')[1]!.trigger('click');
    expect(h.ui.sheet.value).toBe(DB);
  });

  it('not while the board is being edited', async () => {
    const h = harness(home());
    await flushPromises();
    h.ui.state.editing = true;
    const tall = h.mountIn(ContainersWidget, { size: 'tall' });
    await nextTick();
    const b = tall.find('.list .li button.lk');
    expect(b.attributes('tabindex')).toBe('-1');
    await b.trigger('click');
    expect(h.ui.sheet.value).toBeUndefined();
  });

  it('from the command bar: opening a container opens its sheet', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('[]', { status: 200, headers: { 'content-type': 'application/json' } })));
    const h = harness(home());
    await flushPromises();
    h.ui.openPalette();
    h.mountIn(CommandPalette);
    await flushPromises();
    const input = document.querySelector<HTMLInputElement>('.palette input')!;
    input.value = 'sonarr-db';
    input.dispatchEvent(new Event('input'));
    await nextTick();
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
    await nextTick();
    expect(h.ui.sheet.value).toBe(DB);
    expect(h.ui.state.paletteOpen).toBe(false);
  });
});

describe('a container’s sheet', () => {
  it('says what it is, why it’s down, and the facts, with a dash for what nobody knows', async () => {
    const { panel } = await sheetOn(SONARR);
    const p = panel()!;
    expect(p.getAttribute('role')).toBe('dialog');
    expect(p.querySelector('h2')!.textContent).toBe('sonarr');
    expect(document.activeElement).toBe(p.querySelector('h2'));
    expect(p.querySelector('.rs-sub')!.textContent).toContain('container · magpie · exited (137) at 03:12 · OOM-killed');
    const facts = Object.fromEntries([...p.querySelectorAll('.rs-panel > .rs-facts dt')].map((dt) => [dt.textContent, dt.nextElementSibling!.textContent]));
    expect(facts).toMatchObject({
      Image: 'lscr.io/linuxserver/sonarr:4.0.17',
      Tag: '4.0.17',
      Stopped: '03:12 · exit 137 · OOM-killed',
      'Health check': '—',
      Restarts: 'unless-stopped · 2',
      'Memory limit': '—',
      Published: '18989->8989/tcp',
      Ports: '8989/tcp',
      Addresses: '—',
      Compose: 'media · sonarr',
      Engine: 'magpie · node local',
    });
    expect(p.querySelector<HTMLAnchorElement>('.rs-links a')!.href).toBe('https://sonarr.home.example/');
    expect(p.querySelector('.rs-labels summary')!.textContent).toBe('Labels · 3');
    expect(await axeViolations(p)).toEqual([]);
  });

  it('shows the open problem as one line that opens the Inbox on it', async () => {
    const { ui, panel } = await sheetOn(SONARR);
    const line = panel()!.querySelector<HTMLButtonElement>('.rs-incident')!;
    expect(line.textContent).toContain('sonarr exited after running out of memory');
    line.click();
    expect(ui.state.inboxOpen).toBe(true);
    expect(ui.state.inboxFocus).toBe('inc-sonarr');
    expect(ui.sheet.value).toBeUndefined();
  });

  it('has tabs with arrow keys, and Related opens other sheets or reveals the rest', async () => {
    const { ui, panel } = await sheetOn(SONARR);
    const tabs = () => [...panel()!.querySelectorAll<HTMLButtonElement>('[role="tab"]')];
    expect(tabs().map((t) => t.textContent!.trim())).toEqual(['Overview', 'Related', 'History']);
    expect(tabs()[0]!.getAttribute('aria-selected')).toBe('true');
    tabs()[0]!.focus();
    tabs()[0]!.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }));
    await nextTick();
    await nextTick();
    expect(tabs()[1]!.getAttribute('aria-selected')).toBe('true');
    expect(document.activeElement).toBe(tabs()[1]);
    const related = panel()!.querySelector<HTMLElement>('[role="tabpanel"]:not([style*="none"])')!;
    const groups = [...related.querySelectorAll('.rs-group')].map((g) => [g.querySelector('h3')!.textContent, [...g.querySelectorAll('button')].map((b) => b.textContent)]);
    expect(groups).toEqual([
      ['Depends on', ['sonarr-db']],
      ['Used by', ['Sonarr']],
      ['Part of', ['media']],
      ['Runs on', ['magpie']],
    ]);
    // Another container: its sheet, and the tabs start over.
    related.querySelector<HTMLButtonElement>('.rs-group button')!.click();
    await nextTick();
    expect(ui.sheet.value).toBe(DB);
    expect(tabs()[0]!.getAttribute('aria-selected')).toBe('true');
    // Anything without a sheet (sonarr's app): the sheet closes and the page shows it the usual way.
    const reveal = vi.spyOn(ui, 'reveal');
    ui.openSheet(SONARR);
    await nextTick();
    tabs()[1]!.click();
    await nextTick();
    const used = [...panel()!.querySelectorAll<HTMLButtonElement>('.rs-group button')].find((b) => b.textContent === 'Sonarr')!;
    used.click();
    expect(ui.sheet.value).toBeUndefined();
    expect(reveal).toHaveBeenCalledWith('sonarr:i_s/app');
  });

  it('closes on Esc and puts focus back where it was', async () => {
    const h = harness(home());
    await flushPromises();
    const opener = document.createElement('button');
    document.body.appendChild(opener);
    opener.focus();
    h.mountIn(ResourceSheet);
    h.ui.openSheet(SONARR);
    await flushPromises();
    expect(document.activeElement?.tagName).toBe('H2');
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await flushPromises();
    expect(h.ui.sheet.value).toBeUndefined();
    expect(document.activeElement).toBe(opener);
  });

  it('a container that leaves the graph keeps its name and says so', async () => {
    const { ws, panel } = await sheetOn(SONARR);
    ws.push({ op: 'diff', id: ws.subId('graph'), seq: 2, patch: [{ op: 'remove', path: toPointer('graph', 'resources', SONARR) }] });
    await nextTick();
    expect(panel()!.querySelector('h2')!.textContent).toBe('sonarr');
    expect(panel()!.textContent).toContain('This container isn’t in the graph any more.');
    expect(panel()!.querySelector('.rs-facts')).toBeNull();
  });

  it('opens on a deep link straight away', async () => {
    navigate(`/?r=${encodeURIComponent(DB)}`, { replace: true });
    const h = harness(home());
    await flushPromises();
    h.mountIn(ResourceSheet);
    await flushPromises();
    expect(document.querySelector('.rsheet h2')!.textContent).toBe('sonarr-db');
  });

  it('in the Demo Home, says Docker is read-only', async () => {
    const h = harness(home(), { demo: true });
    await flushPromises();
    h.ui.openSheet(DB);
    h.mountIn(ResourceSheet);
    await flushPromises();
    expect(document.querySelector('.rsheet .rs-hint')!.textContent).toContain('In the Demo Home, Docker is read-only');
  });
});

const OFFERS = [
  { id: 'docker.container.start', label: 'Start', tier: 'T1', available: true },
  { id: 'docker.container.recreate', label: 'Recreate', tier: 'T2', available: false, unavailable: 'sonarr is protected by this node’s policy', recipe: 'i_d' },
  { id: 'docker.container.remove', label: 'Remove', tier: 'T3', available: true },
  { id: 'docker.container.logs', label: 'Show the logs of', tier: 'T0', available: true },
] as const;

async function wave2(opts: { role?: string; audit?: unknown } = {}) {
  const e = engine({ offers: { [SONARR]: OFFERS.map((o) => ({ ...o })) }, plan: (action) => (action.endsWith('.remove') ? { tier: 'T3' } : undefined) });
  const audit: unknown[] = [];
  const net = fakeFetch({
    ...e.routes,
    'GET /api/audit': (_b, url) => {
      audit.push(url.search);
      return { status: 200, body: opts.audit ?? { events: [] } };
    },
  });
  const h = harness(home());
  if (opts.role) h.session.state.user = { id: 'u2', username: 'sam', role: opts.role };
  await flushPromises();
  h.ui.openSheet(SONARR);
  h.mountIn(ResourceSheet);
  await flushPromises();
  const panel = () => document.querySelector<HTMLElement>('.rsheet')!;
  const tabNamed = (name: string) => [...panel().querySelectorAll<HTMLButtonElement>('[role="tab"]')].find((t) => t.textContent!.trim() === name);
  const show = async (name: string) => {
    tabNamed(name)!.click();
    await flushPromises();
    return panel().querySelector<HTMLElement>(`[role="tabpanel"][aria-labelledby$="-tab-${name.toLowerCase()}"]`)!;
  };
  return { ...h, e, net, audit, panel, tabNamed, show };
}

describe('wave 2: acting from the sheet', () => {
  it('lists what the hub would plan, by tier, with the reason and the fix for what can’t run', async () => {
    const { panel, show, e } = await wave2();
    expect([...panel().querySelectorAll('[role="tab"]')].map((t) => t.textContent!.trim())).toEqual(['Overview', 'Actions', 'Logs', 'Related', 'History']);
    expect(e.listed).toContain(SONARR);
    const acts = await show('Actions');
    expect([...acts.querySelectorAll('.rs-tier h3')].map((h) => h.textContent)).toEqual(['T1', 'T2', 'T3']);
    // Logs live in their own tab.
    expect(acts.textContent).not.toContain('Show the logs of');
    const recreate = [...acts.querySelectorAll<HTMLButtonElement>('.rs-tier .btn')].find((b) => b.textContent!.startsWith('Recreate'))!;
    expect(recreate.getAttribute('aria-disabled')).toBe('true');
    const why = document.getElementById(recreate.getAttribute('aria-describedby')!)!;
    expect(why.textContent).toContain('sonarr is protected by this node’s policy');
    expect(why.querySelector('button')!.textContent!.trim()).toBe('Open its settings');
    expect(await axeViolations(panel())).toEqual([]);
  });

  it('runs an action through the ladder, and Remove can take the anonymous volumes', async () => {
    const { show, e, ui } = await wave2();
    const acts = await show('Actions');
    const button = (name: string) => [...acts.querySelectorAll<HTMLButtonElement>('.rs-tier .btn')].find((b) => b.textContent!.startsWith(name))!;
    // Remove (T3) waits in its typed confirmation, so nothing runs yet.
    acts.querySelector<HTMLInputElement>('.rs-check input')!.click();
    await flushPromises();
    button('Remove').click();
    await flushPromises();
    expect(e.plans.at(-1)).toEqual({ action: 'docker.container.remove', target: SONARR, params: { volumes: true } });
    expect(ui.state.confirm?.kind).toBe('type');
    ui.cancelConfirm();
    button('Start').click();
    await flushPromises();
    expect(e.plans.at(-1)).toEqual({ action: 'docker.container.start', target: SONARR });
    button('Recreate').click();
    await flushPromises();
    expect(e.plans.some((p) => p.action === 'docker.container.recreate')).toBe(false);
  });

  it('shows the task working on it, with Cancel, and holds the other actions meanwhile', async () => {
    const { show, ws, ui } = await wave2();
    const task = taskFor(planFor('docker.container.restart', SONARR, { label: 'Restart sonarr' }), { step: 'Ask the node to restart sonarr', progress: 0.5, cancellable: true });
    ws.push({ op: 'diff', id: ws.subId('tasks'), seq: 2, patch: [{ op: 'add', path: toPointer('tasks', 'tasks', task.id), value: task }] });
    await flushPromises();
    const acts = await show('Actions');
    expect(acts.querySelector('.rs-working')!.textContent).toContain('Restart sonarr · Ask the node to restart sonarr · 50%');
    const cancel = vi.spyOn(ui, 'cancelTask');
    acts.querySelector<HTMLButtonElement>('.rs-working button')!.click();
    expect(cancel).toHaveBeenCalled();
    expect(acts.querySelector('.rs-tier .btn')!.getAttribute('aria-disabled')).toBe('true');
  });

  it('a viewer sees no Logs and only what their role can run', async () => {
    const { panel } = await wave2({ role: 'viewer' });
    const tabs = [...panel().querySelectorAll('[role="tab"]')].map((t) => t.textContent!.trim());
    expect(tabs).toEqual(['Overview', 'Related', 'History']);
    // Reading data back (logs) is Operator and up, as the hub's policy says.
    expect(['owner', 'admin', 'operator', 'viewer', 'household'].map((r) => permissions(r).readData)).toEqual([true, true, true, false, false]);
  });
});

describe('wave 2: logs in the tab', () => {
  it('reads the last lines into the tab, not the log dialog, and keeps them while the sheet is open', async () => {
    const { show, e, ws, ui } = await wave2();
    const logs = await show('Logs');
    expect(logs.textContent).toContain('Live logs come with v0.3');
    logs.querySelector<HTMLButtonElement>('.rs-acts .btn')!.click();
    await flushPromises();
    expect(e.plans.at(-1)).toEqual({ action: 'docker.container.logs', target: SONARR });
    const p = planFor('docker.container.logs', SONARR);
    const task = taskFor(p, { state: 'succeeded', reads_data: true, log: ['[init] starting', 'listening on :8989'] });
    ws.push({ op: 'diff', id: ws.subId('tasks'), seq: 2, patch: [{ op: 'add', path: toPointer('tasks', 'tasks', task.id), value: task }] });
    await flushPromises();
    expect(ui.state.taskLog).toBeUndefined();
    const pre = logs.querySelector('pre.rs-log')!;
    expect(pre.textContent).toBe('[init] starting\nlistening on :8989');
    expect(logs.querySelector('.rs-acts .btn')!.textContent!.trim()).toBe('Show again');
    // The hub dropping the task doesn't take the lines.
    ws.push({ op: 'diff', id: ws.subId('tasks'), seq: 3, patch: [{ op: 'remove', path: toPointer('tasks', 'tasks', task.id) }] });
    await flushPromises();
    expect(logs.querySelector('pre.rs-log')!.textContent).toContain('listening on :8989');
    // Another container: the lines go.
    ui.openSheet(DB);
    await flushPromises();
    expect(document.querySelector('pre.rs-log')).toBeNull();
  });
});

describe('wave 2: history', () => {
  it('lists the tasks on it, and the audit log for admins with a way into Settings → Audit', async () => {
    const events = { events: [{ id: 'ev1', at: '2026-10-03T03:20:00Z', action: 'docker.container.start', result: 'refused', actor: { name: 'sam' }, error: { message: 'sonarr is already running.' } }], next: 'c2' };
    const { show, ws, audit } = await wave2({ audit: events });
    const task = taskFor(planFor('docker.container.start', SONARR, { label: 'Start sonarr' }), { state: 'failed', error: { code: 'upstream', message: 'The node said no.' }, started_at: '2026-10-03T03:15:00Z' });
    ws.push({ op: 'diff', id: ws.subId('tasks'), seq: 2, patch: [{ op: 'add', path: toPointer('tasks', 'tasks', task.id), value: task }] });
    await flushPromises();
    const hist = await show('History');
    const lists = hist.querySelectorAll('.rs-hist');
    expect(lists[0]!.textContent).toContain('Start sonarr');
    expect(lists[0]!.textContent).toContain('owner · 03:15 · The node said no.');
    expect(audit[0]).toBe(`?target=${encodeURIComponent(SONARR)}&limit=20`);
    expect(lists[1]!.textContent).toContain('docker.container.start');
    expect(lists[1]!.textContent).toContain('sam · 03:20 · refused: sonarr is already running.');
    [...hist.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent === 'Older')!.click();
    await flushPromises();
    expect(audit[1]).toBe(`?target=${encodeURIComponent(SONARR)}&limit=20&cursor=c2`);
    [...hist.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent!.startsWith('Open in Settings'))!.click();
    expect(route.value.path).toBe('/settings/audit');
    expect(route.value.query['target']).toBe(SONARR);
  });

  it('an operator sees their tasks, and where the rest is', async () => {
    const { show, audit } = await wave2({ role: 'operator' });
    const hist = await show('History');
    expect(hist.textContent).toContain('Older history is in the audit log, for admins and the owner.');
    expect(audit).toEqual([]);
  });

  it('Settings → Audit opens filtered to the target the link names', async () => {
    const net = fakeFetch({ 'GET /api/audit': { status: 200, body: { events: [] } }, 'GET /api/audit/verify': { status: 200, body: { ok: true, checked: 0 } } });
    navigate(`/settings/audit?target=${encodeURIComponent(SONARR)}`, { replace: true });
    const h = harness(home());
    await flushPromises();
    h.mountIn(AuditPane);
    await flushPromises();
    expect(net.calls.find((c) => c.path.startsWith('/api/audit?'))!.path).toContain(`target=${encodeURIComponent(SONARR)}`);
  });
});

describe('wave 2: from the Inbox', () => {
  it('an incident on a container links to its sheet', async () => {
    fakeFetch(engine().routes);
    const h = harness(home());
    await flushPromises();
    h.mountIn(InboxSheet);
    h.ui.openInbox('inc-sonarr');
    await flushPromises();
    const link = [...document.querySelectorAll<HTMLButtonElement>('.sheet-link button')].find((b) => b.textContent === 'Open sonarr')!;
    link.click();
    await flushPromises();
    expect(h.ui.sheet.value).toBe(SONARR);
    expect(h.ui.state.inboxOpen).toBe(false);
  });
});

describe('wave 3: engines and stacks', () => {
  it('an engine’s sheet: what it reads through, its containers (problems first), and its node', async () => {
    const { ui, panel } = await sheetOn(ENGINE);
    const p = panel()!;
    expect(p.querySelector('.rs-sub')!.textContent).toContain('Docker engine · node local · running');
    const facts = Object.fromEntries([...p.querySelectorAll('.rs-panel > .rs-facts dt')].map((dt) => [dt.textContent, dt.nextElementSibling!.textContent]));
    expect(facts).toMatchObject({ Endpoint: 'node://local', 'Reads through': 'node · node', Containers: '1 running of 2', Docker: '29.6.2 · API 1.54', Host: '—', Node: 'local' });
    const members = [...p.querySelectorAll<HTMLButtonElement>('.rs-members button')];
    expect(members.map((b) => b.textContent)).toEqual(['sonarr', 'sonarr-db']);
    expect(members[0]!.getAttribute('aria-label')).toMatch(/^sonarr, exited/);
    // Its containers aren't repeated under Related.
    expect([...p.querySelectorAll('.rs-group button')].map((b) => b.textContent)).not.toContain('sonarr');
    expect(await axeViolations(p)).toEqual([]);
    [...p.querySelectorAll<HTMLAnchorElement>('.rs-acts a')].find((a) => a.textContent!.includes('Settings → Nodes'))!.click();
    expect(route.value.path).toBe('/settings/nodes');
    navigate('/', { replace: true });
    ui.openSheet(ENGINE);
    await flushPromises();
    panel()!.querySelector<HTMLButtonElement>('.rs-members button')!.click();
    await flushPromises();
    expect(ui.sheet.value).toBe(SONARR);
  });

  it('a compose stack’s sheet: its folder and files, its containers, and its editor for admins', async () => {
    const { panel } = await sheetOn(STACK);
    const p = panel()!;
    expect(p.querySelector('.rs-sub')!.textContent).toContain('compose stack · magpie');
    const facts = Object.fromEntries([...p.querySelectorAll('.rs-panel > .rs-facts dt')].map((dt) => [dt.textContent, dt.nextElementSibling!.textContent]));
    expect(facts).toMatchObject({ Project: 'media', Containers: '1 running of 2', Folder: '/opt/stacks/media', 'Compose files': '/opt/stacks/media/compose.yaml', Engine: 'magpie · node local' });
    expect([...p.querySelectorAll('.rs-members button')].map((b) => b.textContent)).toEqual(['sonarr', 'sonarr-db']);
    const link = [...p.querySelectorAll<HTMLAnchorElement>('.rs-acts a')].find((a) => a.textContent!.includes('Settings → Stacks'))!;
    expect(link.getAttribute('href')).toBe('/settings/stacks/local/media');
  });

  it('counts an engine’s containers from the graph when the source doesn’t', async () => {
    const { panel } = await sheetOn(ENGINE, home({ [ENGINE]: { attrs: { node: 'local' } } }));
    const dt = [...panel()!.querySelectorAll('.rs-panel > .rs-facts dt')].find((d) => d.textContent === 'Containers')!;
    expect(dt.nextElementSibling!.textContent).toBe('1 running of 2');
  });

  it('an operator gets no links into Settings', async () => {
    const h = harness(home());
    h.session.state.user = { id: 'u2', username: 'sam', role: 'operator' };
    await flushPromises();
    h.ui.openSheet(STACK);
    h.mountIn(ResourceSheet);
    await flushPromises();
    expect(document.querySelector('.rsheet .rs-acts a')).toBeNull();
  });

  it('the containers tile’s engines open their sheet', async () => {
    const h = harness(home());
    await flushPromises();
    const wide = h.mountIn(ContainersWidget, { size: 'wide' });
    await nextTick();
    const engine = wide.find('.tf button.lk');
    expect(engine.text()).toBe('magpie');
    expect(engine.attributes('aria-label')).toBe('magpie, 2 containers');
    await engine.trigger('click');
    expect(h.ui.sheet.value).toBe(ENGINE);
    expect(h.ui.state.inboxOpen).toBe(false);
  });
});
