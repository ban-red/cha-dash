/**
 * Accessibility review fixes in Settings: focus moves on purpose when a
 * confirm group, form or row comes and goes; busy controls stay focusable
 * (aria-disabled) so the step-up dialog can hand focus back; settings that
 * used to save on `change` wait for Save (WCAG 3.2.2); the Audit pane says
 * how many events match and when "Show older" fails; a failed rule save puts
 * its checkbox back; and axe over the states the review named.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, type DOMWrapper } from '@vue/test-utils';
import { nextTick } from 'vue';
import AuditPane from '../src/settings/AuditPane.vue';
import NotificationsPane from '../src/settings/NotificationsPane.vue';
import StackSecrets from '../src/settings/StackSecrets.vue';
import StepUpSetting from '../src/settings/StepUpSetting.vue';
import TokensPane from '../src/settings/TokensPane.vue';
import UsersPane from '../src/settings/UsersPane.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { navigate } from '../src/lib/router';
import { axeViolations } from './axe';
import { refuse } from './fake-engine';
import { fakeFetch, type Handler } from './fake-fetch';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

type Routes = Record<string, Handler | { status: number; body?: unknown }>;

beforeEach(() => {
  setDisplayTimeZone('UTC');
  navigate('/', { replace: true });
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

async function settle(): Promise<void> {
  await vi.dynamicImportSettled();
  await flushPromises();
  await nextTick();
  await flushPromises();
}

/** Focus a control the way a keyboard user would, then press it. */
async function press(el: DOMWrapper<Element> | HTMLElement): Promise<void> {
  const node = (el instanceof HTMLElement ? el : el.element) as HTMLElement;
  node.focus();
  node.click();
  await settle();
}

const active = () => document.activeElement as HTMLElement | null;
const byText = (text: string) => [...document.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === text);
const words = (el: Element | null | undefined) => (el?.textContent ?? '').replace(/\s+/g, ' ').trim();

/** A promise the test resolves when it likes: a hub that takes its time. */
function later<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

// ── API tokens ──

const TOKENS = [
  { id: 'tok_a', user_id: 'u1', username: 'owner', name: 'backup script', scopes: ['graph.read'], created_at: '2026-10-01T10:00:00Z', expires_at: '2026-12-30T10:00:00Z', last_used_at: null, revoked_at: null },
  { id: 'tok_c', user_id: 'u1', username: 'owner', name: 'cron', scopes: ['graph.read'], created_at: '2026-09-01T10:00:00Z', expires_at: '2026-12-30T10:00:00Z', last_used_at: null, revoked_at: null },
];
const SCOPES = {
  reads: [{ scope: 'graph.read', label: 'Read the Home Graph', description: 'Resources.', available: true }],
  families: [],
  actions: [],
  all: { scope: '*', label: 'Everything your role allows', description: 'Everything.', available: true },
  default_days: 90,
  max_days: 365,
};

function tokensHub(over: Routes = {}) {
  return fakeFetch({
    'GET /api/tokens': { status: 200, body: { tokens: TOKENS } },
    'GET /api/tokens/scopes': { status: 200, body: SCOPES },
    'POST /api/tokens': (b) => ({ status: 201, body: { token: { ...TOKENS[0], id: 'tok_new', name: (b as { name: string }).name }, secret: 'cdt_fake_secret_for_tests_only' } }),
    'DELETE /api/tokens/tok_a': { status: 200, body: { token: { ...TOKENS[0], revoked_at: '2026-10-03T14:06:00Z' } } },
    ...over,
  });
}

