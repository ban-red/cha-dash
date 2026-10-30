/**
 * WCAG 2.2 A/AA (axe-core) over the main views: the doors (loading, setup,
 * passkey offer, sign-in, unreachable), the front page (busy, quiet, offline,
 * edit mode), the Inbox, ⌘K, the confirm dialog, toasts, and every Settings
 * pane. Contrast is checked on the tokens (see test/axe.ts).
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils';
import { defineComponent, h, nextTick, type Component } from 'vue';
import { toDocument } from '@chadash/layout';
import App from '../src/App.vue';
import BoardSurface from '../src/board/BoardSurface.vue';
import Briefing from '../src/components/Briefing.vue';
import CommandPalette from '../src/components/CommandPalette.vue';
import ConfirmDialog from '../src/components/ConfirmDialog.vue';
import HubBanner from '../src/components/HubBanner.vue';
import InboxSheet from '../src/components/InboxSheet.vue';
import NeedsAttention from '../src/components/NeedsAttention.vue';
import StepUpDialog from '../src/components/StepUpDialog.vue';
import TaskPresence from '../src/components/TaskPresence.vue';
import Toasts from '../src/components/Toasts.vue';
import TypeConfirmDialog from '../src/components/TypeConfirmDialog.vue';
import SettingsSheet from '../src/settings/SettingsSheet.vue';
import ImportFlow from '../src/import/ImportFlow.vue';
import HistoryList from '../src/import/HistoryList.vue';
import DiscoverPane from '../src/discover/DiscoverPane.vue';
import LoginView from '../src/views/LoginView.vue';
import PasskeyOffer from '../src/views/PasskeyOffer.vue';
import SetupView from '../src/views/SetupView.vue';
import { INLINE_SNAPSHOT_ID } from '../src/lib/inline';
import { setDisplayTimeZone } from '../src/lib/format';
import { navigate, syncRoute } from '../src/lib/router';
import { defaultBoard } from '../src/model/defaultLayout';
import { provideBoard, createBoardStore } from '../src/stores/board';
import { resetIntegrations } from '../src/stores/integrations';
import goldenRaw from '../../testdata/importers/homepage/dogfood/expected-plan.json?raw';
import { axeViolations } from './axe';
import { engine, planFor, taskFor } from './fake-engine';
import { fakeFetch } from './fake-fetch';
import { FakeSocket } from './fake-socket';
import { docs, healthy, withIncidents } from './fixtures';
import { harness } from './harness';

const BS = String.fromCharCode(92);
const USER = { id: 'u1', username: 'owner', role: 'owner' };
const golden = JSON.parse(goldenRaw) as { plan: Record<string, unknown> };

const PLEX_SCHEMA = {
  type: 'object',
  required: ['url', 'token'],
  properties: {
    url: { type: 'string', format: 'uri', title: 'URL' },
    token: { type: 'string', writeOnly: true, title: 'Token', minLength: 8, 'x-chadash-authorize': 'plex' },
    tls: {
      type: 'object',
      title: 'TLS',
      properties: { mode: { enum: ['verify', 'pin', 'tofu', 'insecure'], default: 'verify' }, pin: { type: 'string' } },
    },
    poll_s: { type: 'integer', minimum: 10, maximum: 3600, title: 'Poll every (s)' },
    verify: { type: 'boolean', title: 'Verify' },
    targets: { type: 'array', title: 'Targets', items: { type: 'object', properties: { name: { type: 'string' }, kind: { enum: ['http', 'tcp'] } } } },
  },
};

/** The hub's side of every view here. */
/** What GET /api/settings/history answers on a hub a few days in. */
const HISTORY_VIEW = {
  level: 'standard',
  locked: false,
  demo: false,
  series: 812,
  resources: 240,
  file_bytes: 21_400_000,
  max_bytes: 1e9,
  trimmed_at: null,
  instances: [{ id: 'i1', series: 412, bytes: 9_100_000 }],
  excluded: [],
  exclude_keys: [],
  tiers: [
    { tier: '1m', default_keep_ms: 172_800_000, custom: false, choices_ms: [21_600_000, 172_800_000], step_ms: 60_000, keep_ms: 172_800_000, chunks: 38_976, bytes: 9_800_000, oldest: Date.parse('2026-10-01T14:00:00Z') },
    { tier: '15m', default_keep_ms: 1_209_600_000, custom: false, choices_ms: [1_209_600_000], step_ms: 900_000, keep_ms: 1_209_600_000, chunks: 9_744, bytes: 5_100_000, oldest: Date.parse('2026-09-28T12:00:00Z') },
    { tier: '1h', default_keep_ms: 31_536_000_000, custom: false, choices_ms: [31_536_000_000], step_ms: 3_600_000, keep_ms: 31_536_000_000, chunks: 0, bytes: 0, oldest: null },
    { tier: '1d', default_keep_ms: 0, custom: false, choices_ms: [0], step_ms: 86_400_000, keep_ms: 0, chunks: 0, bytes: 0, oldest: null },
  ],
  estimates: { standard: 28_000_000, 'long-term': 101_000_000 },
};

