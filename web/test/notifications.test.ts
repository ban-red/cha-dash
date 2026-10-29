/**
 * Settings → Notifications (internal/notify): the rules, channels with their
 * tests, the delivery log, "Notify me on this device" (permission only from
 * a click), what each role gets, and the service worker's push and click
 * handlers. The hub is faked to the shapes in internal/notify/README.md.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import NotificationsPane from '../src/settings/NotificationsPane.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { fingerprint } from '../src/lib/push';
import { inboxLink, navigate } from '../src/lib/router';
import source from '../sw/service-worker.js?raw';
import { axeViolations } from './axe';
import { fakeFetch, type Handler } from './fake-fetch';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

async function settle(): Promise<void> {
  await vi.dynamicImportSettled();
  await flushPromises();
  await nextTick();
  await flushPromises();
}

beforeEach(() => {
  setDisplayTimeZone('UTC');
  navigate('/', { replace: true });
});
afterEach(() => {
  vi.unstubAllGlobals();
  Object.defineProperty(window, 'isSecureContext', { value: undefined, configurable: true });
  Object.defineProperty(navigator, 'serviceWorker', { value: undefined, configurable: true });
  document.body.innerHTML = '';
});

const RULES = [
  { id: 'incident.down', label: 'Something is down', description: 'A problem opens at down.', enabled: true, resolved: true, channels: null, has_resolved: true, defaults: { enabled: true, resolved: true, channels: null }, updated_at: null },
  { id: 'incident.attention', label: 'Needs attention for a while', description: 'After the grace period.', enabled: true, resolved: false, grace_minutes: 5, channels: null, has_resolved: true, defaults: { enabled: true, resolved: false, grace_minutes: 5, channels: null }, updated_at: null },
  { id: 'repair', label: 'An integration needs reconnecting', description: 'Once per problem.', enabled: true, resolved: false, channels: null, has_resolved: true, defaults: { enabled: true, resolved: false, channels: null }, updated_at: null },
  { id: 'stack.stuck', label: 'A stack is stuck', description: 'Deploy and rollback failed.', enabled: true, resolved: true, channels: null, has_resolved: true, defaults: { enabled: true, resolved: true, channels: null }, updated_at: null },
  { id: 'action.t4', label: 'Critical actions', description: 'Every T4 action.', enabled: true, resolved: false, channels: null, locked: true, has_resolved: false, defaults: { enabled: true, resolved: false, channels: null }, updated_at: null },
];
const CHANNELS = [
  { id: 'nc_webpush', kind: 'webpush', name: 'Web Push', enabled: true, builtin: true, config: {}, secrets: {}, created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-01T00:00:00Z', last: null, devices: 2 },
  {
    id: 'nc_phones', kind: 'ntfy', name: 'Phones', enabled: true, builtin: false, config: { server: 'https://ntfy.home.example', topic: 'lab-alerts' }, secrets: { token: { set: true } },
    created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-01T00:00:00Z', last: { state: 'failed', at: '2026-10-03T13:00:00Z', error: 'It answered 401: the token or secret wasn’t accepted.' },
  },
];
const DELIVERIES = [
  { id: 'nd_2', channel_id: 'nc_phones', channel_name: 'Phones', channel_kind: 'ntfy', device: null, kind: 'incident.down', title: 'PBS is down', state: 'failed', attempts: 5, status_code: 503, error: 'It answered 503.', created_at: '2026-10-03T13:00:00Z', updated_at: '2026-10-03T13:40:00Z', next_at: null },
  { id: 'nd_1', channel_id: 'nc_webpush', channel_name: 'Web Push', channel_kind: 'webpush', device: 'Firefox on Linux', kind: 'test', title: 'Test from Cha Dash', state: 'sent', attempts: 1, status_code: 201, error: null, created_at: '2026-10-03T12:00:00Z', updated_at: '2026-10-03T12:00:00Z', next_at: null },
];

function hub(over: Record<string, Handler | { status: number; body?: unknown }> = {}) {
  return fakeFetch({
    'GET /api/notify/rules': { status: 200, body: { rules: RULES } },
    'GET /api/notify/channels': { status: 200, body: { channels: CHANNELS, public_url: false, web_push: true } },
    'GET /api/notify/deliveries': (_b, url) => ({ status: 200, body: { deliveries: url.searchParams.get('state') === 'failed' ? DELIVERIES.slice(0, 1) : DELIVERIES } }),
    'GET /api/notify/push/subscriptions': { status: 200, body: { subscriptions: [] } },
    'PUT /api/notify/rules/*': (b, url) => {
      const id = url.pathname.split('/').pop()!;
      return { status: 200, body: { ...RULES.find((r) => r.id === id)!, ...(b as object) } };
    },
    'POST /api/notify/channels/nc_phones/test': { status: 200, body: { ok: false, status_code: 401, error: 'It answered 401: the token or secret wasn’t accepted.' } },
    'POST /api/notify/channels/nc_webpush/test': { status: 200, body: { ok: true, status_code: 201, error: null, devices: 1 } },
    'POST /api/notify/channels': (b) => ({ status: 201, body: { id: 'nc_new', builtin: false, enabled: true, secrets: {}, last: null, created_at: '', updated_at: '', ...(b as object) } }),
    ...over,
  });
}

describe('Settings → Notifications for an admin', () => {
  it('explains the defaults, lists rules, channels and the log, and passes axe', async () => {
    hub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(NotificationsPane);
    await settle();
    expect(w.text()).toContain('symptoms, not causes');
    const rules = w.findAll('.nr-rule');
    expect(rules.map((r) => r.find('.t').text())).toEqual(['Something is down', 'Needs attention for a while', 'An integration needs reconnecting', 'A stack is stuck', 'Critical actions']);
    // Critical actions can't be turned off.
    expect((rules[4]!.find('input').element as HTMLInputElement).disabled).toBe(true);
    expect(rules[4]!.text()).toContain('always on');
    // Channels with their state, and the note about links.
    expect(w.text()).toContain('Web Push · 2 devices · nothing sent yet');
    expect(w.text()).toContain('ntfy · ntfy.home.example · lab-alerts · last try failed');
    expect(w.text()).toContain('CHADASH_PUBLIC_URL');
    // The log, with failures.
    const log = w.findAll('.nl-list > li');
    expect(log).toHaveLength(2);
    expect(log[0]!.text()).toContain('PBS is down');
    expect(log[0]!.text()).toContain('Phones · failed after 5 tries');
    expect(log[0]!.find('[role="img"]').attributes('aria-label')).toBe('failed');
    expect(log[1]!.text()).toContain('Web Push · Firefox on Linux · sent');
    expect(await axeViolations()).toEqual([]);
  });

  it('changes a rule, its grace and its channels with PUTs the hub reads', async () => {
    const f = hub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(NotificationsPane);
    await settle();
    const rules = w.findAll('.nr-rule');
    await rules[2]!.find('input').setValue(false);
    await settle();
    expect(f.last('PUT', '/api/notify/rules/repair')!.body).toEqual({ enabled: false });
    // The grace saves on Save, not as the select changes.
    await rules[1]!.find('select').setValue('15');
    await settle();
    expect(f.last('PUT', '/api/notify/rules/incident.attention')).toBeUndefined();
    await rules[1]!.find('button[aria-label^="Save the wait"]').trigger('click');
    await settle();
    expect(f.last('PUT', '/api/notify/rules/incident.attention')!.body).toEqual({ grace_minutes: 15 });
    // Untick a channel: the rule now names the others.
    const chans = rules[0]!.findAll('.nr-chans input');
    expect(chans).toHaveLength(2);
    await chans[1]!.setValue(false);
    await settle();
    expect(f.last('PUT', '/api/notify/rules/incident.down')!.body).toEqual({ channels: ['nc_webpush'] });
    // Resolutions.
    await w.findAll('.nr-rule')[0]!.find('.nr-opts input').setValue(false);
    await settle();
    expect(f.last('PUT', '/api/notify/rules/incident.down')!.body).toEqual({ resolved: false });
  });

  it('tests a channel and says how it went, in the hub’s words', async () => {
    hub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(NotificationsPane);
    await settle();
    await w.find('button[aria-label="Send a test to Phones"]').trigger('click');
    await settle();
    expect(w.find('.err[role="status"]').text()).toBe('It answered 401: the token or secret wasn’t accepted.');
    await w.find('button[aria-label="Send a test to Web Push"]').trigger('click');
    await settle();
    expect(w.text()).toContain('Sent to your device. Check that it arrived.');
  });

  it('adds an ntfy channel after checking it, and the token leaves only in that request', async () => {
    const f = hub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(NotificationsPane);
    await settle();
    await w.findAll('button').find((b) => b.text() === 'Add a channel')!.trigger('click');
    await w.find('form.nc-form').trigger('submit');
    expect(w.text()).toContain('A name of 1 to 64 characters.');
    expect(w.text()).toContain('Letters, digits, - and _; at most 64.');
    expect(f.calls.some((c) => c.method === 'POST' && c.path === '/api/notify/channels')).toBe(false);
    const inputs = w.find('form.nc-form').findAll('input');
    await inputs.find((i) => i.attributes('maxlength') === '64' && i.attributes('type') === 'text')!.setValue('Desk');
    await w.find('form.nc-form').findAll('input[type="text"]')[1]!.setValue('desk-alerts');
    await w.find('form.nc-form input[type="password"]').setValue('tk_fakeexampletoken000000000');
    await w.find('form.nc-form').trigger('submit');
    await settle();
    expect(f.last('POST', '/api/notify/channels')!.body).toEqual({
      kind: 'ntfy',
      name: 'Desk',
      config: { server: 'https://ntfy.sh', topic: 'desk-alerts' },
      secrets: { token: 'tk_fakeexampletoken000000000' },
    });
    expect(w.find('form.nc-form').exists()).toBe(false);
    expect(h.ui.state.toasts.at(-1)?.text).toBe('Added Desk. Send a test to check it.');
  });

  it('a webhook needs a signing secret, and Generate makes one to copy', async () => {
    const f = hub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(NotificationsPane);
    await settle();
    await w.findAll('button').find((b) => b.text() === 'Add a channel')!.trigger('click');
    await w.find('input[value="webhook"]').setValue(true);
    const form = w.find('form.nc-form');
    await form.find('input[maxlength="64"]').setValue('Home Assistant');
    await form.find('input[type="url"]').setValue('https://ha.home.example/api/webhook/chadash');
    await form.trigger('submit');
    expect(form.text()).toContain('A webhook needs a signing secret');
    await form.findAll('button').find((b) => b.text() === 'Generate')!.trigger('click');
    const secret = form.find('.secret-row input');
    expect(secret.attributes('type')).toBe('text');
    expect((secret.element as HTMLInputElement).value).toMatch(/^whsec_[A-Za-z0-9_-]{43}$/);
    await form.trigger('submit');
    await settle();
    const body = f.last('POST', '/api/notify/channels')!.body as { kind: string; config: unknown; secrets: { secret: string } };
    expect(body.kind).toBe('webhook');
    expect(body.config).toEqual({ url: 'https://ha.home.example/api/webhook/chadash' });
    expect(body.secrets.secret).toMatch(/^whsec_/);
  });

  it('filters the log to failures', async () => {
    const f = hub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(NotificationsPane);
    await settle();
    await w.find('.nl-seg').findAll('button')[1]!.trigger('click');
    await settle();
    expect(f.calls.at(-1)!.path).toBe('/api/notify/deliveries?limit=50&state=failed');
    expect(w.findAll('.nl-list > li')).toHaveLength(1);
  });
});

describe('Settings → Notifications below admin', () => {
  it('shows only this device and never asks for the admin routes', async () => {
    const f = hub();
    const h = harness(docs(withIncidents));
    h.session.state.user = { id: 'u2', username: 'kid', role: 'household' };
    const w = h.mountIn(NotificationsPane);
    await settle();
    expect(w.text()).toContain('This device');
    expect(w.text()).toContain('on your boards');
    expect(w.text()).not.toContain('Channels');
    expect(f.calls.every((c) => c.path.startsWith('/api/notify/push/'))).toBe(true);
    // Not a secure context here: a sentence, not a button that can't work.
    expect(w.text()).toContain('Browsers only allow notifications over HTTPS');
    expect(w.find('.nd-switch').exists()).toBe(false);
    expect(await axeViolations()).toEqual([]);
  });
});

describe('Notify me on this device', () => {
  function browser() {
    let sub: { endpoint: string; options: { applicationServerKey: ArrayBuffer | null }; toJSON(): unknown; unsubscribe: () => Promise<boolean> } | null = null;
    const requestPermission = vi.fn(async () => 'granted' as NotificationPermission);
    const subscribe = vi.fn(async () => {
      sub = {
        endpoint: 'https://push.example/wpush/abc',
        options: { applicationServerKey: null },
        toJSON: () => ({ endpoint: 'https://push.example/wpush/abc', expirationTime: null, keys: { p256dh: 'BPfake', auth: 'fakeauth' } }),
        unsubscribe: vi.fn(async () => {
          sub = null;
          return true;
        }),
      };
      return sub;
    });
    const reg = { pushManager: { subscribe, getSubscription: async () => sub } };
    Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true });
    Object.defineProperty(navigator, 'serviceWorker', { value: { ready: Promise.resolve(reg), getRegistration: async () => reg }, configurable: true });
    vi.stubGlobal('PushManager', class {});
    vi.stubGlobal('Notification', { permission: 'default', requestPermission });
    return { requestPermission, subscribe };
  }

  it('asks for permission only when clicked, then subscribes with the hub’s key and registers the device', async () => {
    const b = browser();
    let devices: unknown[] = [];
    const selfPrint = await fingerprint('https://push.example/wpush/abc');
    const f = hub({
      'GET /api/notify/push/key': { status: 200, body: { public_key: 'BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4' } },
      'GET /api/notify/push/subscriptions': () => ({ status: 200, body: { subscriptions: devices } }),
      'POST /api/notify/push/subscriptions': (body) => {
        const fp = selfPrint;
        devices = [{ id: 'ps_1', label: 'Firefox on Linux', host: 'push.example', fingerprint: fp, created_at: '2026-10-03T14:00:00Z', last_ok_at: null, last_error: null }];
        return { status: 201, body: { ...(devices[0] as object), echoed: body } };
      },
      'POST /api/notify/push/unsubscribe': { status: 204 },
    });
    const h = harness(docs(withIncidents));
    h.session.state.user = { id: 'u3', username: 'sam', role: 'viewer' };
    const w = h.mountIn(NotificationsPane);
    await settle();
    expect(b.requestPermission).not.toHaveBeenCalled();
    const sw = w.find('.nd-switch');
    expect(sw.attributes('aria-pressed')).toBe('false');
    expect(sw.text()).toBe('Notify me on this device');
    await sw.trigger('click');
    await settle();
    expect(b.requestPermission).toHaveBeenCalledTimes(1);
    expect(b.subscribe).toHaveBeenCalledTimes(1);
    const opts = (b.subscribe.mock.calls[0] as unknown as [{ userVisibleOnly: boolean; applicationServerKey: Uint8Array }])[0];
    expect(opts.userVisibleOnly).toBe(true);
    expect(opts.applicationServerKey).toHaveLength(65);
    expect(f.last('POST', '/api/notify/push/subscriptions')!.body).toEqual({ endpoint: 'https://push.example/wpush/abc', expirationTime: null, keys: { p256dh: 'BPfake', auth: 'fakeauth' } });
    expect(h.ui.state.toasts.at(-1)?.text).toBe('This device will get notifications.');
    // "this device" waits on the endpoint's SHA-256 (crypto.subtle), which
    // settles on its own schedule under load.
    await vi.waitFor(() => expect(w.text()).toContain('Firefox on Linuxthis device'));
    expect(w.find('.nd-switch').attributes('aria-pressed')).toBe('true');
    // And off again: the hub forgets it, then the browser does.
    await w.find('.nd-switch').trigger('click');
    await settle();
    expect(f.last('POST', '/api/notify/push/unsubscribe')!.body).toEqual({ endpoint: 'https://push.example/wpush/abc' });
  });

  it('says so when the browser blocks notifications', async () => {
    const b = browser();
    b.requestPermission.mockResolvedValueOnce('denied');
    hub({ 'GET /api/notify/push/key': { status: 200, body: { public_key: 'AA' } } });
    const h = harness(docs(withIncidents));
    h.session.state.user = { id: 'u3', username: 'sam', role: 'viewer' };
    const w = h.mountIn(NotificationsPane);
    await settle();
    await w.find('.nd-switch').trigger('click');
    await settle();
    expect(w.find('.err').text()).toContain('Notifications are blocked for Cha Dash in this browser.');
    expect(b.subscribe).not.toHaveBeenCalled();
  });
});

describe('the service worker’s notifications', () => {
  function worker() {
    const listeners: Record<string, (e: unknown) => void> = {};
    const shown: { title: string; options: Record<string, unknown> }[] = [];
    const opened: string[] = [];
    const navigated: string[] = [];
    let tabs: { url: string; focus: () => Promise<void>; navigate: (u: string) => Promise<void> }[] = [];
    const self = {
      __CHADASH_PRECACHE__: [],
      location: { origin: 'https://hub.test' },
      addEventListener: (type: string, fn: (e: unknown) => void) => (listeners[type] = fn),
      registration: { showNotification: async (title: string, options: Record<string, unknown>) => void shown.push({ title, options }) },
      clients: { matchAll: async () => tabs, openWindow: async (u: string) => void opened.push(u), claim: async () => undefined },
      skipWaiting: async () => undefined,
    };
    new Function('self', 'caches', 'fetch', 'Request', source)(self, {}, async () => new Response(), class {});
    const fire = async (type: string, event: Record<string, unknown>) => {
      let wait: Promise<unknown> = Promise.resolve();
      listeners[type]!({ ...event, waitUntil: (p: Promise<unknown>) => (wait = p) });
      await wait;
    };
    return { fire, shown, opened, navigated, setTabs: (t: typeof tabs) => (tabs = t) };
  }

  it('shows a push with its title, body and tag, and keeps only in-app paths', async () => {
    const w = worker();
    await w.fire('push', { data: { json: () => ({ v: 1, kind: 'incident.down', severity: 'down', title: 'PBS is down', body: 'Down since 02:14 UTC.', path: '/?incident=pbs', tag: 'pbs', at: '2026-10-06T02:14:10Z' }) } });
    expect(w.shown[0]!.title).toBe('PBS is down');
    expect(w.shown[0]!.options).toMatchObject({ body: 'Down since 02:14 UTC.', tag: 'pbs', renotify: true, data: { path: '/?incident=pbs' }, icon: '/icons/icon-192.png' });
    await w.fire('push', { data: { json: () => ({ title: 'x', path: 'https://evil.example/' }) } });
    expect(w.shown[1]!.options['data']).toEqual({ path: '/' });
    await w.fire('push', { data: { json: () => ({ title: 'y', path: '//evil.example/' }) } });
    expect(w.shown[2]!.options['data']).toEqual({ path: '/' });
    await w.fire('push', { data: { json: () => { throw new Error('not json'); } } });
    expect(w.shown[3]!.title).toBe('Cha Dash');
  });

  it('a click focuses an open tab and takes it to the incident, or opens one', async () => {
    const w = worker();
    const close = vi.fn();
    await w.fire('notificationclick', { notification: { close, data: { path: '/?incident=pbs' } } });
    expect(close).toHaveBeenCalled();
    expect(w.opened).toEqual(['https://hub.test/?incident=pbs']);
    const focus = vi.fn(async () => undefined);
    const nav = vi.fn(async () => undefined);
    w.setTabs([
      { url: 'https://elsewhere.example/', focus: vi.fn(async () => undefined), navigate: vi.fn(async () => undefined) },
      { url: 'https://hub.test/boards/b1', focus, navigate: nav },
    ]);
    await w.fire('notificationclick', { notification: { close, data: { path: '/settings/audit' } } });
    expect(focus).toHaveBeenCalled();
    expect(nav).toHaveBeenCalledWith('https://hub.test/settings/audit');
    expect(w.opened).toHaveLength(1);
  });
});

describe('a notification’s link', () => {
  it('opens the Inbox on the incident from the front page only', () => {
    expect(inboxLink({ path: '/', query: { incident: 'pbs.datastore' } })).toEqual({ incident: 'pbs.datastore' });
    expect(inboxLink({ path: '/', query: { inbox: '1' } })).toEqual({ incident: '' });
    expect(inboxLink({ path: '/', query: {} })).toBeNull();
    expect(inboxLink({ path: '/settings/audit', query: { incident: 'x' } })).toBeNull();
  });
});
