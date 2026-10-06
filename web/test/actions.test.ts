/**
 * The safety ladder on the action engine (stores/actions.ts through
 * stores/ui.ts): plan → the plan's rung → step-up when asked → approve, every
 * refusal code, the Undo toast, tasks over the `tasks` topic (and polled
 * without it), cancelling a task, the Demo Home on the engine, and what each
 * role sees.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import Briefing from '../src/components/Briefing.vue';
import InboxSheet from '../src/components/InboxSheet.vue';
import NeedsAttention from '../src/components/NeedsAttention.vue';
import TypeConfirmDialog from '../src/components/TypeConfirmDialog.vue';
import Toasts from '../src/components/Toasts.vue';
import TaskPresence from '../src/components/TaskPresence.vue';
import BoardSurface from '../src/board/BoardSurface.vue';
import { actionError } from '../src/lib/actions';
import { ApiError } from '../src/lib/api';
import { setDisplayTimeZone } from '../src/lib/format';
import { toPointer } from '../src/lib/jsonpatch';
import { navigate, route } from '../src/lib/router';
import { buildCommands } from '../src/model/commands';
import { defaultBoard } from '../src/model/defaultLayout';
import { assignableRoles, permissions } from '../src/model/roles';
import type { Incident } from '../src/model/types';
import { engine, planFor, refuse, taskFor } from './fake-engine';
import { fakeFetch } from './fake-fetch';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

beforeEach(() => {
  setDisplayTimeZone('UTC');
  navigate('/', { replace: true });
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

const BACKUP = 'backup-failed'; // T1: Back up homeassistant now
const PRUNE = 'pbs-datastore-filling'; // T2: Prune per policy
const RESTART = 'host-memory-pressure'; // T2: Restart debian-nas

function start(opts: { demo?: boolean; role?: string; data?: Record<string, unknown> } = {}) {
  const h = harness(opts.data ?? docs(withIncidents), opts.demo ? { demo: true } : {});
  if (opts.role) h.session.state.user = { id: 'u1', username: 'sam', role: opts.role };
  const inc = (id: string) => h.live.inbox.value!.incidents[id]!;
  return { ...h, inc };
}

describe('refusals, as the hub says them', () => {
  const err = (status: number, code: string, message?: string, extra: Record<string, unknown> = {}) =>
    new ApiError(status, code, message ?? 'x', undefined, message ? { error: { code, message, ...extra } } : undefined);

  it('keeps each code and shows the hub’s sentence as is', () => {
    for (const [status, code] of [
      [403, 'forbidden'],
      [404, 'not_found'],
      [409, 'precondition'],
      [409, 'unavailable'],
      [422, 'invalid'],
      [403, 'step_up_required'],
      [410, 'plan_expired'],
      [409, 'plan_changed'],
      [422, 'confirm_mismatch'],
      [409, 'locked'],
    ] as const) {
      const f = actionError(err(status, code, 'win-gaming is already stopped.'));
      expect(f.code).toBe(code);
      expect(f.message).toBe('win-gaming is already stopped.');
    }
  });

  it('carries the recipe and a new plan, and falls back to words when the hub sent none', () => {
    expect(actionError(err(409, 'unavailable', 'Add a control token to let Cha Dash reboot guests on io', { recipe: 'i_io' }))).toMatchObject({
      code: 'unavailable',
      message: 'Add a control token to let Cha Dash reboot guests on io',
      recipe: 'i_io',
    });
    const p = planFor('pve.guest.restart', 'pve:jupiter-nas/lxc/100');
    expect(actionError(err(409, 'plan_changed', 'The target changed.', { plan: p })).plan).toEqual(p);
    expect(actionError(err(403, 'http_403')).code).toBe('forbidden');
    expect(actionError(err(409, 'locked')).message).toContain('Another task');
    expect(actionError(new ApiError(0, 'network', '')).code).toBe('network');
    expect(actionError(new ApiError(429, 'rate', '', 7)).message).toBe('Too many actions at once. Try again in 7 s.');
  });
});

describe('the ladder, driven by the plan', () => {
  it('none (T0): approves straight away, no Undo', async () => {
    const e = engine({ plan: () => ({ tier: 'T0', confirm: 'none' }) });
    fakeFetch(e.routes);
    const h = start();
    h.ui.act(h.inc(BACKUP));
    await flushPromises();
    expect(e.approvals).toHaveLength(1);
    expect(h.ui.state.confirm).toBeUndefined();
    expect(h.ui.state.toasts[0]).toMatchObject({ text: 'Started: Back up homeassistant now.' });
    expect(h.ui.state.toasts[0]!.action).toBeUndefined();
  });

  it('undo (T1): runs, then Undo for 10 s plans and approves the plan’s undo on the same target', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const e = engine({
      plan: (action) => (action === 'downloads.pause' ? { undo: { action: 'downloads.resume', label: 'Resume all torrents' } } : { tier: 'T1' }),
    });
    fakeFetch(e.routes);
    const h = start();
    h.mountIn(Toasts);
    const oom = h.inc('qbittorrent-oom');
    h.ui.act(oom, oom.alternatives!.find((a) => a.id === 'downloads.pause'));
    await flushPromises();
    expect(e.approvals.map((a) => a.plan)).toEqual(['p_downloads_pause_app_qbittorrent']);
    await nextTick();
    const undo = [...document.querySelectorAll<HTMLButtonElement>('.toast .toast-act')].find((b) => b.textContent === 'Undo')!;
    expect(undo).toBeDefined();
    undo.click();
    await flushPromises();
    expect(e.plans.at(-1)).toEqual({ action: 'downloads.resume', target: 'app:qbittorrent' });
    expect(e.approvals).toHaveLength(2);
    // The undo itself offers no second Undo.
    expect(h.ui.state.toasts.filter((t) => t.action?.label === 'Undo')).toHaveLength(0);

    // Unused, the Undo goes after 10 s.
    h.ui.act(oom, oom.alternatives!.find((a) => a.id === 'downloads.pause'));
    await flushPromises();
    expect(h.ui.state.toasts.some((t) => t.action?.label === 'Undo')).toBe(true);
    vi.advanceTimersByTime(9_000);
    expect(h.ui.state.toasts.some((t) => t.action?.label === 'Undo')).toBe(true);
    vi.advanceTimersByTime(1_100);
    expect(h.ui.state.toasts.some((t) => t.action?.label === 'Undo')).toBe(false);
  });

  it('hold (T2): opens the ring with the plan’s impact and steps; approves only once confirmed', async () => {
    const e = engine();
    fakeFetch(e.routes);
    const h = start();
    h.ui.act(h.inc(PRUNE));
    await flushPromises();
    expect(h.ui.state.confirm).toMatchObject({ kind: 'hold', plan: { action: 'pbs.datastore.prune', tier: 'T2' } });
    expect(h.ui.state.confirm!.action.impact?.length).toBe(14);
    expect(e.approvals).toHaveLength(0);
    h.ui.confirmed();
    await flushPromises();
    expect(e.approvals).toEqual([{ plan: 'p_pbs_datastore_prune_pbs_main', hash: 'sha256:pbs.datastore.prune:pbs:main' }]);
    expect(h.ui.state.confirm).toBeUndefined();
  });

  it('type (T3): the typed dialog enables only on the exact name, sends it, and keeps a mismatch at the field', async () => {
    let n = 0;
    const e = engine({
      plan: () => ({ tier: 'T3', confirm: 'type', confirm_text: 'debian-nas', label: 'Remove debian-nas', undo: null }),
      approve: () => (++n === 1 ? refuse(422, 'confirm_mismatch', 'That isn’t the name. Type debian-nas.') : undefined),
    });
    fakeFetch(e.routes);
    const h = start();
    h.mountIn(TypeConfirmDialog, {});
    h.ui.act(h.inc(RESTART));
    await flushPromises();
    expect(h.ui.state.confirm?.kind).toBe('type');
    // The test mounts the dialog before the request; remount it the way FrontPage does.
    document.body.innerHTML = '';
    h.mountIn(TypeConfirmDialog);
    await nextTick();
    const dlg = document.querySelector<HTMLDialogElement>('dialog.act-dlg')!;
    expect(dlg.textContent).toContain('This can’t be undone.');
    expect(dlg.textContent).toContain('What will be lost or affected');
    const input = dlg.querySelector<HTMLInputElement>('input')!;
    const submit = dlg.querySelector<HTMLButtonElement>('button[type="submit"]')!;
    expect(submit.disabled).toBe(true);
    input.value = 'debian';
    input.dispatchEvent(new Event('input'));
    await nextTick();
    expect(submit.disabled).toBe(true);
    input.value = 'debian-nas';
    input.dispatchEvent(new Event('input'));
    await nextTick();
    expect(submit.disabled).toBe(false);
    dlg.querySelector('form')!.dispatchEvent(new Event('submit'));
    await flushPromises();
    expect(e.approvals[0]).toMatchObject({ typed: 'debian-nas' });
    // Refused as a mismatch: the dialog stays, the hub's sentence at the field.
    expect(h.ui.state.confirm?.error).toBe('That isn’t the name. Type debian-nas.');
    await nextTick();
    expect(dlg.querySelector('.help.bad')?.textContent).toBe('That isn’t the name. Type debian-nas.');
    dlg.querySelector('form')!.dispatchEvent(new Event('submit'));
    await flushPromises();
    expect(e.approvals).toHaveLength(2);
    expect(h.ui.state.confirm).toBeUndefined();
  });

  it('runs step-up before approving when the plan says required, and not at all if it’s closed', async () => {
    const e = engine({ plan: () => ({ tier: 'T3', confirm: 'type', confirm_text: 'debian-nas', step_up: 'required' }) });
    fakeFetch(e.routes);
    const h = start();
    h.ui.act(h.inc(RESTART));
    await flushPromises();
    h.ui.confirmed('debian-nas');
    await flushPromises();
    expect(h.ui.state.stepUp?.reason).toBe('Restart debian-nas');
    expect(e.approvals).toHaveLength(0);
    h.ui.stepUpDone(false);
    await flushPromises();
    expect(e.approvals).toHaveLength(0);
    expect(h.ui.state.confirm).toBeUndefined();

    // Again, confirmed this time; a second action within 5 minutes doesn't ask again.
    h.ui.act(h.inc(RESTART));
    await flushPromises();
    h.ui.confirmed('debian-nas');
    await flushPromises();
    h.ui.stepUpDone(true);
    await flushPromises();
    expect(e.approvals).toHaveLength(1);
    h.ui.act(h.inc(RESTART));
    await flushPromises();
    h.ui.confirmed('debian-nas');
    await flushPromises();
    expect(h.ui.state.stepUp).toBeUndefined();
    expect(e.approvals).toHaveLength(2);
  });

  it('T4 (fresh) always asks, even with a grant', async () => {
    const e = engine({ plan: () => ({ tier: 'T4', confirm: 'type', confirm_text: 'pluto', step_up: 'fresh' }) });
    fakeFetch(e.routes);
    const h = start();
    for (let i = 0; i < 2; i++) {
      h.ui.act(h.inc(RESTART));
      await flushPromises();
      h.ui.confirmed('pluto');
      await flushPromises();
      expect(h.ui.state.stepUp?.fresh).toBe(true);
      h.ui.stepUpDone(true);
      await flushPromises();
    }
    expect(e.approvals).toHaveLength(2);
  });

  it('step_up_required at approve: steps up and tries once more', async () => {
    const e = engine({ approve: (_id, _b, n) => (n === 1 ? refuse(403, 'step_up_required', 'Confirm it’s you first.') : undefined) });
    fakeFetch(e.routes);
    const h = start();
    h.ui.act(h.inc(PRUNE));
    await flushPromises();
    h.ui.confirmed();
    await flushPromises();
    expect(h.ui.state.stepUp).toBeDefined();
    h.ui.stepUpDone(true);
    await flushPromises();
    expect(e.approvals).toHaveLength(2);
    expect(h.live.fixes[PRUNE]?.state).toBe('accepted');
  });

  it('plan_expired: re-plans once, silently, and approves the new plan', async () => {
    let planned = 0;
    const e = engine({
      plan: () => ({ id: `p_${++planned}` }),
      approve: (_id, _b, n) => (n === 1 ? refuse(410, 'plan_expired', 'That plan expired.') : undefined),
    });
    fakeFetch(e.routes);
    const h = start();
    h.ui.act(h.inc(PRUNE));
    await flushPromises();
    h.ui.confirmed();
    await flushPromises();
    expect(e.plans).toHaveLength(2);
    expect(e.approvals.map((a) => a.plan)).toEqual(['p_1', 'p_2']);
    expect(h.ui.state.confirm).toBeUndefined();
    expect(h.ui.state.toasts.some((t) => t.tone === 'error')).toBe(false);
  });

  it('plan_changed: shows the new plan with the hub’s sentence (a review for T1)', async () => {
    const changed = planFor('pve.guest.backup', 'pve:minig5/qemu/100', { id: 'p_new', hash: 'sha256:new', impact: [{ resource: 'app:homeassistant', via: 'runs_on', depth: 1 }] });
    const e = engine({ approve: (id) => (id === 'p_new' ? undefined : refuse(409, 'plan_changed', 'homeassistant moved to another host since this was planned.', { plan: changed })) });
    fakeFetch(e.routes);
    const h = start();
    h.ui.act(h.inc(BACKUP));
    await flushPromises();
    expect(h.ui.state.confirm).toMatchObject({ kind: 'review', note: 'homeassistant moved to another host since this was planned.', plan: { id: 'p_new' } });
    h.ui.confirmed();
    await flushPromises();
    expect(e.approvals.map((a) => a.plan)).toEqual(['p_pve_guest_backup_pve_minig5_qemu_100', 'p_new']);
  });

  it('locked, precondition, forbidden, invalid: the hub’s sentence in an error toast, and at the incident', async () => {
    for (const [status, code, message] of [
      [409, 'locked', 'Another task is rebooting minig5. Try again when it’s done.'],
      [409, 'precondition', 'homeassistant is already backing up.'],
      [403, 'forbidden', 'Operators can’t back up guests on minig5.'],
      [422, 'invalid', 'Pick a storage for the backup.'],
    ] as const) {
      const e = engine(code === 'locked' ? { approve: () => refuse(status, code, message) } : { plan: () => refuse(status, code, message) });
      fakeFetch(e.routes);
      const h = start();
      h.ui.act(h.inc(BACKUP));
      await flushPromises();
      expect(h.ui.state.toasts.at(-1)).toMatchObject({ text: message, tone: 'error' });
      expect(h.live.fixes[BACKUP]).toMatchObject({ state: 'failed', error: message });
      h.live.stop();
    }
  });

  it('unavailable: says why and links to the instance’s settings', async () => {
    const e = engine({ plan: () => refuse(409, 'unavailable', 'Add a control token to let Cha Dash back up guests on minig5.', { recipe: 'i_minig5' }) });
    fakeFetch(e.routes);
    const h = start();
    h.mountIn(InboxSheet);
    h.ui.openInbox(BACKUP);
    await flushPromises();
    h.ui.act(h.inc(BACKUP));
    await flushPromises();
    await nextTick();
    const t = h.ui.state.toasts.at(-1)!;
    expect(t).toMatchObject({ text: 'Add a control token to let Cha Dash back up guests on minig5.', tone: 'error', action: { label: 'Open its settings' } });
    const item = document.querySelector('.ib.open')!;
    expect(item.querySelector('.err')?.textContent).toBe('Add a control token to let Cha Dash back up guests on minig5.');
    [...item.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent === 'Open its settings')!.click();
    expect(route.value.path).toBe('/settings/integrations/i_minig5');
  });

  it('prefers the hub’s availability on the incident’s action', () => {
    const h = start();
    const inc = JSON.parse(JSON.stringify(h.inc(BACKUP))) as Incident;
    inc.suggested!.unavailable = 'Add a control token to let Cha Dash back up guests on minig5.';
    expect(h.ui.canRun(inc)).toEqual({ ok: false, why: 'Add a control token to let Cha Dash back up guests on minig5.' });
    inc.suggested!.unavailable = undefined as unknown as string;
    delete inc.suggested!.unavailable;
    inc.suggested!.plan_ready = false;
    expect(h.ui.canRun(inc).ok).toBe(false);
    inc.suggested!.plan_ready = true;
    expect(h.ui.canRun(inc).ok).toBe(true);
  });
});

describe('tasks', () => {
  it('show the task’s step and progress at the incident, and a toast when it ends', async () => {
    const e = engine();
    fakeFetch(e.routes);
    const h = start();
    h.mountIn(InboxSheet);
    h.mountIn(TaskPresence);
    h.ui.openInbox(BACKUP);
    await flushPromises();
    h.ui.act(h.inc(BACKUP));
    await flushPromises();
    const task = taskFor(planFor('pve.guest.backup', 'pve:minig5/qemu/100'));
    // The hub publishes it on the tasks topic and moves it along.
    h.ws.push({ op: 'diff', id: h.ws.subId('tasks'), seq: 2, patch: [{ op: 'add', path: toPointer('tasks', 'tasks', task.id), value: { ...task, step: 'Write the backup to pbs-jupiter', progress: 0.5 } }] });
    await nextTick();
    const item = document.querySelector('.ib.open')!;
    expect(item.querySelector('.working')?.textContent).toContain('Back up homeassistant now: Write the backup to pbs-jupiter · 50%');
    expect(document.querySelector('.tp')?.textContent).toContain('Back up homeassistant now · Write the backup to pbs-jupiter · 50%');

    h.ws.push({ op: 'diff', id: h.ws.subId('tasks'), seq: 3, patch: [{ op: 'replace', path: toPointer('tasks', 'tasks', task.id, 'state'), value: 'succeeded' }] });
    await nextTick();
    expect(h.ui.state.toasts.at(-1)?.text).toBe('Done: Back up homeassistant now.');
    expect(document.querySelector('.tp')).toBeNull();
  });

  it('a failed task says the hub’s sentence, at the incident and in a toast', async () => {
    const e = engine();
    fakeFetch(e.routes);
    const h = start();
    h.ui.act(h.inc(BACKUP));
    await flushPromises();
    const task = taskFor(planFor('pve.guest.backup', 'pve:minig5/qemu/100'), { state: 'failed', error: { code: 'verify_failed', message: 'The backup didn’t show up on pbs-jupiter within 10 minutes.' } });
    h.ws.push({ op: 'diff', id: h.ws.subId('tasks'), seq: 2, patch: [{ op: 'add', path: toPointer('tasks', 'tasks', task.id), value: task }] });
    await nextTick();
    expect(h.ui.state.toasts.at(-1)).toMatchObject({ text: 'The backup didn’t show up on pbs-jupiter within 10 minutes.', tone: 'error' });
    expect(h.live.fixes[BACKUP]).toEqual({ state: 'failed', error: 'The backup didn’t show up on pbs-jupiter within 10 minutes.' });
  });

  it('are polled with GET /api/tasks/{id} on a hub without the topic', async () => {
    const e = engine();
    const task = taskFor(planFor('pve.guest.backup', 'pve:minig5/qemu/100'));
    let polls = 0;
    fakeFetch({ ...e.routes, [`GET /api/tasks/${task.id}`]: () => ({ status: 200, body: { ...task, state: ++polls < 2 ? 'running' : 'succeeded' } }) });
    const h = start();
    h.ws.push({ op: 'error', id: h.ws.subId('tasks'), code: 'unknown_topic', msg: 'no such topic' });
    h.ui.act(h.inc(BACKUP));
    await vi.waitFor(() => expect(h.ui.state.toasts.at(-1)?.text).toBe('Done: Back up homeassistant now.'), { timeout: 2000 });
    expect(polls).toBe(2);
  });
});

describe('the Demo Home on the engine', () => {
  it('plans and approves demo fixes like any action, and never uses a demo-only endpoint', async () => {
    const e = engine();
    const hub = fakeFetch(e.routes);
    const h = start({ demo: true });
    h.ui.act(h.inc(BACKUP));
    await flushPromises();
    h.ui.act(h.inc(PRUNE));
    await flushPromises();
    expect(h.ui.state.confirm).toMatchObject({ kind: 'hold', plan: { action: 'pbs.datastore.prune' } });
    h.ui.confirmed();
    await flushPromises();
    expect(hub.calls.map((c) => `${c.method} ${c.path.replace(/plans\/[^/]+/, 'plans/{id}')}`)).toEqual([
      'POST /api/actions/plan',
      'POST /api/plans/{id}/approve',
      'POST /api/actions/plan',
      'POST /api/plans/{id}/approve',
    ]);
    expect(e.plans.map((x) => x.action)).toEqual(['pve.guest.backup', 'pbs.datastore.prune']);
    expect(hub.calls.some((c) => c.path.startsWith('/api/demo'))).toBe(false);
  });

  it('shows the hub’s sentence when planning a demo fix answers 404', async () => {
    const hub = fakeFetch({ 'POST /api/actions/plan': refuse(404, 'not_found', 'There’s no action called "pve.guest.backup".') });
    const h = start({ demo: true });
    h.ui.act(h.inc(BACKUP));
    await flushPromises();
    expect(h.live.fixes[BACKUP]).toEqual({ state: 'failed', error: 'There’s no action called "pve.guest.backup".' });
    expect(hub.calls.map((c) => c.path)).toEqual(['/api/actions/plan']);
  });
});

describe('cancelling a task', () => {
  const push = (h: ReturnType<typeof start>, task: ReturnType<typeof taskFor>) =>
    h.ws.push({ op: 'diff', id: h.ws.subId('tasks'), seq: 2, patch: [{ op: 'add', path: toPointer('tasks', 'tasks', task.id), value: task }] });

  it('offers Cancel on the presence and at the incident only while a cancellable task runs', async () => {
    const e = engine();
    fakeFetch(e.routes);
    const h = start();
    h.mountIn(InboxSheet);
    h.mountIn(TaskPresence);
    h.ui.openInbox(BACKUP);
    await flushPromises();
    h.ui.act(h.inc(BACKUP));
    await flushPromises();
    const p = planFor('pve.guest.backup', 'pve:minig5/qemu/100');
    // Not cancellable: no button anywhere.
    push(h, taskFor(p));
    await nextTick();
    expect(document.querySelector('.tp-cancel')).toBeNull();
    expect(document.querySelector('.working-cancel')).toBeNull();

    h.ws.push({ op: 'diff', id: h.ws.subId('tasks'), seq: 3, patch: [{ op: 'add', path: toPointer('tasks', 'tasks', `t_${p.id}`, 'cancellable'), value: true }] });
    await nextTick();
    const presence = document.querySelector<HTMLButtonElement>('.tp-cancel')!;
    const atIncident = document.querySelector<HTMLButtonElement>('.ib.open .working-cancel')!;
    expect(presence.getAttribute('aria-label')).toBe('Cancel Back up homeassistant now');
    expect(atIncident.textContent?.trim()).toBe('Cancel');
    atIncident.click();
    await flushPromises();
    expect(e.cancels).toEqual([`t_${p.id}`]);

    // The hub cancels it: it ends as cancelled, the buttons go, the toast says so.
    h.ws.push({ op: 'diff', id: h.ws.subId('tasks'), seq: 4, patch: [{ op: 'replace', path: toPointer('tasks', 'tasks', `t_${p.id}`, 'state'), value: 'cancelled' }] });
    await nextTick();
    expect(document.querySelector('.tp-cancel')).toBeNull();
    expect(h.ui.state.toasts.at(-1)?.text).toBe('Cancelled: Back up homeassistant now.');
  });

  it('shows the hub’s sentence when it refuses', async () => {
    const e = engine({ cancel: () => refuse(409, 'not_cancellable', 'Back up homeassistant now can’t be stopped once it has started.') });
    fakeFetch(e.routes);
    const h = start();
    const task = taskFor(planFor('pve.guest.backup', 'pve:minig5/qemu/100'), { cancellable: true });
    await h.ui.cancelTask(task);
    expect(h.ui.state.toasts.at(-1)).toMatchObject({ text: 'Back up homeassistant now can’t be stopped once it has started.', tone: 'error' });
    // A finished task isn't asked about.
    await h.ui.cancelTask({ ...task, state: 'succeeded' });
    expect(e.cancels).toHaveLength(1);
  });
});

describe('roles', () => {
  it('follow the plan’s table', () => {
    const p = (r: string) => permissions(r);
    expect(['owner', 'admin', 'operator', 'viewer', 'household'].map((r) => [0, 1, 2, 3, 4].filter((n) => p(r).act(`T${n}`)).length)).toEqual([5, 4, 3, 1, 1]);
    expect(p('household').act('T1', true)).toBe(true);
    expect(p('household').inbox).toBe(false);
    // Below admin, Settings is your passkeys only; integrations, the wizard and decisions are admin.
    for (const r of ['operator', 'viewer', 'household']) {
      expect(p(r).settings).toBe(true);
      expect(p(r).panes).toEqual(['security']);
      expect(p(r).integrations).toBe(false);
      expect(p(r).editBoards).toBe(false);
      expect(p(r).decide).toBe(false);
    }
    expect(p('admin').integrations && p('admin').decide).toBe(true);
    expect(p('admin').panes).toEqual(['integrations', 'import', 'backup', 'users', 'audit', 'security']);
    expect(p('admin').securitySettings).toBe(false);
    expect(p('owner').securitySettings).toBe(true);
    expect(p('someone-new').role).toBe('viewer');
    expect(assignableRoles('owner')).not.toContain('owner');
    expect(assignableRoles('operator')).toEqual([]);
  });

  it('hide the buttons a role can’t use: a viewer sees no T1 or T2, an operator no T3', async () => {
    const v = start({ role: 'viewer' });
    v.mountIn(InboxSheet);
    v.ui.openInbox(PRUNE);
    await flushPromises();
    await nextTick();
    const open = document.querySelector('.ib.open')!;
    expect([...open.querySelectorAll('.acts .btn')].map((b) => b.textContent)).toEqual(['Open PBST0']);
    expect(open.textContent).toContain('Your role can see this but not run “Prune per policy”');
    v.ui.act(v.inc(PRUNE));
    expect(v.ui.state.toasts.at(-1)?.text).toBe('Your role can’t run T2 actions.');

    document.body.innerHTML = '';
    const o = start({ role: 'operator' });
    const inc = JSON.parse(JSON.stringify(o.inc(PRUNE))) as Incident;
    expect(o.ui.canRun(inc).ok).toBe(true);
    inc.suggested!.tier = 'T3';
    expect(o.ui.canRun(inc).hidden).toBe(true);
  });

  it('household sees boards only: no Needs attention, no Inbox, no Edit layout, Settings only for passkeys', async () => {
    const h = start({ role: 'household' });
    const na = h.mountIn(NeedsAttention);
    expect(na.find('.attn-sec').exists()).toBe(false);
    const brief = h.mountIn(Briefing);
    expect(brief.find('.pill').exists()).toBe(false);
    expect(brief.find('.phrase').exists()).toBe(false);
    h.ui.openInbox();
    expect(h.ui.state.inboxOpen).toBe(false);
    h.ui.openSettings('integrations');
    expect(route.value.path).toBe('/');
    const board = h.mountIn(BoardSurface, { board: defaultBoard(docs(withIncidents).graph, Date.parse(withIncidents.now)), breakpoint: 'desktop' });
    expect(board.text()).not.toContain('Edit layout');
    const cmds = buildCommands({
      inbox: h.live.inbox.value,
      can: h.ui.can.value,
      do: { openInbox: vi.fn(), toggleEdit: vi.fn(), openBoard: vi.fn(), reveal: vi.fn(), openSettings: vi.fn(), addIntegration: vi.fn(), setTheme: vi.fn(), signOut: vi.fn() },
    });
    expect(cmds.filter((c) => ['Inbox', 'Integrations'].includes(c.group) || c.id === 'inbox' || c.id === 'edit')).toEqual([]);
    expect(cmds.filter((c) => c.group === 'Settings').map((c) => c.id)).toEqual(['settings:security']);
    h.ui.openSettings('security');
    expect(route.value.path).toBe('/settings/security');
    expect(cmds.some((c) => c.id === 'signout')).toBe(true);
  });

  it('⌘K lists the Users and Audit panes for owners and admins only; everyone gets their passkeys', () => {
    const d = { openInbox: vi.fn(), toggleEdit: vi.fn(), openBoard: vi.fn(), reveal: vi.fn(), openSettings: vi.fn(), addIntegration: vi.fn(), setTheme: vi.fn(), signOut: vi.fn() };
    const ids = (role: string) => buildCommands({ can: permissions(role), do: d }).filter((c) => c.group === 'Settings').map((c) => c.id);
    expect(ids('admin')).toEqual(['settings:integrations', 'settings:import', 'settings:backup', 'settings:users', 'settings:audit', 'settings:security']);
    expect(ids('operator')).toEqual(['settings:security']);
    expect(ids('household')).toEqual(['settings:security']);
  });
});