function hubApi(over: Record<string, unknown> = {}) {
  return fakeFetch({
    'GET /api/demo': { status: 200, body: { enabled: true, now: withIncidents.now, seed: 1 } },
    'GET /api/integrations/catalog': {
      status: 200,
      body: [
        { id: 'plex', name: 'Plex', version: '1.0.0', tier: 0, capabilities: ['media'], config_schema: PLEX_SCHEMA },
        { id: 'checks', name: 'Checks', version: '1.0.0', tier: 0, config_schema: { type: 'object', properties: {} } },
      ],
    },
    'GET /api/integrations/instances': {
      status: 200,
      body: [
        { id: 'i1', integration: 'plex', name: 'Plex', status: 'repair', version: '1.41.2', last_error: 'Plex rejected its token since 13:27.', warnings: [] },
        { id: 'i2', integration: 'checks', name: 'Checks', status: 'ok', warnings: [{ code: 'http_creds', severity: 'attention', message: 'Sends credentials over http://' }] },
      ],
    },
    'GET /api/integrations/instances/i1': {
      status: 200,
      body: { id: 'i1', integration: 'plex', name: 'Plex', status: 'repair', last_error: 'Plex rejected its token since 13:27.', config: { url: 'https://plex.example.com:32400', tls: { mode: 'verify' } }, secrets: { token: { set: true } } },
    },
    'POST /api/integrations/instances/i1/test': {
      status: 200,
      body: { ok: false, latency_ms: 84, grants: [{ name: 'server.settings', access: 'write', detail: 'change server settings' }], warnings: [{ code: 'w', severity: 'attention', message: 'This token can change settings.' }], error: { code: 'unauthorized', message: 'Plex rejected the token (401).' } },
    },
    'GET /api/auth/passkeys': {
      status: 200,
      body: [
        { id: 'pk1', name: 'iCloud Keychain', created_at: '2026-10-01T10:00:00Z', last_used_at: null, rp_id: 'localhost', backed_up: true, clone_warning: false },
        { id: 'pk2', name: 'YubiKey', created_at: '2026-10-02T10:00:00Z', last_used_at: '2026-10-03T09:00:00Z', rp_id: 'dash.example.com', backed_up: false, clone_warning: true },
      ],
    },
    'GET /api/users': {
      status: 200,
      body: [
        { id: 'u1', username: 'owner', role: 'owner', disabled: false, has_passkey: true, last_seen_at: '2026-10-03T14:00:00Z' },
        { id: 'u2', username: 'sam', role: 'operator', disabled: false, has_passkey: false, last_seen_at: null },
        { id: 'u3', username: 'kid', role: 'household', disabled: true, has_passkey: false, last_seen_at: '2026-09-20T08:00:00Z' },
      ],
    },
    'GET /api/audit/verify': { status: 200, body: { ok: true, checked: 4812 } },
    'GET /api/audit*': {
      status: 200,
      body: {
        events: [
          { id: 'a2', seq: 4812, at: '2026-10-03T14:01:00Z', actor: { kind: 'user', name: 'owner', via: 'web' }, action: 'pve.guest.restart', target: 'pve:stork/lxc/220', tier: 'T2', result: 'ok', duration_ms: 8123, hash: 'sha256:bb', prev_hash: 'sha256:aa' },
          { id: 'a1', seq: 4811, at: '2026-10-03T13:59:00Z', actor: { kind: 'user', name: 'sam', via: 'web' }, action: 'pbs.datastore.prune', target: 'pbs:main', tier: 'T2', result: 'refused', error: { code: 'forbidden', message: 'Operators can’t prune datastores.' }, hash: 'sha256:aa', prev_hash: 'sha256:99' },
        ],
        next: 'c1',
      },
    },
    'GET /api/settings/security': { status: 200, body: { posture: 'relaxed', step_up: 'passkey_or_password', levels: [{ id: 'relaxed', label: 'Relaxed · LAN-only home network', description: 'Every finding a hint.', default: true }] } },
    'GET /api/settings/history': { status: 200, body: HISTORY_VIEW },
    ...engine().routes,
    'POST /api/import/homepage?dry_run=1': { status: 200, body: golden },
    'GET /api/import/homepage': { status: 200, body: { record: { board_id: 'b1', tiles: golden.plan['tiles'] } } },
    ...over,
  });
}