describe('API tokens: focus', () => {
  it('Revoke opens the confirm on Cancel, Cancel goes back to Revoke, a revoke lands on the heading', async () => {
    tokensHub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(TokensPane);
    await settle();
    await press(w.find('button[aria-label="Revoke backup script"]'));
    expect(active()?.textContent?.trim()).toBe('Cancel');
    expect(active()?.closest('.tk-revoke')).not.toBeNull();
    await press(active()!);
    expect(w.find('.tk-revoke').exists()).toBe(false);
    expect(active()?.getAttribute('aria-label')).toBe('Revoke backup script');
    await press(w.find('button[aria-label="Revoke backup script"]'));
    await press(w.find('.tk-revoke .btn.danger'));
    expect(h.ui.state.toasts.at(-1)?.text).toContain('Revoked “backup script”');
    expect(active()?.id).toBe('tk-h');
  });

  it('the confirm button stays focused and aria-disabled while the hub works, and after a failure', async () => {
    const slow = later<{ status: number; body?: unknown }>();
    tokensHub({ 'DELETE /api/tokens/tok_a': () => slow.promise });
    const h = harness(docs(withIncidents));
    const w = h.mountIn(TokensPane);
    await settle();
    await press(w.find('button[aria-label="Revoke backup script"]'));
    const go = w.find('.tk-revoke .btn.danger');
    await press(go);
    expect(go.attributes('aria-disabled')).toBe('true');
    expect(go.attributes('disabled')).toBeUndefined();
    expect(active()).toBe(go.element);
    slow.resolve(refuse(500, 'internal', 'The hub had a problem.'));
    await settle();
    expect(w.find('.tk-row .err').text()).toContain('The hub had a problem.');
    expect(active()).toBe(go.element);
    expect(go.attributes('aria-disabled')).toBeUndefined();
  });

  it('Cancel on the new-token form and “I’ve saved it” go back to New token', async () => {
    tokensHub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(TokensPane);
    await settle();
    await press(byText('New token')!);
    expect(active()?.tagName).toBe('INPUT');
    await press(w.find('form.tk-add').findAll('button').find((b) => b.text() === 'Cancel')!);
    expect(active()?.textContent?.trim()).toBe('New token');

    await press(byText('New token')!);
    await w.find('form.tk-add input[type="text"]').setValue('desk');
    await w.find('input[value="graph.read"]').setValue(true);
    await press(w.find('form.tk-add button[type="submit"]'));
    expect(active()?.tagName).toBe('INPUT'); // the secret
    await press(byText('I’ve saved it')!);
    expect(active()?.textContent?.trim()).toBe('New token');
  });
});

// ── Stack secrets ──

function secretsHub(over: Routes = {}) {
  return fakeFetch({
    'GET /api/stacks/secrets': {
      status: 200,
      body: { secrets: [{ name: 'api_token', set: false }, { name: 'db_password', set: true, updated_at: '2026-10-03T10:00:00Z' }] },
    },
    'DELETE /api/stacks/secrets/db_password': { status: 204 },
    'PUT /api/stacks/secrets/api_token': { status: 204 },
    ...over,
  });
}

describe('Stack secrets', () => {
  it('a secret that isn’t set says “Set” and is named “Set <name>”, and passes axe', async () => {
    secretsHub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(StackSecrets);
    await settle();
    const rows = w.findAll('.sks-row');
    const set = rows[0]!.find('.end button');
    expect(set.text()).toBe('Set');
    expect(set.attributes('aria-label')).toBe('Set api_token');
    const replace = rows[1]!.find('.end button');
    expect(replace.text()).toBe('Replace');
    expect(replace.attributes('aria-label')).toBe('Replace db_password');
    // Label in name: the visible word starts the accessible name.
    for (const b of [set, replace]) expect(b.attributes('aria-label')!.startsWith(b.text())).toBe(true);
    await press(set);
    expect(active()?.id).toMatch(/-val$/);
    expect(await axeViolations()).toEqual([]);
  });

  it('Delete focuses Cancel, Cancel goes back to Delete, a delete lands on the heading', async () => {
    const f = secretsHub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(StackSecrets);
    await settle();
    await press(w.find('button[aria-label="Delete db_password"]'));
    expect(active()?.textContent?.trim()).toBe('Cancel');
    expect(active()?.closest('.sks-confirm')).not.toBeNull();
    await press(active()!);
    expect(active()?.getAttribute('aria-label')).toBe('Delete db_password');
    await press(w.find('button[aria-label="Delete db_password"]'));
    await press(w.find('.sks-confirm .btn.danger'));
    expect(f.last('DELETE', '/api/stacks/secrets/db_password')).toBeDefined();
    expect(active()?.id).toBe('sks-h');
  });

  it('Save secret keeps focus while the step-up dialog is asked for', async () => {
    let n = 0;
    secretsHub({ 'PUT /api/stacks/secrets/api_token': () => (++n === 1 ? refuse(403, 'step_up_required', 'Confirm it’s you first.') : { status: 204 }) });
    const h = harness(docs(withIncidents));
    const w = h.mountIn(StackSecrets);
    await settle();
    await w.find('input[placeholder="db_password"]').setValue('api_token');
    await w.find('input[type="password"]').setValue('fake-value-for-tests');
    const save = w.find('.sks-form button[type="submit"]');
    await press(save);
    expect(h.ui.state.stepUp?.reason).toBe('Set the stack secret api_token');
    expect(active()).toBe(save.element);
    expect(save.attributes('aria-disabled')).toBe('true');
    // Pressing it again while it works does nothing.
    await press(save);
    expect(n).toBe(1);
    h.ui.stepUpDone(true);
    await settle();
    expect(n).toBe(2);
    expect(active()).toBe(save.element);
  });
});

