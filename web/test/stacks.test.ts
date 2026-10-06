/**
 * Settings → Stacks (owned stacks, wave 9): the list per node, the editor's
 * live validation, Deploy through the safety ladder with the hold ring, the
 * task's outcome (rolled back on its own), adopting, the history's rollback,
 * stack secrets (write-only), protected stacks, and axe on the main states.
 * The hub's side is faked to the Track 9B API contract.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import ConfirmDialog from '../src/components/ConfirmDialog.vue';
import StacksPane from '../src/settings/StacksPane.vue';
import SettingsSheet from '../src/settings/SettingsSheet.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { toPointer } from '../src/lib/jsonpatch';
import { navigate } from '../src/lib/router';
import type { StackDetail, StackNode, Validation } from '../src/settings/stacks';
import type { Task } from '../src/model/types';
import { axeViolations } from './axe';
import { engine, refuse, taskFor, planFor } from './fake-engine';
import { fakeFetch, type Handler } from './fake-fetch';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

beforeEach(() => {
  setDisplayTimeZone('UTC');
  navigate('/', { replace: true });
});
const mounted: { unmount: () => void }[] = [];
afterEach(() => {
  // Unmount first, so no debounced check outlives the faked fetch.
  for (const w of mounted.splice(0)) w.unmount();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

async function settle(): Promise<void> {
  await vi.dynamicImportSettled();
  await flushPromises();
  await nextTick();
}
const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));

const HEAD = { commit: 'a'.repeat(40), short: 'aaaaaaa', subject: 'Deploy media: recreate web', at: '2026-10-03T12:00:00Z' };

const NODES: StackNode[] = [
  {
    node: 'local',
    name: 'local',
    online: true,
    error: null,
    root: '/opt/stacks',
    stacks: [
      { name: 'media', status: 'running', services: 2, running: 2, location: 'root', path: '/opt/stacks/media', protected: false, drift: false, draft: true, head: HEAD },
      { name: 'chadash', status: 'running', services: 1, running: 1, location: 'root', path: '/opt/stacks/chadash', protected: true, drift: false, draft: false, head: { ...HEAD, short: 'bbbbbbb', subject: 'Adopt chadash' } },
      { name: 'dns', status: 'partial', services: 2, running: 1, location: 'external', path: '/srv/dns', protected: false, drift: true, draft: false, head: null },
    ],
    detected: [
      { name: 'pihole', status: 'running', services: 1, running: 1, path: '/srv/pihole', config_files: ['/srv/pihole/docker-compose.yml'], adoptable: true, reason: null },
      { name: 'oddball', status: 'running', services: 1, running: 1, path: '/tmp/x', config_files: [], adoptable: false, reason: 'Its compose file is no longer on disk, so there’s nothing to adopt.' },
    ],
  },
  { node: 'edge', name: 'edge', online: false, error: { code: 'node_unavailable', message: 'The node edge hasn’t checked in for 4 minutes.' }, root: '/opt/stacks', stacks: [], detected: [] },
];

const COMMITTED = 'services:\n  web:\n    image: nginx:1.27\n  db:\n    image: postgres:17\n';
const DRAFTED = 'services:\n  web:\n    image: nginx:1.28\n  db:\n    image: postgres:17\n    environment:\n      PW: ${secret:db_password}\n';

function media(over: Partial<StackDetail> = {}): StackDetail {
  return {
    node: 'local',
    name: 'media',
    path: '/opt/stacks/media',
    file: 'compose.yaml',
    location: 'root',
    managed: true,
    protected: false,
    protected_reason: null,
    drift: false,
    status: 'running',
    content: COMMITTED,
    env: { present: true, keys: ['TZ'] },
    meta: { soak: '20s' },
    services: [
      { name: 'web', image: 'nginx:1.27', state: 'running', health: 'none', restarts: 0 },
      { name: 'db', image: 'postgres:17', state: 'running', health: 'healthy', restarts: 1 },
    ],
    history: [
      { ...HEAD, author: 'sam', kind: 'deploy' },
      { commit: 'c'.repeat(40), short: 'ccccccc', subject: 'Roll back media: web restarted 1× during the soak', at: '2026-10-02T12:00:00Z', author: 'sam', kind: 'rollback' },
      { commit: 'd'.repeat(40), short: 'ddddddd', subject: 'Adopt media', at: '2026-10-01T12:00:00Z', author: 'owner', kind: 'adopt' },
    ],
    draft: { content: DRAFTED, revision: 3, updated_at: '2026-10-03T13:00:00Z', updated_by: 'sam' },
    secrets: [{ name: 'db_password', set: true }],
    ...over,
  };
}

const VALID: Validation = {
  ok: true,
  messages: [{ severity: 'info', message: 'web has no healthcheck; the gate only checks it stays running.', line: 2, service: 'web' }],
  secrets: [{ name: 'db_password', set: true }],
  plan: { services: [{ name: 'web', action: 'recreate', image: 'nginx:1.28', reason: 'image changed', pull: true }, { name: 'db', action: 'unchanged', image: 'postgres:17' }], downtime_s: 2 },
  diff: [
    { op: ' ', text: 'services:' },
    { op: ' ', text: '  web:' },
    { op: '-', text: '    image: nginx:1.27' },
    { op: '+', text: '    image: nginx:1.28' },
    { op: '…', text: '3 unchanged lines' },
  ],
};

const INVALID: Validation = {
  ok: false,
  messages: [{ severity: 'error', message: 'services.web.image must be a string', line: 3, service: 'web' }],
  secrets: [],
  plan: null,
  diff: [],
};

function hub(over: Record<string, Handler | { status: number; body?: unknown }> = {}, validate: (content: string) => Validation = () => VALID) {
  const e = engine({ plan: (action) => (action === 'stack.deploy' || action === 'stack.rollback' ? { tier: 'T2', confirm: 'hold', label: action === 'stack.deploy' ? 'Deploy media' : 'Roll back media', steps: [{ label: 'Check images can be pulled (dry run)' }, { label: 'Pull nginx:1.28' }, { label: 'Recreate web' }, { label: 'Hold to the health gate: 20 s soak' }, { label: 'Roll back on its own if the gate fails' }] } : action === 'stack.adopt' ? { tier: 'T1', confirm: 'undo', label: 'Adopt pihole', undo: { action: 'stack.unadopt', label: 'Release pihole' } } : undefined) });
  const f = fakeFetch({
    ...e.routes,
    'GET /api/stacks': { status: 200, body: { nodes: NODES } },
    'GET /api/stacks/secrets': { status: 200, body: { secrets: [{ name: 'db_password', set: true, updated_at: '2026-10-03T10:00:00Z' }] } },
    'GET /api/stacks/local/media': { status: 200, body: media() },
    'POST /api/stacks/local/media/validate': (b) => ({ status: 200, body: validate((b as { content: string }).content) }),
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

describe('Settings → Stacks: the list', () => {
  it('lists stacks per node with status, badges and last commit; node errors as a sentence; detected projects with adopt or their reason', async () => {
    hub();
    const h = open('/settings/stacks');
    const w = h.mountIn(StacksPane);
    await settle();
    const rows = w.findAll('.sk-node').at(0)!.findAll('a.sk-row');
    expect(rows.map((r) => r.find('.t').text().replace(/\s+/g, ' ').trim())).toEqual([
      'media draft',
      'chadash Cha Dash’s own stack · view only until self-update',
      'dns edited outside Cha Dash',
    ]);
    expect(rows[0]!.find('.m').text().replace(/\s+/g, ' ')).toContain('running · 2/2 · aaaaaaa Deploy media: recreate web');
    expect(rows[2]!.find('.m').text().replace(/\s+/g, ' ')).toContain('partly running · 1/2 · adopted in place at /srv/dns · not deployed yet');
    expect(rows[0]!.attributes('href')).toBe('/settings/stacks/local/media');
    expect(w.text()).toContain('The node edge hasn’t checked in for 4 minutes.');
    const detected = w.findAll('.sk-node').at(0)!.findAll('div.sk-row');
    expect(detected[0]!.find('button').text()).toContain('Adopt');
    expect(detected[1]!.find('button').exists()).toBe(false);
    expect(detected[1]!.text()).toContain('Its compose file is no longer on disk, so there’s nothing to adopt.');
  });

  it('adopts through the ladder: T1 runs straight away with Undo', async () => {
    const { e } = hub();
    const h = open('/settings/stacks');
    const w = h.mountIn(StacksPane);
    await settle();
    await w.find('button[aria-label="Adopt pihole where it is"]').trigger('click');
    await settle();
    expect(e.plans).toEqual([{ action: 'stack.adopt', target: 'stack:local/pihole' }]);
    expect(e.approvals).toHaveLength(1);
    const toast = h.ui.state.toasts.at(-1)!;
    expect(toast.text).toBe('Started: Adopt pihole.');
    expect(toast.action?.label).toBe('Undo');
  });

  it('says when no node is connected, and when the list can’t load', async () => {
    hub({ 'GET /api/stacks': { status: 200, body: { nodes: [] } } });
    const h = open('/settings/stacks');
    const w = h.mountIn(StacksPane);
    await settle();
    expect(w.find('.st-empty .t').text()).toBe('No nodes connected');
    expect(w.text()).not.toContain('New stack');
    expect(await axeViolations()).toEqual([]);

    document.body.innerHTML = '';
    hub({ 'GET /api/stacks': refuse(500, 'internal', 'The stacks root isn’t a git repository.') });
    const h2 = open('/settings/stacks');
    const w2 = h2.mountIn(StacksPane);
    await settle();
    expect(w2.find('.st-note.bad').text()).toContain('The stacks root isn’t a git repository.');
  });

  it('is a Settings pane for owners and admins, its own chunk', async () => {
    hub();
    const h = open('/settings/stacks');
    const w = h.mountIn(SettingsSheet);
    await settle();
    expect(document.querySelector('#settings-h')?.textContent).toBe('Stacks');
    expect(document.querySelector('.st-nav a[aria-current="page"]')?.textContent).toContain('Stacks');
    expect(w.exists()).toBe(true);
    expect(await axeViolations()).toEqual([]);
  });
});

describe('Settings → Stacks: one stack', () => {
  it('shows the draft, messages with lines, secrets, the plan, the diff, services and history', async () => {
    hub();
    const h = open('/settings/stacks/local/media');
    const w = h.mountIn(StacksPane);
    await settle();
    await settle();
    expect((w.find('textarea').element as HTMLTextAreaElement).value).toBe(DRAFTED);
    expect(w.find('.sk-gutter').findAll('span')).toHaveLength(DRAFTED.split('\n').length);
    expect(w.find('.sk-msg').text()).toContain('line 2');
    expect(w.find('.sk-msg').text()).toContain('web has no healthcheck');
    expect(w.find('.sk-chips').text().replace(/\s+/g, ' ')).toContain('db_password · set');
    expect(w.find('.sk-table tbody').text()).toContain('recreate · pull');
    expect(w.find('.sk-plan').text()).toContain('About 2 s of downtime');
    expect(w.findAll('.sk-diff .add').map((x) => x.text())).toEqual(['added: +     image: nginx:1.28']);
    expect(w.findAll('.sk-diff .del')).toHaveLength(1);
    // No healthcheck is never "healthy".
    const svcs = w.findAll('.sk-svc');
    expect(svcs[0]!.find('.end').text()).toBe('running · no healthcheck');
    expect(svcs[1]!.find('.end').text()).toBe('running · healthy · restarted 1×');
    // History: kind shown, roll back offered on all but the current commit.
    const hist = w.findAll('.sk-h');
    expect(hist.map((x) => x.find('.sk-kind').text())).toEqual(['deploy', 'rollback', 'adopted']);
    expect(hist[0]!.find('button').exists()).toBe(false);
    expect(hist[1]!.find('button').text()).toContain('Roll back to this');
    expect(w.find('.sk-deploy').attributes('disabled')).toBeUndefined();
    expect(await axeViolations()).toEqual([]);
  });

  it('validates as you type, debounced, and renders errors with their line', async () => {
    const { f } = hub({}, (c) => (c.includes('image: [') ? INVALID : VALID));
    const h = open('/settings/stacks/local/media');
    const w = h.mountIn(StacksPane);
    await settle();
    await settle();
    const before = f.calls.filter((c) => c.path.endsWith('/validate')).length;
    expect(before).toBe(1); // once on load, not again when the file is filled in
    const ta = w.find('textarea');
    await ta.setValue('services:\n  web:\n    image: [\n');
    await ta.setValue('services:\n  web:\n    image: [1]\n');
    expect(f.calls.filter((c) => c.path.endsWith('/validate')).length).toBe(before);
    await wait(650);
    await settle();
    expect(f.calls.filter((c) => c.path.endsWith('/validate')).length).toBe(before + 1);
    expect(f.calls.filter((c) => c.path.endsWith('/validate')).at(-1)!.body).toEqual({ content: 'services:\n  web:\n    image: [1]\n' });
    expect(w.find('.sk-sum').text()).toBe('1 error');
    expect(w.find('.sk-msg.error').text()).toContain('line 3');
    expect(w.find('.sk-msg.error').text()).toContain('services.web.image must be a string');
    expect(w.find('.sk-gutter span.e').text()).toBe('3');
    expect(w.find('.sk-deploy').attributes('disabled')).toBeDefined();
    expect(w.find('.sk-why').text()).toBe('Save the draft first: Deploy runs the saved draft.');
    expect(await axeViolations()).toEqual([]);
  });

  it('saves a draft with its revision, and handles a conflict', async () => {
    let n = 0;
    const { f } = hub({
      'PUT /api/stacks/local/media': (b) =>
        ++n === 1 ? refuse(409, 'conflict', 'Someone else saved first.', { revision: 4 }) : { status: 200, body: { draft: { content: (b as { content: string }).content, revision: 5, updated_at: '2026-10-03T14:00:00Z', updated_by: 'owner' } } },
    });
    const h = open('/settings/stacks/local/media');
    const w = h.mountIn(StacksPane);
    await settle();
    await w.find('textarea').setValue(DRAFTED + '# more\n');
    await w.findAll('button').find((b) => b.text() === 'Save draft')!.trigger('click');
    await settle();
    expect(f.last('PUT', '/api/stacks/local/media')?.body).toEqual({ content: DRAFTED + '# more\n', revision: 3 });
    expect(w.text()).toContain('now revision 4');
    await w.findAll('button').find((b) => b.text() === 'Save mine anyway')!.trigger('click');
    await settle();
    expect(f.last('PUT', '/api/stacks/local/media')?.body).toEqual({ content: DRAFTED + '# more\n', revision: 4 });
    expect(h.ui.state.toasts.at(-1)?.text).toBe('Draft saved. Deploy it when you’re ready.');
    expect(w.text()).toContain('revision 5');
  });

  it('deploys through planning and the hold ring, follows the task, and says it rolled back on its own', async () => {
    const { e } = hub();
    const h = open('/settings/stacks/local/media');
    const w = h.mountIn(StacksPane);
    h.mountIn(ConfirmDialog);
    await settle();
    await settle();
    await w.find('.sk-deploy').trigger('click');
    await settle();
    expect(e.plans).toEqual([{ action: 'stack.deploy', target: 'stack:local/media' }]);
    // T2: nothing runs until the ring is held.
    expect(e.approvals).toHaveLength(0);
    expect(h.ui.state.confirm?.kind).toBe('hold');
    const dialog = document.querySelector('.confirm')!;
    expect([...dialog.querySelectorAll('.confirm-steps li')].map((li) => li.textContent)).toContain('Roll back on its own if the gate fails');
    h.ui.confirmed();
    await settle();
    expect(e.approvals).toHaveLength(1);

    const p = planFor('stack.deploy', 'stack:local/media', { label: 'Deploy media' });
    const t: Task = taskFor(p, { step: 'Hold to the health gate: 20 s soak', progress: 0.7, log: ['pulled nginx:1.28', 'recreated web', 'soaking 20 s'] });
    h.ws.push({ op: 'diff', id: h.ws.subId('tasks'), seq: 2, patch: [{ op: 'add', path: toPointer('tasks', 'tasks', t.id), value: t }] });
    await settle();
    const live = w.find('.sk-task');
    expect(live.text()).toContain('Deploy media');
    expect(live.text()).toContain('Hold to the health gate: 20 s soak · 70%');
    expect(live.find('.sk-log').text()).toContain('soaking 20 s');
    expect(w.find('.sk-deploy').attributes('disabled')).toBeDefined();

    h.ws.push({
      op: 'diff',
      id: h.ws.subId('tasks'),
      seq: 3,
      patch: [{ op: 'replace', path: toPointer('tasks', 'tasks', t.id), value: { ...t, state: 'failed', error: { code: 'rolled_back', message: 'web restarted 2× during the 20 s soak. media is back on aaaaaaa.' } } }],
    });
    await settle();
    const done = w.find('.sk-task');
    expect(done.classes()).toContain('attn');
    expect(done.text()).toContain('The new version failed its health check and Cha Dash rolled back on its own.');
    expect(done.text()).toContain('web restarted 2× during the 20 s soak. media is back on aaaaaaa.');
    expect(await axeViolations()).toEqual([]);
  });

  it('rolls back to an older commit through the hold ring', async () => {
    const { e } = hub();
    const h = open('/settings/stacks/local/media');
    const w = h.mountIn(StacksPane);
    await settle();
    await w.findAll('.sk-h')[1]!.find('button').trigger('click');
    await settle();
    expect(e.plans).toEqual([{ action: 'stack.rollback', target: 'stack:local/media', params: { commit: 'c'.repeat(40) } }]);
    expect(h.ui.state.confirm?.kind).toBe('hold');
  });

  it('a protected stack is read-only and can’t be deployed', async () => {
    hub({
      'GET /api/stacks/local/chadash': { status: 200, body: media({ name: 'chadash', protected: true, protected_reason: null, draft: null, path: '/opt/stacks/chadash' }) },
      'POST /api/stacks/local/chadash/validate': { status: 200, body: { ...VALID, diff: [] } },
    });
    const h = open('/settings/stacks/local/chadash');
    const w = h.mountIn(StacksPane);
    await settle();
    await settle();
    expect(w.find('textarea').attributes('readonly')).toBeDefined();
    expect(w.find('.sk-prot').text()).toContain('Cha Dash’s own stack · view only until self-update');
    expect(w.find('.sk-deploy').attributes('disabled')).toBeDefined();
    expect(w.find('.sk-why').text()).toBe('Cha Dash’s own stack · view only until self-update.');
    expect(w.findAll('button').some((b) => b.text() === 'Save draft')).toBe(false);
    expect(w.findAll('.sk-h button')).toHaveLength(0);
  });

  it('won’t deploy with a secret unset, and offers to accept drift', async () => {
    const { e } = hub(
      { 'GET /api/stacks/local/media': { status: 200, body: media({ drift: true }) } },
      () => ({ ...VALID, ok: true, secrets: [{ name: 'db_password', set: false }], plan: null }),
    );
    const h = open('/settings/stacks/local/media');
    const w = h.mountIn(StacksPane);
    await settle();
    await settle();
    expect(w.find('.sk-chips').text().replace(/\s+/g, ' ')).toContain('db_password · not set');
    expect(w.find('.sk-deploy').attributes('disabled')).toBeDefined();
    const accept = w.findAll('button').find((b) => b.text().startsWith('Accept edits made outside Cha Dash'))!;
    await accept.trigger('click');
    await settle();
    expect(e.plans).toEqual([{ action: 'stack.adopt', target: 'stack:local/media' }]);
  });

  it('says when the node is offline, and when the stack doesn’t exist', async () => {
    hub({ 'GET /api/stacks/local/media': refuse(503, 'node_unavailable', 'The node local isn’t connected right now.') });
    const h = open('/settings/stacks/local/media');
    const w = h.mountIn(StacksPane);
    await settle();
    expect(w.find('.st-note.hatched').text()).toContain('The node local isn’t connected right now.');
    expect(await axeViolations()).toEqual([]);

    document.body.innerHTML = '';
    hub({ 'GET /api/stacks/local/media': refuse(404, 'not_found', 'no such stack') });
    const h2 = open('/settings/stacks/local/media');
    const w2 = h2.mountIn(StacksPane);
    await settle();
    expect(w2.find('.st-note.bad').text()).toContain('Cha Dash doesn’t manage a stack called media on local.');
  });

  it('a new stack saves a draft under its name and opens it', async () => {
    const { f } = hub({
      'POST /api/stacks/local/web2/validate': { status: 200, body: { ...VALID, diff: [{ op: '+', text: 'services:' }] } },
      'PUT /api/stacks/local/web2': (b) => ({ status: 200, body: { draft: { content: (b as { content: string }).content, revision: 1, updated_at: '2026-10-03T14:00:00Z', updated_by: 'owner' } } }),
    });
    const h = open('/settings/stacks/new');
    const w = h.mountIn(StacksPane);
    await settle();
    expect(w.find('select').findAll('option').map((o) => o.text())).toEqual(['local · /opt/stacks']);
    await w.find('input[placeholder="media"]').setValue('media');
    await w.find('input[placeholder="media"]').trigger('blur');
    expect(w.text()).toContain('media already exists on local.');
    await w.find('input[placeholder="media"]').setValue('web2');
    await wait(650);
    await settle();
    expect(f.calls.some((c) => c.path === '/api/stacks/local/web2/validate')).toBe(true);
    expect(await axeViolations()).toEqual([]);
    await w.findAll('button').find((b) => b.text() === 'Save draft')!.trigger('click');
    await settle();
    expect(f.last('PUT', '/api/stacks/local/web2')?.body).toMatchObject({ revision: 0 });
    expect(location.pathname).toBe('/settings/stacks/local/web2');
  });
});

describe('Settings → Stacks: secrets', () => {
  it('sets a value write-only and never echoes it; deletes after asking', async () => {
    const { f } = hub({ 'PUT /api/stacks/secrets/api_token': { status: 204 }, 'DELETE /api/stacks/secrets/db_password': { status: 204 } });
    const h = open('/settings/stacks');
    const w = h.mountIn(StacksPane);
    await settle();
    expect(w.find('.sks-list').text()).toContain('db_password');
    const form = w.find('.sks-form');
    const value = form.find('input[type="password"]');
    expect(value.attributes('autocomplete')).toBe('new-password');
    await form.find('input[placeholder="db_password"]').setValue('api_token');
    await value.setValue('hunter2-very-secret');
    await form.trigger('submit');
    await settle();
    expect(f.last('PUT', '/api/stacks/secrets/api_token')?.body).toEqual({ value: 'hunter2-very-secret' });
    expect((value.element as HTMLInputElement).value).toBe('');
    expect(document.body.innerHTML).not.toContain('hunter2-very-secret');
    expect(h.ui.state.toasts.map((t) => t.text).join(' ')).not.toContain('hunter2');

    await w.find('button[aria-label="Delete db_password"]').trigger('click');
    expect(f.calls.some((c) => c.method === 'DELETE')).toBe(false);
    await w.find('.sks-confirm .btn.danger').trigger('click');
    await settle();
    expect(f.last('DELETE', '/api/stacks/secrets/db_password')).toBeDefined();
    expect(await axeViolations()).toEqual([]);
  });
});