async function settle(): Promise<void> {
  await vi.dynamicImportSettled();
  await flushPromises();
  await nextTick();
}

async function until(cond: () => boolean, what: string): Promise<void> {
  for (let i = 0; i < 1000 && !cond(); i++) {
    await settle();
    await new Promise((r) => setTimeout(r, 5));
  }
  if (!cond()) throw new Error(`timed out waiting for ${what}`);
}

function inline(s: typeof withIncidents): void {
  const d = docs(s);
  const snap = {
    v: 1,
    user: USER,
    boards: [{ id: 'b1', name: 'Home' }],
    board: { id: 'b1', name: 'Home', revision: 1, format_version: 1, layout: toDocument(defaultBoard(d.graph, Date.parse(s.now))) },
    demo: true,
    ...d,
  };
  const json = JSON.stringify(snap).replace(/[<>&]/g, (c) => `${BS}u${c.charCodeAt(0).toString(16).padStart(4, '0')}`);
  document.body.insertAdjacentHTML('beforeend', `<script type="application/json" id="${INLINE_SNAPSHOT_ID}">${json}</script>`);
}

/**
 * Mount a component with the app's stores and a board; null data means the
 * hub never answered. Front-page pieces go inside <main>, as in the app;
 * layers (the Inbox, ⌘K, Settings, dialogs) sit beside it.
 */
function page(component: Component, data: Record<string, unknown> | null = docs(withIncidents), props: Record<string, unknown> = {}, opts: { main?: boolean } = {}) {
  const hs = harness(data ?? undefined, { demo: true });
  const board = createBoardStore({ waitForGraph: async () => hs.live.graph.value });
  const Page = defineComponent({
    setup() {
      provideBoard(board);
      return () => (opts.main ? h('main', { class: 'page', 'aria-label': 'Front page' }, [h(component, props)]) : h(component, props));
    },
  });
  return { ...hs, w: hs.mountIn(Page) };
}

// Nothing outlives its test: a view left mounted would react to the next one's route.
enableAutoUnmount(afterEach);

