/**
 * Roles below admin: every screen hides what the role can't use, and none
 * calls a route the hub answers 403 (integrations, onboarding, the
 * correlator's decisions, label discovery), so no pane is ever broken.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { enableAutoUnmount, flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import InboxSheet from '../src/components/InboxSheet.vue';
import SuggestionItem from '../src/components/SuggestionItem.vue';
import SettingsSheet from '../src/settings/SettingsSheet.vue';
import { navigate, syncRoute } from '../src/lib/router';
import type { Incident } from '../src/model/types';
import { resetIntegrations } from '../src/stores/integrations';
import { axeViolations } from './axe';
import { engine, refuse } from './fake-engine';
import { fakeFetch } from './fake-fetch';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

enableAutoUnmount(afterEach);
beforeEach(() => {
  resetIntegrations();
  navigate('/', { replace: true });
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

async function settle(): Promise<void> {
  for (let i = 0; i < 20; i++) {
    await vi.dynamicImportSettled();
    await flushPromises();
    await nextTick();
  }
}

function as(role: string) {
  const h = harness(docs(withIncidents));
  h.session.state.user = { id: 'u2', username: 'kim', role };
  return h;
}

const FORBIDDEN = /^\/api\/(onboarding|integrations|users|audit|import|export|settings\/security|correlate|boards\/[^/]+\/discovery)/;

describe('below admin', () => {
  it('a repair says to ask an admin instead of offering Reconnect', async () => {
    const hub = fakeFetch({});
    const h = as('viewer');
    h.mountIn(InboxSheet);
    h.ui.openInbox('plex-token-revoked');
    await settle();
    const item = document.querySelector('.ib.open')!;
    expect([...item.querySelectorAll('button')].some((b) => b.textContent?.includes('Reconnect'))).toBe(false);
    expect(item.textContent).toContain('Ask an admin or the owner to reconnect it.');
    expect(document.querySelector('.ib-quick')).toBeNull();
    expect(await axeViolations()).toEqual([]);
    expect(hub.calls.filter((c) => FORBIDDEN.test(c.path))).toEqual([]);
  });

  it('the correlator’s question is shown, but only admins answer it', async () => {
    const h = as('operator');
    const q: Incident = {
      id: 'correlate.1',
      class: 'suggestion',
      severity: 'info',
      status: 'open',
      title: 'These look like the same thing. Merge?',
      summary: 'Sonarr and the container sonarr have matching names.',
      target: 'app:sonarr',
      opened_at: '2026-10-03T14:00:00Z',
      suggested: { id: 'correlate.confirm', label: 'Merge', tier: 'T0', target: 'app:sonarr' },
    };
    h.mountIn(SuggestionItem, { incident: q });
    await settle();
    expect(document.querySelector('.sugg-acts')).toBeNull();
    expect(document.body.textContent).toContain('An admin or the owner can answer this.');
  });

  it('an unavailable action names the reason, without a link into settings the role can’t open', async () => {
    const e = engine({ plan: () => refuse(409, 'unavailable', 'Add a control token to let Cha Dash back up guests on heron.', { recipe: 'i_pve' }) });
    fakeFetch(e.routes);
    const h = as('operator');
    h.ui.act(h.live.inbox.value!.incidents['backup-failed']!);
    await settle();
    expect(h.ui.state.toasts.at(-1)).toMatchObject({ text: 'Add a control token to let Cha Dash back up guests on heron.', tone: 'error' });
    expect(h.ui.state.toasts.at(-1)!.action).toBeUndefined();
  });

  for (const role of ['operator', 'viewer', 'household']) {
    it(`${role}: Settings is your passkeys, and asks nothing else of the hub`, async () => {
      const hub = fakeFetch({ 'GET /api/auth/passkeys': { status: 200, body: [] } });
      Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true });
      for (const path of ['/settings/security', '/settings/integrations']) {
        history.replaceState(null, '', path);
        syncRoute();
        const h = as(role);
        const w = h.mountIn(SettingsSheet);
        await settle();
        const nav = [...document.querySelectorAll('.settings nav a, .settings nav button')].map((a) => a.textContent?.trim() ?? '');
        expect(nav.some((t) => t.startsWith('Security'))).toBe(true);
        expect(nav.some((t) => /Integrations|Users|Audit|Backup|Import/.test(t))).toBe(false);
        expect(document.querySelector('.settings')?.textContent).toContain('Passkeys');
        expect(await axeViolations()).toEqual([]);
        w.unmount();
      }
      expect(hub.calls.filter((c) => FORBIDDEN.test(c.path))).toEqual([]);
    });
  }
});