// ── Notifications ──

const RULES = [
  { id: 'incident.down', label: 'Something is down', description: 'A problem opens at down.', enabled: true, resolved: true, channels: null, has_resolved: true, defaults: {}, updated_at: null },
  { id: 'incident.attention', label: 'Needs attention for a while', description: 'After the grace.', enabled: true, resolved: false, grace_minutes: 5, channels: null, has_resolved: true, defaults: {}, updated_at: null },
];
const CHANNELS = [
  { id: 'nc_webpush', kind: 'webpush', name: 'Web Push', enabled: true, builtin: true, config: {}, secrets: {}, created_at: '', updated_at: '', last: null, devices: 1 },
  { id: 'nc_phones', kind: 'ntfy', name: 'Phones', enabled: true, builtin: false, config: { server: 'https://ntfy.lab.example', topic: 'lab-alerts' }, secrets: { token: { set: true } }, created_at: '', updated_at: '', last: null },
  { id: 'nc_hook', kind: 'webhook', name: 'Hook', enabled: true, builtin: false, config: { url: 'https://hooks.lab.example/in' }, secrets: { secret: { set: true } }, created_at: '', updated_at: '', last: null },
];

function notifyHub(over: Routes = {}) {
  return fakeFetch({
    'GET /api/notify/rules': { status: 200, body: { rules: RULES } },
    'GET /api/notify/channels': { status: 200, body: { channels: CHANNELS, public_url: true, web_push: true } },
    'GET /api/notify/deliveries': { status: 200, body: { deliveries: [] } },
    'GET /api/notify/push/subscriptions': { status: 200, body: { subscriptions: [] } },
    'PUT /api/notify/rules/*': (b, url) => ({ status: 200, body: { ...RULES.find((r) => r.id === url.pathname.split('/').pop())!, ...(b as object) } }),
    'PUT /api/notify/channels/nc_phones': (b) => ({ status: 200, body: { ...CHANNELS[1], ...(b as object) } }),
    'DELETE /api/notify/channels/nc_phones': { status: 204 },
    ...over,
  });
}

