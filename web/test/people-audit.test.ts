/**
 * Settings → Audit, Users and Security's step-up setting, the step-up dialog,
 * and which panes each role gets. The hub's side is faked to the shapes in
 * 08 · v0.2 → API shapes.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import StepUpDialog from '../src/components/StepUpDialog.vue';
import AuditPane from '../src/settings/AuditPane.vue';
import SettingsSheet from '../src/settings/SettingsSheet.vue';
import StepUpSetting from '../src/settings/StepUpSetting.vue';
import UsersPane from '../src/settings/UsersPane.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { navigate, syncRoute } from '../src/lib/router';
import { refuse } from './fake-engine';
import { fakeFetch, type Handler } from './fake-fetch';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

beforeEach(() => {
  setDisplayTimeZone('UTC');
  navigate('/', { replace: true });
});
afterEach(() => {
  vi.unstubAllGlobals();
  Object.defineProperty(window, 'isSecureContext', { value: undefined, configurable: true });
  document.body.innerHTML = '';
});

async function settle(): Promise<void> {
  await vi.dynamicImportSettled();
  await flushPromises();
  await nextTick();
}

const EVENTS = [
  { id: 'a3', seq: 4812, at: '2026-10-03T14:01:00Z', actor: { kind: 'user', name: 'owner', via: 'web', ip: '10.0.0.12' }, action: 'pve.guest.restart', target: 'pve:jupiter-nas/lxc/100', params: {}, tier: 'T2', result: 'ok', duration_ms: 8123, task: 't_1', hash: 'sha256:cccccccccccccccc', prev_hash: 'sha256:bbbbbbbbbbbbbbbb' },
  { id: 'a2', seq: 4811, at: '2026-10-03T13:59:00Z', actor: { kind: 'user', name: 'sam', via: 'web' }, action: 'pbs.datastore.prune', target: 'pbs:main', tier: 'T2', result: 'refused', error: { code: 'forbidden', message: 'Operators can’t prune datastores.' } },
];

describe('Settings → Audit', () => {
  function hub(over: Record<string, Handler | { status: number; body?: unknown }> = {}) {
    return fakeFetch({
      'GET /api/audit/verify': { status: 200, body: { ok: true, checked: 4812 } },
      'GET /api/audit': (_b, url) =>
        url.searchParams.get('cursor') === 'c1'
          ? { status: 200, body: { events: [{ ...EVENTS[1], id: 'a1', seq: 4810, action: 'settings.security.update', target: '', result: 'ok' }] } }
          : { status: 200, body: { events: EVENTS, next: 'c1' } },
      ...over,
    });
  }

  it('lists events newest first with who, what, the result and the hub’s error, and says the chain is intact', async () => {
    hub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(AuditPane);
    await settle();
    expect(w.find('.st-note').text()).toBe('The audit log is intact · 4,812 events checked');
    const rows = w.findAll('.au-list > li');
    expect(rows).toHaveLength(2);
    expect(rows[0]!.find('.t').text()).toBe('pve.guest.restart · debian-nas');
    expect(rows[0]!.find('.m').text()).toBe('owner · via web · T2 · done in 8s');
    expect(rows[0]!.find('[role="img"]').attributes('aria-label')).toBe('done');
    expect(rows[1]!.text()).toContain('refused');
    expect(rows[1]!.find('.au-err').text()).toBe('Operators can’t prune datastores.');
    // Details: the event number, where from, the task and the chain link.
    await rows[0]!.find('button').trigger('click');
    expect(rows[0]!.find('.au-detail').text()).toContain('4,812');
    expect(rows[0]!.find('.au-detail').text()).toContain('10.0.0.12');
    expect(rows[0]!.find('.au-detail').text()).toContain('cccccccccccc ← bbbbbbbbbbbb');
    expect(w.find('a[href="/api/audit/export"]').attributes('download')).toBeDefined();
  });

  it('filters with the query the hub reads, and pages with the cursor', async () => {
    const f = hub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(AuditPane);
    await settle();
    await w.find('input[placeholder="username"]').setValue('sam');
    await w.find('select').setValue('refused');
    await w.findAll('select')[1]!.setValue('24h');
    await w.find('form.au-filters').trigger('submit');
    await settle();
    const q = new URL(`http://x${f.calls.filter((c) => c.path.startsWith('/api/audit?')).at(-1)!.path}`).searchParams;
    expect(q.get('actor')).toBe('sam');
    expect(q.get('result')).toBe('refused');
    expect(q.get('since')).toMatch(/^\d{4}-\d\d-\d\dT/);
    expect(q.get('limit')).toBe('50');
    await w.findAll('button').find((b) => b.text() === 'Show older')!.trigger('click');
    await settle();
    expect(f.calls.at(-1)!.path).toContain('cursor=c1');
    expect(w.findAll('.au-list > li')).toHaveLength(3);
    expect(w.findAll('button').some((b) => b.text() === 'Show older')).toBe(false);
  });

  it('says where the chain breaks', async () => {
    hub({ 'GET /api/audit/verify': { status: 200, body: { ok: false, checked: 4812, first_break: { seq: 1234, at: '2026-09-30T08:00:00Z' } } } });
    const h = harness(docs(withIncidents));
    const w = h.mountIn(AuditPane);
    await settle();
    expect(w.find('.st-note.bad').text()).toContain('The audit log’s chain breaks at event 1,234 (Wed 30 Sept 08:00) · 4,812 events checked.');
  });
});

describe('Settings → Users', () => {
  const USERS = [
    { id: 'u1', username: 'owner', role: 'owner', disabled: false, has_passkey: true, last_seen_at: '2026-10-03T14:00:00Z' },
    { id: 'u2', username: 'sam', role: 'operator', disabled: false, has_passkey: false, last_seen_at: null },
    { id: 'u3', username: 'kid', role: 'household', disabled: true, has_passkey: false, last_seen_at: null },
  ];

  function hub(over: Record<string, Handler | { status: number; body?: unknown }> = {}) {
    return fakeFetch({
      'GET /api/users': { status: 200, body: USERS },
      'PUT /api/users/u2': (b) => ({ status: 200, body: { ...USERS[1], ...(b as object) } }),
      'PUT /api/users/u3': (b) => ({ status: 200, body: { ...USERS[2], ...(b as object) } }),
      'POST /api/users': (b) => ({ status: 201, body: { id: 'u4', disabled: false, has_passkey: false, last_seen_at: null, ...(b as { username: string; role: string; password?: string }), password: undefined } }),
      ...over,
    });
  }

  it('lists role, passkey, last seen and disabled; the owner can change everyone else', async () => {
    const f = hub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(UsersPane);
    await settle();
    const rows = w.findAll('.us-row');
    expect(rows.map((r) => r.find('.t').text().replace(/\s+/g, ' '))).toEqual(['owner you', 'sam', 'kid disabled']);
    expect(rows[1]!.find('.m').text().replace(/\s+/g, ' ')).toBe('Operator · no passkey · never signed in');
    expect(rows[0]!.find('select').exists()).toBe(false); // yourself, and the owner
    await rows[1]!.find('select').setValue('admin');
    await settle();
    expect(f.last('PUT', '/api/users/u2')?.body).toEqual({ role: 'admin' });
    expect(h.ui.state.toasts.at(-1)?.text).toBe('sam is now admin.');
    await w.findAll('.us-row')[2]!.findAll('button').find((b) => b.text() === 'Enable')!.trigger('click');
    await settle();
    expect(f.last('PUT', '/api/users/u3')?.body).toEqual({ disabled: false });
  });

  it('an admin can’t touch the owner, and has no ownership transfer', async () => {
    hub();
    const h = harness(docs(withIncidents));
    h.session.state.user = { id: 'u2', username: 'sam', role: 'admin' };
    const w = h.mountIn(UsersPane);
    await settle();
    const owner = w.findAll('.us-row')[0]!;
    expect(owner.find('select').exists()).toBe(false);
    expect(owner.findAll('button')).toHaveLength(0);
    expect(w.text()).not.toContain('Make owner');
    expect(w.findAll('.us-row')[2]!.find('select option[value="owner"]').exists()).toBe(false);
  });

  it('adds a user after checking the username and password', async () => {
    const f = hub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(UsersPane);
    await settle();
    await w.findAll('button').find((b) => b.text() === 'Add a user')!.trigger('click');
    const form = w.find('form.us-add');
    await form.find('input[type="text"]').setValue('-bad');
    await form.find('input[type="password"]').setValue('short');
    await form.trigger('submit');
    expect(form.findAll('.help.bad')).toHaveLength(2);
    expect(f.last('POST', '/api/users')).toBeUndefined();
    await form.find('input[type="text"]').setValue('sam');
    await form.find('input[type="password"]').setValue('a long enough password');
    await form.find('select').setValue('household');
    await form.trigger('submit');
    await settle();
    expect(f.last('POST', '/api/users')?.body).toEqual({ username: 'sam', password: 'a long enough password', role: 'household' });
    expect(w.findAll('.us-row').map((r) => r.find('.t').text().trim())).toContain('sam');
  });

  it('hands ownership over only after the name is typed, with step-up when the hub asks', async () => {
    let n = 0;
    const f = hub({
      'POST /api/users/u2/transfer-ownership': () => (++n === 1 ? refuse(403, 'step_up_required', 'Confirm it’s you first.') : { status: 204 }),
      'GET /api/auth/session': { status: 200, body: { user: { id: 'u1', username: 'owner', role: 'admin' } } },
    });
    const h = harness(docs(withIncidents));
    const w = h.mountIn(UsersPane);
    await settle();
    await w.findAll('.us-row')[1]!.findAll('button').find((b) => b.text() === 'Make owner…')!.trigger('click');
    const form = w.find('form.us-transfer');
    expect(form.text()).toContain('This can’t be undone from your side.');
    const go = form.find('button[type="submit"]');
    expect(go.attributes('disabled')).toBeDefined();
    await form.find('input').setValue('sam');
    expect(go.attributes('disabled')).toBeUndefined();
    await form.trigger('submit');
    await settle();
    expect(h.ui.state.stepUp?.reason).toBe('Hand ownership to sam');
    h.ui.stepUpDone(true);
    await settle();
    expect(n).toBe(2);
    expect(f.last('POST', '/api/users/u2/transfer-ownership')?.body).toEqual({ typed: 'sam' });
    expect(h.session.state.user?.role).toBe('admin');
    expect(h.ui.state.toasts.at(-1)?.text).toBe('sam is the owner now.');
  });
});

describe('Settings → Security → Confirming destructive actions', () => {
  it('saves through PUT, stepping up when the hub asks, and puts the choice back if step-up is closed', async () => {
    let puts = 0;
    const f = fakeFetch({
      'GET /api/settings/security': { status: 200, body: { posture: 'relaxed', step_up: 'passkey_or_password', levels: [] } },
      'PUT /api/settings/security': (b) => (++puts % 2 === 1 ? refuse(403, 'step_up_required', 'Confirm it’s you first.') : { status: 200, body: { posture: 'relaxed', ...(b as object), levels: [] } }),
    });
    const h = harness(docs(withIncidents));
    const w = h.mountIn(StepUpSetting, { hasPasskey: false });
    await settle();
    const only = w.find('input[value="passkey_only"]');
    await only.setValue(true);
    await only.trigger('change');
    await settle();
    expect(h.ui.state.stepUp).toBeDefined();
    h.ui.stepUpDone(true);
    await settle();
    expect(f.last('PUT', '/api/settings/security')?.body).toEqual({ step_up: 'passkey_only' });
    expect((w.find('input[value="passkey_only"]').element as HTMLInputElement).checked).toBe(true);
    expect(w.find('.st-note.attn').text()).toContain('You don’t have a passkey yet');

    // Back, but closing the step-up keeps it as it was.
    const either = w.find('input[value="passkey_or_password"]');
    await either.setValue(true);
    await either.trigger('change');
    await settle();
    h.ui.stepUpDone(false);
    await settle();
    expect((w.find('input[value="passkey_only"]').element as HTMLInputElement).checked).toBe(true);
    expect(w.find('.err').exists()).toBe(false);
  });
});

describe('the step-up dialog', () => {
  it('asks for the password when the hub falls back to it, and resolves the step-up', async () => {
    const f = fakeFetch({
      'POST /api/auth/step-up/begin': { status: 200, body: { method: 'password' } },
      'POST /api/auth/step-up': (b) => ((b as { password: string }).password === 'right password' ? { status: 200, body: { until: '2026-10-03T14:10:00Z' } } : refuse(401, 'bad_password', 'That password doesn’t match.')),
    });
    const h = harness(docs(withIncidents));
    const done = vi.fn();
    h.ui.state.stepUp = { reason: 'Remove debian-nas', fresh: false, resolve: done };
    h.mountIn(StepUpDialog);
    await settle();
    const dlg = document.querySelector<HTMLDialogElement>('dialog.stepup')!;
    expect(dlg.textContent).toContain('Before “Remove debian-nas”');
    expect(dlg.textContent).toContain('Add a passkey in Settings → Security');
    const pw = dlg.querySelector<HTMLInputElement>('input[type="password"]')!;
    expect(document.activeElement).toBe(pw);
    pw.value = 'wrong password';
    pw.dispatchEvent(new Event('input'));
    dlg.querySelector('form')!.dispatchEvent(new Event('submit'));
    await settle();
    expect(dlg.querySelector('.help.bad')?.textContent).toBe('That password doesn’t match.');
    expect(done).not.toHaveBeenCalled();
    pw.value = 'right password';
    pw.dispatchEvent(new Event('input'));
    dlg.querySelector('form')!.dispatchEvent(new Event('submit'));
    await settle();
    expect(f.last('POST', '/api/auth/step-up')?.body).toEqual({ password: 'right password' });
    expect(done).toHaveBeenCalledWith(true);
  });

  it('uses a passkey when the hub sends WebAuthn options', async () => {
    const get = vi.fn().mockResolvedValue({
      id: 'cred-1',
      rawId: new Uint8Array([1, 2]).buffer,
      type: 'public-key',
      response: { clientDataJSON: new Uint8Array([123, 125]).buffer, authenticatorData: new Uint8Array([9]).buffer, signature: new Uint8Array([7]).buffer, userHandle: null },
      getClientExtensionResults: () => ({}),
    });
    vi.stubGlobal('PublicKeyCredential', function PublicKeyCredential() {});
    Object.defineProperty(navigator, 'credentials', { value: { get }, configurable: true });
    Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true });
    const f = fakeFetch({
      'POST /api/auth/step-up/begin': { status: 200, body: { publicKey: { challenge: 'AAEC', rpId: 'localhost' } } },
      'POST /api/auth/step-up': { status: 200, body: { until: '2026-10-03T14:10:00Z' } },
    });
    const h = harness(docs(withIncidents));
    const done = vi.fn();
    h.ui.state.stepUp = { reason: 'Delete snapshot pre-upgrade', fresh: true, resolve: done };
    h.mountIn(StepUpDialog);
    await settle();
    const dlg = document.querySelector<HTMLDialogElement>('dialog.stepup')!;
    expect(dlg.textContent).toContain('it asks every time');
    dlg.querySelector('form')!.dispatchEvent(new Event('submit'));
    await settle();
    expect(get).toHaveBeenCalledTimes(1);
    expect((f.last('POST', '/api/auth/step-up')?.body as { id: string }).id).toBe('cred-1');
    expect(done).toHaveBeenCalledWith(true);
  });

  it('under Passkey only with no passkey, explains and links to Settings → Security', async () => {
    fakeFetch({ 'POST /api/auth/step-up/begin': refuse(409, 'passkey_required', 'This hub asks for a passkey before destructive actions. Add one in Settings → Security.') });
    const h = harness(docs(withIncidents));
    const done = vi.fn();
    h.ui.state.stepUp = { reason: 'Remove debian-nas', fresh: false, resolve: done };
    h.ui.state.confirm = {
      kind: 'type',
      req: { action: 'x', target: 'y', label: 'Remove debian-nas' },
      plan: { id: 'p_1', hash: 'sha256:1', action: 'x', label: 'Remove debian-nas', target: 'y', tier: 'T3', confirm: 'type', confirm_text: 'debian-nas', step_up: 'required' },
      action: { id: 'x', label: 'Remove debian-nas', tier: 'T3', target: 'y' },
    };
    h.mountIn(StepUpDialog);
    await settle();
    const dlg = document.querySelector<HTMLDialogElement>('dialog.stepup')!;
    expect(dlg.querySelector('[role="alert"]')?.textContent).toBe('This hub asks for a passkey before destructive actions. Add one in Settings → Security.');
    [...dlg.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent === 'Open Settings → Security')!.click();
    expect(done).toHaveBeenCalledWith(false);
    expect(h.ui.state.confirm).toBeUndefined();
    expect(location.pathname).toBe('/settings/security');
  });

  it('Esc cancels', async () => {
    fakeFetch({ 'POST /api/auth/step-up/begin': { status: 200, body: { method: 'password' } } });
    const h = harness(docs(withIncidents));
    const done = vi.fn();
    h.ui.state.stepUp = { reason: 'x', fresh: false, resolve: done };
    h.mountIn(StepUpDialog);
    await settle();
    document.querySelector('dialog.stepup')!.dispatchEvent(new Event('cancel', { cancelable: true }));
    expect(done).toHaveBeenCalledWith(false);
  });
});

describe('Settings by role', () => {
  function sheetFor(role: string, path = '/settings/integrations') {
    fakeFetch({
      'GET /api/integrations/catalog': { status: 200, body: [] },
      'GET /api/integrations/instances': { status: 200, body: [] },
      'GET /api/auth/passkeys': { status: 200, body: [] },
      'GET /api/settings/security': { status: 200, body: { posture: 'relaxed', step_up: 'passkey_or_password', levels: [] } },
    });
    history.replaceState(null, '', path);
    syncRoute();
    const h = harness(docs(withIncidents));
    h.session.state.user = { id: 'u9', username: 'someone', role };
    const w = h.mountIn(SettingsSheet);
    return { h, w };
  }

  it('owners get every pane; admins the same, with Security as passkeys only', async () => {
    const $ = (sel: string) => [...document.querySelectorAll(sel)].map((e) => e.textContent?.trim());
    const o = sheetFor('owner');
    await settle();
    expect($('.st-nav a span')).toEqual(['Integrations', 'Stacks', 'Import', 'Backup', 'Users', 'Audit', 'Notifications', 'Security']);
    o.w.unmount();
    document.body.innerHTML = '';
    sheetFor('admin', '/settings/security');
    await settle();
    expect($('.st-nav a small').at(-1)).toBe('passkeys');
    const pane = document.querySelector('.st-pane')!.textContent!;
    expect(pane).not.toContain('Security level');
    expect(pane).not.toContain('Confirming destructive actions');
    expect(pane).toContain('Passkeys');
  });
});
