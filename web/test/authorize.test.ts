import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import IntegrationEditor from '../src/settings/IntegrationEditor.vue';
import { syncRoute } from '../src/lib/router';
import type { JsonSchema } from '../src/lib/schema';
import { resetIntegrations } from '../src/stores/integrations';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

// Plex's schema as the hub serves it: the token can come from its sign-in.
const PLEX: JsonSchema = {
  type: 'object',
  required: ['url', 'token'],
  properties: {
    url: { type: 'string', format: 'uri', title: 'URL' },
    tls: { type: 'object', title: 'TLS', properties: { mode: { enum: ['verify', 'pin', 'tofu', 'insecure'], default: 'verify' } } },
    token: { type: 'string', title: 'Token', writeOnly: true, minLength: 1, 'x-chadash-authorize': true },
  },
};
const SONARR: JsonSchema = {
  type: 'object',
  required: ['url', 'api_key'],
  properties: { url: { type: 'string', format: 'uri', title: 'URL' }, api_key: { type: 'string', title: 'API key', writeOnly: true } },
};

const TOKEN = 'token-from-plex-sign-in-0001';
const LINK = 'https://app.plex.tv/auth#?clientID=chadash-0123&code=pin0001&context%5Bdevice%5D%5Bproduct%5D=Cha%20Dash';

type Answer = { status: number; body?: unknown };
const STARTED: Answer = { status: 200, body: { id: 'au_1', code: 'pin0001', url: LINK, expires_at: '2026-10-03T14:35:00Z', interval_s: 2 } };

/** A hub whose sign-in starts with `begin`, answers pending `waits` times, then `end`. */
function hub(end: Answer = { status: 200, body: { status: 'done', secrets: { token: TOKEN } } }, waits = 1, begin: Answer = STARTED) {
  let polls = 0;
  return fakeFetch({
    'GET /api/integrations/catalog': {
      status: 200,
      body: [
        { id: 'plex', name: 'Plex', version: '0.1.0', tier: 'silver', capabilities: ['media.sessions'], config_schema: PLEX },
        { id: 'sonarr', name: 'Sonarr', version: '0.1.0', tier: 'silver', capabilities: ['media.calendar'], config_schema: SONARR },
      ],
    },
    'GET /api/integrations/instances': { status: 200, body: [] },
    'POST /api/integrations/instances': { status: 201, body: { id: 'i9' } },
    'POST /api/integrations/authorize/plex': begin,
    'GET /api/integrations/authorize/plex/au_1': () => (polls++ < waits ? { status: 200, body: { status: 'pending', expires_at: '2026-10-03T14:35:00Z' } } : end),
  });
}

/** window.open, as a tab whose opener and location the test can read. */
function stubOpen(fail = false) {
  const tab = { opener: {} as unknown, location: { href: '' }, close: vi.fn() };
  const open = vi.fn(() => (fail ? null : tab));
  vi.stubGlobal('open', open);
  return { tab, open };
}

const input = (id: string) => document.getElementById(id) as HTMLInputElement;
const button = (text: string) => [...document.body.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim().startsWith(text));
const row = () => document.body.querySelector('.auth-row')!;

async function typeInto(id: string, value: string) {
  const el = input(id);
  el.value = value;
  el.dispatchEvent(new Event('input'));
  el.dispatchEvent(new Event('blur'));
  await nextTick();
}

async function editor(integrationId: string) {
  const h = harness();
  const w = h.mountIn(IntegrationEditor, { integrationId });
  await flushPromises();
  await nextTick();
  return { ...h, w };
}