describe('Notifications', () => {
  it('a rule checkbox goes back to what the hub has when the save fails', async () => {
    notifyHub({ 'PUT /api/notify/rules/*': refuse(500, 'internal', 'The hub had a problem.') });
    const h = harness(docs(withIncidents));
    const w = h.mountIn(NotificationsPane);
    await settle();
    const box = w.findAll('.nr-rule')[0]!.find<HTMLInputElement>('input[type="checkbox"]');
    expect(box.element.checked).toBe(true);
    await box.setValue(false);
    await settle();
    expect(w.findAll('.nr-rule')[0]!.find('.err[role="alert"]').text()).toContain('The hub had a problem.');
    expect(box.element.checked).toBe(true);
    // The same for “Also when it’s resolved” and a channel.
    const resolved = w.findAll('.nr-rule')[0]!.find<HTMLInputElement>('.nr-opts input[type="checkbox"]');
    await resolved.setValue(false);
    await settle();
    expect(resolved.element.checked).toBe(true);
    const chan = w.findAll('.nr-rule')[0]!.findAll<HTMLInputElement>('.nr-chans input')[1]!;
    await chan.setValue(false);
    await settle();
    expect(chan.element.checked).toBe(true);
  });

  it('a rule checkbox stays focusable but doesn’t toggle while its rule saves', async () => {
    const slow = later<{ status: number; body?: unknown }>();
    const f = notifyHub({ 'PUT /api/notify/rules/incident.down': () => slow.promise });
    const h = harness(docs(withIncidents));
    const w = h.mountIn(NotificationsPane);
    await settle();
    const box = w.findAll('.nr-rule')[0]!.find<HTMLInputElement>('input[type="checkbox"]');
    await press(box);
    expect(box.element.checked).toBe(false);
    expect(box.attributes('aria-disabled')).toBe('true');
    expect(box.attributes('disabled')).toBeUndefined();
    expect(active()).toBe(box.element);
    await press(box);
    expect(box.element.checked).toBe(false);
    expect(f.calls.filter((c) => c.method === 'PUT')).toHaveLength(1);
    slow.resolve({ status: 200, body: { ...RULES[0], enabled: false } });
    await settle();
    expect(box.attributes('aria-disabled')).toBeUndefined();
  });

  it('the grace waits for Save, which keeps focus', async () => {
    const f = notifyHub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(NotificationsPane);
    await settle();
    const rule = w.findAll('.nr-rule')[1]!;
    const save = rule.find('button[aria-label="Save the wait for Needs attention for a while"]');
    expect(save.text()).toBe('Save');
    expect(save.attributes('aria-disabled')).toBe('true');
    await rule.find('select').setValue('10');
    await rule.find('select').setValue('15');
    await settle();
    expect(f.calls.some((c) => c.method === 'PUT')).toBe(false);
    expect(save.attributes('aria-disabled')).toBeUndefined();
    await press(save);
    expect(f.last('PUT', '/api/notify/rules/incident.attention')?.body).toEqual({ grace_minutes: 15 });
    expect(active()).toBe(save.element);
    expect(save.attributes('aria-disabled')).toBe('true');
    expect((rule.find('select').element as HTMLSelectElement).value).toBe('15');
  });

  it('Delete focuses Cancel, Cancel goes back, a delete lands on the Channels heading; the confirm passes axe', async () => {
    const f = notifyHub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(NotificationsPane);
    await settle();
    await press(w.find('button[aria-label="Delete Phones"]'));
    expect(active()?.textContent?.trim()).toBe('Cancel');
    expect(active()?.closest('.nc-confirm')).not.toBeNull();
    expect(await axeViolations()).toEqual([]);
    await press(active()!);
    expect(active()?.getAttribute('aria-label')).toBe('Delete Phones');
    await press(w.find('button[aria-label="Delete Phones"]'));
    await press(w.find('.nc-confirm .btn.danger'));
    expect(f.last('DELETE', '/api/notify/channels/nc_phones')).toBeDefined();
    expect(active()?.id).toBe('nc-h');
  });

  it('Edit focuses the form’s first field and Cancel or Save goes back to Edit; the ntfy and webhook forms pass axe', async () => {
    const f = notifyHub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(NotificationsPane);
    await settle();
    await press(w.find('button[aria-label="Edit Phones"]'));
    expect((active() as HTMLInputElement).value).toBe('Phones');
    expect(active()?.closest('form.nc-form')).not.toBeNull();
    expect(await axeViolations()).toEqual([]);
    await press(byText('Cancel')!);
    expect(w.find('form.nc-form').exists()).toBe(false);
    expect(active()?.getAttribute('aria-label')).toBe('Edit Phones');

    await press(w.find('button[aria-label="Edit Phones"]'));
    await press(w.find('form.nc-form button[type="submit"]'));
    expect(f.last('PUT', '/api/notify/channels/nc_phones')).toBeDefined();
    expect(active()?.getAttribute('aria-label')).toBe('Edit Phones');

    // The webhook's edit form, with an error showing.
    await press(w.find('button[aria-label="Edit Hook"]'));
    await w.find('form.nc-form input[type="url"]').setValue('not a url');
    await w.find('form.nc-form').trigger('submit');
    await settle();
    expect(w.find('form.nc-form .help.bad').exists()).toBe(true);
    expect(await axeViolations()).toEqual([]);
  });

  it('Add a channel focuses the kind, Cancel goes back; both kinds of new form pass axe, errors and all', async () => {
    notifyHub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(NotificationsPane);
    await settle();
    await press(byText('Add a channel')!);
    expect((active() as HTMLInputElement).value).toBe('ntfy');
    await w.find('form.nc-form').trigger('submit');
    await settle();
    expect(w.findAll('form.nc-form .help.bad').length).toBeGreaterThan(0);
    expect(await axeViolations()).toEqual([]);
    await w.find('form.nc-form input[value="webhook"]').setValue(true);
    await w.find('form.nc-form').trigger('submit');
    await settle();
    expect(w.text()).toContain('A webhook needs a signing secret');
    expect(await axeViolations()).toEqual([]);
    await press(w.find('form.nc-form').findAll('button').find((b) => b.text() === 'Cancel')!);
    expect(active()?.textContent?.trim()).toBe('Add a channel');
  });
});

