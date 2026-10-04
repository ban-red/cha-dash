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
import Toasts from '../src/components/Toasts.vue';
import SettingsSheet from '../src/settings/SettingsSheet.vue';
import ImportPane from '../src/settings/ImportPane.vue';
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
    const edit = [...document.querySelectorAll('button')].find((b) => /edit layout/i.test(b.textContent ?? ''));
    expect(edit).toBeDefined();
    edit!.click();
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
    const hs = page(ConfirmDialog);
    hs.ui.act(hs.live.inbox.value!.incidents['host-memory-pressure']!);
    await settle();
    expect(document.querySelector('.confirm')).not.toBeNull();
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
    const hs = page(ImportPane);
    const services = document.querySelector<HTMLTextAreaElement>('#yaml-services_yaml')!;
    services.value = '- Media:\n    - Plex:\n        href: https://plex.example.com\n';
    services.dispatchEvent(new Event('input'));
    await settle();
    [...document.querySelectorAll('button')].find((b) => b.textContent === 'Preview import')!.click();
    await settle();
    expect(document.querySelector('.import-preview')).not.toBeNull();
    expect(await axeViolations()).toEqual([]);
    hs.live.stop();
  });
});