beforeEach(() => {
  setDisplayTimeZone('UTC');
  FakeSocket.reset();
  vi.stubGlobal('WebSocket', FakeSocket);
  resetIntegrations();
  navigate('/', { replace: true });
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('the doors', () => {
  it('loading, then the unreachable door', async () => {
    let fail: (e: Error) => void = () => undefined;
    vi.stubGlobal('fetch', vi.fn(() => new Promise((_, reject) => (fail = reject))));
    const w = mount(App, { attachTo: document.body });
    expect(document.querySelector('.door[aria-busy="true"]')).not.toBeNull();
    expect(await axeViolations()).toEqual([]);
    fail(new TypeError('Failed to fetch'));
    await until(() => document.querySelector('.door-card') !== null, 'the unreachable door');
    expect(await axeViolations()).toEqual([]);
    w.unmount();
  });

  it('first-run setup, with its errors showing', async () => {
    vi.stubGlobal('fetch', vi.fn());
    const { mountIn } = harness();
    const w = mountIn(SetupView);
    expect(await axeViolations()).toEqual([]);
    await w.find('#setup-pass').setValue('short');
    await w.find('#setup-confirm').setValue('shorter');
    await w.find('form').trigger('submit');
    expect(await axeViolations()).toEqual([]);
  });

  it('the passkey offer', async () => {
    const { mountIn } = harness();
    mountIn(PasskeyOffer);
    await settle();
    expect(await axeViolations()).toEqual([]);
  });

  it('sign-in, after a wrong password', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{}', { status: 401 })));
    const { mountIn } = harness();
    const w = mountIn(LoginView);
    expect(await axeViolations()).toEqual([]);
    await w.find('#login-user').setValue('owner');
    await w.find('#login-pass').setValue('wrong password here');
    await w.find('form').trigger('submit');
    await settle();
    expect(await axeViolations()).toEqual([]);
  });
});

describe('the front page', () => {
  for (const [name, fixture] of [['with today’s incidents', withIncidents], ['all quiet', healthy]] as const) {
    it(name, async () => {
      hubApi();
      inline(fixture);
      const w = mount(App, { attachTo: document.body });
      await until(() => document.querySelector('.tile') !== null, 'the board');
      expect(await axeViolations()).toEqual([]);
      w.unmount();
    });
  }

  it('with the settings menu and its themes open', async () => {
    const hs = page(Briefing, docs(withIncidents), {}, { main: true });
    (document.querySelector('.settings-menu > button') as HTMLButtonElement).click();
    await settle();
    expect(document.querySelector('[role="menu"]')).not.toBeNull();
    expect(await axeViolations()).toEqual([]);
    (document.querySelector('[role="menuitem"][data-item="theme"]') as HTMLButtonElement).click();
    await settle();
    expect(document.querySelector('[role="radiogroup"]')).not.toBeNull();
    expect(await axeViolations()).toEqual([]);
    hs.live.stop();
  });

  it('offline, on the last-known page', async () => {
    const hs = page(defineComponent({ setup: () => () => [h(Briefing), h(HubBanner), h(NeedsAttention), h(BoardSurface, { board: defaultBoard(docs(withIncidents).graph, Date.parse(withIncidents.now)), breakpoint: 'desktop' })] }), null, {}, { main: true });
    hs.ws.drop();
    hs.live.restore({ savedAt: Date.parse(withIncidents.now), ...docs(withIncidents) });
    hs.live.tick();
    await settle();
    expect(document.querySelector('.banner')).not.toBeNull();
    expect(await axeViolations()).toEqual([]);
    hs.live.stop();
  });

  it('in edit mode', async () => {
    const hs = page(BoardSurface, docs(withIncidents), { board: defaultBoard(docs(withIncidents).graph, Date.parse(withIncidents.now)), breakpoint: 'desktop' }, { main: true });
    hs.ui.state.editing = true;
    await settle();
    await until(() => document.querySelector('.edit-hint') !== null, 'edit mode');
    expect(await axeViolations()).toEqual([]);
    hs.live.stop();
  });
});

describe('layers', () => {
  it('the Inbox, on a problem and on a repair', async () => {
    const hs = page(InboxSheet);
    hs.ui.openInbox('pbs-datastore-filling');
    await settle();
    expect(document.querySelector('.inbox')).not.toBeNull();
    expect(await axeViolations()).toEqual([]);
    hs.ui.openInbox('plex-token-revoked');
    await settle();
    expect(await axeViolations()).toEqual([]);
  });

  it('⌘K, empty and with results', async () => {
    hubApi();
    const hs = page(CommandPalette);
    hs.ui.openPalette();
    await settle();
    expect(document.querySelector('[role="combobox"]')).not.toBeNull();
    expect(await axeViolations()).toEqual([]);
    const input = document.querySelector<HTMLInputElement>('[role="combobox"]')!;
    input.value = 'jup';
    input.dispatchEvent(new Event('input'));
    await settle();
    expect(document.querySelectorAll('[role="option"]').length).toBeGreaterThan(0);
    expect(await axeViolations()).toEqual([]);
  });

  it('the hold-to-confirm dialog', async () => {
    hubApi();
    const hs = page(ConfirmDialog);
    hs.ui.act(hs.live.inbox.value!.incidents['host-memory-pressure']!);
    await settle();
    expect(document.querySelector('.confirm')).not.toBeNull();
    expect(await axeViolations()).toEqual([]);
  });

  it('the typed confirmation, with a mismatch said at the field', async () => {
    const hs = page(TypeConfirmDialog);
    const plan = planFor('pve.snapshot.delete', 'pve:stork/qemu/221', { tier: 'T3', confirm_text: 'sandbox', label: 'Delete snapshot pre-upgrade', impact: [{ resource: 'pve:stork/qemu/221', via: 'snapshot_of', depth: 1 }] });
    hs.ui.state.confirm = { kind: 'type', req: { action: plan.action, target: plan.target, label: plan.label }, plan, action: { id: plan.action, label: plan.label, tier: 'T3', target: plan.target }, error: 'That doesn’t match. Type it exactly as shown.' };
    await settle();
    expect(document.querySelector('dialog.act-dlg')).not.toBeNull();
    expect(await axeViolations()).toEqual([]);
  });

  for (const [name, begin] of [
    ['a passkey', { status: 200, body: { publicKey: { challenge: 'AAAA', rpId: 'localhost' } } }],
    ['the password fallback', { status: 200, body: { method: 'password' } }],
    ['passkey only, without a passkey', { status: 409, body: { error: { code: 'passkey_required', message: 'This hub asks for a passkey. Add one in Settings → Security.' } } }],
  ] as const) {
    it(`step-up: ${name}`, async () => {
      fakeFetch({ 'POST /api/auth/step-up/begin': begin });
      const hs = page(StepUpDialog);
      hs.ui.state.stepUp = { reason: 'Delete snapshot pre-upgrade', fresh: false, resolve: () => undefined };
      await settle();
      await until(() => !document.querySelector('dialog.stepup [aria-busy="true"]'), 'the step-up method');
      expect(await axeViolations()).toEqual([]);
    });
  }

  it('the running-task presence, with Cancel for a cancellable task', async () => {
    const hs = page(TaskPresence, { ...docs(withIncidents), tasks: { tasks: { t1: taskFor(planFor('pve.guest.restart', 'pve:stork/lxc/220'), { id: 't1', progress: 0.5, cancellable: true }) } } });
    await settle();
    expect(document.querySelector('.tp')?.textContent).toContain('Restart files');
    expect(document.querySelector('.tp-cancel')?.getAttribute('aria-label')).toBe('Cancel Restart files');
    expect(hs.ui.running.value).toHaveLength(1);
    expect(await axeViolations()).toEqual([]);
  });

  it('toasts', async () => {
    const hs = page(Toasts);
    hs.ui.toast('Restarted qBittorrent.');
    hs.ui.toast('The hub couldn’t do that.', 'error');
    await settle();
    expect(await axeViolations()).toEqual([]);
  });
});

describe('Settings', () => {
  for (const path of [
    '/settings/integrations',
    '/settings/integrations/new',
    '/settings/integrations/new/plex',
    '/settings/integrations/i1?reason=auth',
    '/settings/import',
    '/settings/security',
    '/settings/backup',
    '/settings/history',
    '/settings/users',
    '/settings/audit',
  ]) {
    it(path, async () => {
      hubApi();
      Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true });
      history.replaceState(null, '', path);
      syncRoute();
      page(SettingsSheet);
      await settle();
      await until(() => document.querySelector('.st-skel') === null, 'the pane to load');
      expect(await axeViolations()).toEqual([]);
    });
  }

  it('an integration form with errors and a failed test', async () => {
    hubApi();
    history.replaceState(null, '', '/settings/integrations/i1');
    syncRoute();
    page(SettingsSheet);
    await until(() => document.querySelector('form.st-form') !== null, 'the form');
    const url = document.querySelector<HTMLInputElement>('form.st-form input[type="url"], form.st-form input')!;
    url.value = 'not a url';
    url.dispatchEvent(new Event('input'));
    const buttons = [...document.querySelectorAll<HTMLButtonElement>('form.st-form button')];
    buttons.find((b) => /test/i.test(b.textContent ?? ''))?.click();
    await settle();
    document.querySelector<HTMLFormElement>('form.st-form')!.dispatchEvent(new Event('submit'));
    await settle();
    expect(await axeViolations()).toEqual([]);
  });

  it('the import preview', async () => {
    hubApi();
    const hs = page(ImportFlow);
    const services = document.querySelector<HTMLTextAreaElement>('#imp-services_yaml')!;
    services.value = '- Media:\n    - Plex:\n        href: https://plex.example.com\n';
    services.dispatchEvent(new Event('input'));
    await settle();
    [...document.querySelectorAll('button')].find((b) => b.textContent === 'Preview import')!.click();
    await settle();
    expect(document.querySelector('.imp-review')).not.toBeNull();
    // a connection unticked: the group is partly in, and the board follows
    document.querySelector<HTMLInputElement>('.imp-row input[type="checkbox"]')!.click();
    await settle();
    expect(await axeViolations()).toEqual([]);
    hs.live.stop();
  });

  it('the Discover tab', async () => {
    hubApi({ 'GET /api/discover/dismissed': { status: 200, body: { dismissed: [] } }, 'POST /api/onboarding/discover': { status: 200, body: { found: [], checked: [] } } });
    const hs = page(DiscoverPane);
    await settle();
    expect(document.querySelector('#leads-h')).not.toBeNull();
    expect(await axeViolations()).toEqual([]);
    hs.live.stop();
  });

  it('the import history, with a revert planned', async () => {
    hubApi({
      'GET /api/imports': {
        status: 200,
        body: {
          imports: [
            {
              id: 'imp_1', source: 'homepage', created_at: Date.parse('2026-10-29T09:00:00Z'), files: ['services.yaml'], target: 'new', board_id: 'b1',
              created: { board: 'b1', instances: ['i1'], secrets: [], items: ['plex'] }, counts: { created: 4, updated: 0, tiles: 1, instances: 1, secrets: 0 },
            },
          ],
        },
      },
      'POST /api/imports/imp_1/revert?dry_run=1': { status: 200, body: { remove: [{ kind: 'board', id: 'b1', name: 'Home' }], keep: [{ kind: 'instance', id: 'i1', name: 'Plex', why: 'changed since the import' }], connections: false } },
    });
    const hs = page(HistoryList);
    await settle();
    [...document.querySelectorAll('button')].find((b) => b.textContent === 'Revert…')!.click();
    await settle();
    expect(document.querySelector('.hist-plan')?.textContent).toContain('Keeps the connection Plex: changed since the import');
    expect(await axeViolations()).toEqual([]);
    hs.live.stop();
  });
});