// ── Users ──

const USERS = [
  { id: 'u1', username: 'owner', role: 'owner', disabled: false, has_passkey: true, last_seen_at: '2026-10-03T14:00:00Z' },
  { id: 'u2', username: 'sam', role: 'operator', disabled: false, has_passkey: false, last_seen_at: null },
];

function usersHub(over: Routes = {}) {
  return fakeFetch({
    'GET /api/users': { status: 200, body: USERS },
    'PUT /api/users/u2': (b) => ({ status: 200, body: { ...USERS[1], ...(b as object) } }),
    ...over,
  });
}

describe('Users', () => {
  it('the role waits for Save, which keeps focus through the step-up', async () => {
    let n = 0;
    const f = usersHub({ 'PUT /api/users/u2': (b) => (++n === 1 ? refuse(403, 'step_up_required', 'Confirm it’s you first.') : { status: 200, body: { ...USERS[1], ...(b as object) } }) });
    const h = harness(docs(withIncidents));
    const w = h.mountIn(UsersPane);
    await settle();
    const save = w.find('button[aria-label="Save role for sam"]');
    expect(save.attributes('aria-disabled')).toBe('true');
    await w.find('select.us-role').setValue('viewer');
    await w.find('select.us-role').setValue('admin');
    await settle();
    expect(f.calls.some((c) => c.method === 'PUT')).toBe(false);
    expect(h.ui.state.stepUp).toBeUndefined();
    await press(save);
    expect(h.ui.state.stepUp?.reason).toBe('Change sam');
    expect(active()).toBe(save.element);
    expect(save.attributes('aria-disabled')).toBe('true');
    h.ui.stepUpDone(true);
    await settle();
    expect(f.last('PUT', '/api/users/u2')?.body).toEqual({ role: 'admin' });
    expect(active()).toBe(save.element);
    expect(w.find('.us-row .m').exists()).toBe(true);
    expect(words(w.findAll('.us-row')[1]!.find('.m').element)).toContain('Admin');
  });

  it('a closed step-up puts the role back', async () => {
    usersHub({ 'PUT /api/users/u2': refuse(403, 'step_up_required', 'Confirm it’s you first.') });
    const h = harness(docs(withIncidents));
    const w = h.mountIn(UsersPane);
    await settle();
    await w.find('select.us-role').setValue('admin');
    await press(w.find('button[aria-label="Save role for sam"]'));
    h.ui.stepUpDone(false);
    await settle();
    expect((w.find('select.us-role').element as HTMLSelectElement).value).toBe('operator');
    expect(w.find('button[aria-label="Save role for sam"]').attributes('aria-disabled')).toBe('true');
  });

  it('Disable keeps focus while it works', async () => {
    usersHub({ 'PUT /api/users/u2': refuse(403, 'step_up_required', 'Confirm it’s you first.') });
    const h = harness(docs(withIncidents));
    const w = h.mountIn(UsersPane);
    await settle();
    const off = w.findAll('.us-row')[1]!.findAll('button').find((b) => b.text() === 'Disable')!;
    await press(off);
    expect(h.ui.state.stepUp).toBeDefined();
    expect(active()).toBe(off.element);
    expect(off.attributes('aria-disabled')).toBe('true');
  });

  it('Add a user: errors pass axe, Cancel and a new user go back to Add a user', async () => {
    usersHub({ 'POST /api/users': (b) => ({ status: 201, body: { id: 'u4', disabled: false, has_passkey: false, last_seen_at: null, username: (b as { username: string }).username, role: (b as { role: string }).role } }) });
    const h = harness(docs(withIncidents));
    const w = h.mountIn(UsersPane);
    await settle();
    await press(byText('Add a user')!);
    expect(active()?.id).toMatch(/-un$/);
    const form = w.find('form.us-add');
    await form.find('input[type="text"]').setValue('-bad');
    await form.find('input[type="password"]').setValue('short');
    await form.trigger('submit');
    await settle();
    expect(form.findAll('.help.bad')).toHaveLength(2);
    expect(await axeViolations()).toEqual([]);
    await press(form.findAll('button').find((b) => b.text() === 'Cancel')!);
    expect(active()?.textContent?.trim()).toBe('Add a user');

    await press(byText('Add a user')!);
    await w.find('form.us-add input[type="text"]').setValue('kim');
    await w.find('form.us-add input[type="password"]').setValue('a long enough password');
    await press(w.find('form.us-add button[type="submit"]'));
    expect(w.find('form.us-add').exists()).toBe(false);
    expect(active()?.textContent?.trim()).toBe('Add a user');
  });

  it('ownership transfer: a mismatched name passes axe, Cancel goes back to Make owner…', async () => {
    usersHub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(UsersPane);
    await settle();
    await press(byText('Make owner…')!);
    const form = w.find('form.us-transfer');
    expect(active()).toBe(form.find('input').element);
    await form.find('input').setValue('sma');
    expect(form.find('button[type="submit"]').attributes('disabled')).toBeDefined();
    await form.trigger('submit');
    await settle();
    expect(form.find('.help.bad[role="alert"]').text()).toBe('Type sam exactly to confirm.');
    expect(await axeViolations()).toEqual([]);
    await press(form.findAll('button').find((b) => b.text() === 'Cancel')!);
    expect(w.find('form.us-transfer').exists()).toBe(false);
    expect(active()?.textContent?.trim()).toBe('Make owner…');
  });
});