beforeEach(() => {
  history.replaceState(null, '', '/settings/integrations/new/plex');
  syncRoute();
  resetIntegrations();
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('Sign in with …', () => {
  it('opens the sign-in, waits, and fills the token into the form only', async () => {
    const f = hub();
    const { tab, open } = stubOpen();
    await editor('plex');
    const signIn = button('Sign in with Plex')!;
    expect(signIn).toBeDefined();
    expect(signIn.closest('.field')!.querySelector('#f-token')).not.toBeNull();

    signIn.click();
    // The tab opens inside the click (so it isn't blocked), loses its opener,
    // then goes to the link.
    expect(open).toHaveBeenCalledWith('', '_blank');
    await flushPromises();
    expect(f.last('POST', '/api/integrations/authorize/plex')).toBeDefined();
    expect(tab.opener).toBeNull();
    expect(tab.location.href).toBe(LINK);
    expect(row().textContent).toContain('Waiting for Plex: approve Cha Dash in the tab that opened.');
    const again = row().querySelector('a')!;
    expect(again.getAttribute('href')).toBe(LINK);
    expect(again.getAttribute('rel')).toBe('noopener noreferrer');

    // Pending, then done.
    await vi.advanceTimersByTimeAsync(2000);
    await flushPromises();
    expect(f.calls.filter((c) => c.path === '/api/integrations/authorize/plex/au_1')).toHaveLength(1);
    expect(input('f-token').value).toBe('');
    await vi.advanceTimersByTimeAsync(2000);
    await flushPromises();
    expect(input('f-token').value).toBe(TOKEN);
    expect(input('f-token').type).toBe('password');
    expect(row().textContent).toContain('Signed in with Plex. The token is filled in: test the connection, then save.');
    expect(button('Sign in with Plex')).toBeDefined();
    // No more polling once it's done.
    await vi.advanceTimersByTimeAsync(10_000);
    expect(f.calls.filter((c) => c.path === '/api/integrations/authorize/plex/au_1')).toHaveLength(2);

    // It saves like a typed token.
    await typeInto('f-url', 'https://plex.lab.example');
    button('Save')!.click();
    await flushPromises();
    expect(f.last('POST', '/api/integrations/instances')!.body).toEqual({
      integration: 'plex',
      name: 'Plex',
      config: { url: 'https://plex.lab.example', tls: { mode: 'verify' } },
      secrets: { token: TOKEN },
    });
  });

  it('says when the sign-in expired or failed', async () => {
    hub({ status: 200, body: { status: 'expired' } }, 0);
    stubOpen();
    await editor('plex');
    button('Sign in with Plex')!.click();
    await flushPromises();
    await vi.advanceTimersByTimeAsync(2000);
    await flushPromises();
    expect(row().querySelector('[role="alert"]')!.textContent).toBe('The sign-in expired before Plex approved it. Try again.');
    expect(input('f-token').value).toBe('');

    document.body.innerHTML = '';
    resetIntegrations();
    hub({ status: 200, body: { status: 'failed', error: { code: 'auth', message: 'plex.tv refused the PIN.' } } }, 0);
    await editor('plex');
    button('Sign in with Plex')!.click();
    await flushPromises();
    await vi.advanceTimersByTimeAsync(2000);
    await flushPromises();
    expect(row().querySelector('[role="alert"]')!.textContent).toBe('Plex didn’t sign in: plex.tv refused the PIN.');
  });

  it('closes the tab when the hub can’t start, and works without a tab', async () => {
    hub(undefined, 0, { status: 502, body: { error: { code: 'unreachable', message: 'dial tcp: connection refused' } } });
    const { tab } = stubOpen();
    await editor('plex');
    button('Sign in with Plex')!.click();
    await flushPromises();
    expect(tab.close).toHaveBeenCalled();
    expect(row().querySelector('[role="alert"]')!.textContent).toBe('Couldn’t start signing in with Plex. Try again, or paste the token.');

    // A blocked popup still works: the link is there to click.
    document.body.innerHTML = '';
    resetIntegrations();
    hub();
    stubOpen(true);
    await editor('plex');
    button('Sign in with Plex')!.click();
    await flushPromises();
    expect(row().querySelector('a')!.getAttribute('href')).toBe(LINK);
  });

  it('stops polling when cancelled or closed', async () => {
    const f = hub({ status: 200, body: { status: 'pending' } }, 100);
    stubOpen();
    const { w } = await editor('plex');
    button('Sign in with Plex')!.click();
    await flushPromises();
    button('Cancel')!.click();
    await nextTick();
    await vi.advanceTimersByTimeAsync(10_000);
    expect(f.calls.filter((c) => c.path === '/api/integrations/authorize/plex/au_1')).toHaveLength(0);
    expect(button('Sign in with Plex')).toBeDefined();

    button('Sign in with Plex')!.click();
    await flushPromises();
    await vi.advanceTimersByTimeAsync(2000);
    await flushPromises();
    w.unmount();
    await vi.advanceTimersByTimeAsync(10_000);
    expect(f.calls.filter((c) => c.path === '/api/integrations/authorize/plex/au_1')).toHaveLength(1);
  });

  it('is only offered where the schema says so', async () => {
    hub();
    history.replaceState(null, '', '/settings/integrations/new/sonarr');
    syncRoute();
    await editor('sonarr');
    expect(input('f-api_key')).not.toBeNull();
    expect(button('Sign in with')).toBeUndefined();
    expect(document.body.querySelector('.auth-row')).toBeNull();
  });
});