// ── Step-up setting ──

describe('Confirming destructive actions', () => {
  it('a radio only marks the choice; Save sends it and keeps focus through the step-up', async () => {
    let puts = 0;
    const f = fakeFetch({
      'GET /api/settings/security': { status: 200, body: { posture: 'relaxed', step_up: 'passkey_or_password', levels: [] } },
      'PUT /api/settings/security': (b) => (++puts === 1 ? refuse(403, 'step_up_required', 'Confirm it’s you first.') : { status: 200, body: { posture: 'relaxed', ...(b as object), levels: [] } }),
    });
    const h = harness(docs(withIncidents));
    const w = h.mountIn(StepUpSetting, { hasPasskey: true });
    await settle();
    const save = w.find('button[aria-label="Save how destructive actions are confirmed"]');
    expect(save.attributes('aria-disabled')).toBe('true');
    const only = w.find('input[value="passkey_only"]');
    await only.setValue(true);
    await settle();
    expect(f.calls.some((c) => c.method === 'PUT')).toBe(false);
    expect(h.ui.state.stepUp).toBeUndefined();
    expect(save.attributes('aria-disabled')).toBeUndefined();
    await press(save);
    expect(h.ui.state.stepUp).toBeDefined();
    expect(active()).toBe(save.element);
    h.ui.stepUpDone(true);
    await settle();
    expect(f.last('PUT', '/api/settings/security')?.body).toEqual({ step_up: 'passkey_only' });
    expect(active()).toBe(save.element);
    expect(save.attributes('aria-disabled')).toBe('true');
  });

  it('the Passkey-only warning without a passkey passes axe', async () => {
    fakeFetch({ 'GET /api/settings/security': { status: 200, body: { posture: 'relaxed', step_up: 'passkey_only', levels: [] } } });
    const h = harness(docs(withIncidents));
    const w = h.mountIn(StepUpSetting, { hasPasskey: false });
    await settle();
    expect(w.find('.st-note.attn').text()).toContain('You don’t have a passkey yet');
    expect(await axeViolations()).toEqual([]);
  });
});

// ── Audit ──

const EVENTS = [
  { id: 'a3', seq: 4812, at: '2026-10-03T14:01:00Z', actor: { kind: 'user', name: 'owner', via: 'web', ip: '10.0.0.12' }, action: 'pve.guest.restart', target: 'pve:jupiter-nas/lxc/100', params: {}, tier: 'T2', result: 'ok', duration_ms: 8123, task: 't_1', hash: 'sha256:cccccccccccccccc', prev_hash: 'sha256:bbbbbbbbbbbbbbbb' },
  { id: 'a2', seq: 4811, at: '2026-10-03T13:59:00Z', actor: { kind: 'user', name: 'sam', via: 'web' }, action: 'pbs.datastore.prune', target: 'pbs:main', tier: 'T2', result: 'refused', error: { code: 'forbidden', message: 'Operators can’t prune datastores.' } },
];
const OLDER = { ...EVENTS[1], id: 'a1', seq: 4810, action: 'settings.security.update', target: '', result: 'ok', error: null };

function auditHub(over: Routes = {}) {
  return fakeFetch({
    'GET /api/audit/verify': { status: 200, body: { ok: true, checked: 4812 } },
    'GET /api/audit': (_b, url) => {
      const actor = url.searchParams.get('actor');
      if (actor === 'nobody') return { status: 200, body: { events: [] } };
      if (actor === 'sam') return { status: 200, body: { events: [EVENTS[1]] } };
      if (url.searchParams.get('cursor') === 'c1') return { status: 200, body: { events: [OLDER] } };
      return { status: 200, body: { events: EVENTS, next: 'c1' } };
    },
    ...over,
  });
}

describe('Audit', () => {
  it('each Details names its event, says whether it’s open, and the open row passes axe', async () => {
    auditHub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(AuditPane);
    await settle();
    const btn = w.findAll('.au-list > li')[0]!.find('.end button');
    expect(words(btn.element)).toBe('Details: pve.guest.restart · debian-nas, done, 14:01');
    expect(btn.find('.sr').exists()).toBe(true);
    expect(btn.attributes('aria-expanded')).toBe('false');
    await btn.trigger('click');
    expect(btn.attributes('aria-expanded')).toBe('true');
    expect(document.getElementById(btn.attributes('aria-controls')!)?.classList.contains('au-detail')).toBe(true);
    expect(await axeViolations()).toEqual([]);
  });

  it('a failed Show older says so beside the button, which tries again and then moves focus to the new events', async () => {
    let n = 0;
    auditHub({
      'GET /api/audit': (_b, url) =>
        url.searchParams.get('cursor') === 'c1'
          ? ++n === 1
            ? refuse(500, 'internal', 'The hub had a problem.')
            : { status: 200, body: { events: [OLDER] } }
          : { status: 200, body: { events: EVENTS, next: 'c1' } },
    });
    const h = harness(docs(withIncidents));
    const w = h.mountIn(AuditPane);
    await settle();
    const more = byText('Show older')!;
    await press(more);
    const err = w.find('.au-older .err[role="alert"]');
    expect(err.text()).toContain('The hub had a problem.');
    expect(more.textContent?.trim()).toBe('Try again');
    expect(more.getAttribute('aria-describedby')).toBe(err.attributes('id'));
    expect(active()).toBe(more);
    expect(w.find('.st-note.bad').exists()).toBe(false); // the list stays
    expect(await axeViolations()).toEqual([]);
    await press(more);
    expect(w.findAll('.au-list > li')).toHaveLength(3);
    expect(w.find('.au-older').exists()).toBe(false);
    expect(words(active())).toContain('Details: settings.security.update');
  });

  it('says how many events match after a filter, in a polite live region; the empty filtered state passes axe', async () => {
    auditHub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(AuditPane);
    await settle();
    const region = w.find('p.sr[role="status"]');
    expect(region.text()).toBe('');
    await w.find('input[placeholder="username"]').setValue('sam');
    await w.find('form.au-filters').trigger('submit');
    await settle();
    expect(region.text()).toBe('1 event');
    await w.find('input[placeholder="username"]').setValue('nobody');
    await w.find('form.au-filters').trigger('submit');
    await settle();
    expect(region.text()).toBe('No events match');
    expect(w.find('.st-empty').text()).toContain('Nothing matches');
    expect(await axeViolations()).toEqual([]);
    await press(byText('Clear')!);
    expect(region.text()).toBe('2 events, and older ones with Show older');
  });

  it('the broken chain and the empty log pass axe', async () => {
    auditHub({
      'GET /api/audit/verify': { status: 200, body: { ok: false, checked: 4812, first_break: { seq: 1234, at: '2026-09-30T08:00:00Z' } } },
      'GET /api/audit': { status: 200, body: { events: [] } },
    });
    const h = harness(docs(withIncidents));
    const w = h.mountIn(AuditPane);
    await settle();
    expect(w.find('.st-note.bad').text()).toContain('chain breaks at event 1,234');
    expect(w.find('.st-empty').text()).toContain('Nothing recorded yet');
    expect(await axeViolations()).toEqual([]);
  });
});
