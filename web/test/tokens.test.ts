/**
 * Settings → API tokens and the Inbox's approvals: plans an API token made
 * that wait for an admin or the owner. The hub's side is faked to the shapes
 * in internal/README.md → API.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import ApprovalItem from '../src/components/ApprovalItem.vue';
import HoldToConfirm from '../src/components/HoldToConfirm.vue';
import InboxSheet from '../src/components/InboxSheet.vue';
import TokensPane from '../src/settings/TokensPane.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { navigate } from '../src/lib/router';
import { groupInbox, incidentsFor, resolved } from '../src/model/inbox';
import type { Incident } from '../src/model/types';
import { axeViolations } from './axe';
import { fakeFetch, type Handler } from './fake-fetch';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

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
}

const SECRET = 'cdt_aaaaaaaaaaaaaaaaaaaaaaaaaa_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA';

const TOKENS = [
  { id: 'tok_a', user_id: 'u1', username: 'owner', name: 'backup script', scopes: ['graph.read', 'docker.*'], created_at: '2026-10-01T10:00:00Z', expires_at: '2026-12-30T10:00:00Z', last_used_at: '2026-10-03T13:58:00Z', last_used_ip: '192.0.2.12', revoked_at: null },
  { id: 'tok_b', user_id: 'u2', username: 'sam', name: 'old cron', scopes: ['*'], created_at: '2026-09-01T10:00:00Z', expires_at: '2026-12-01T10:00:00Z', last_used_at: null, revoked_at: '2026-09-20T10:00:00Z' },
];

const SCOPES = {
  reads: [
    { scope: 'graph.read', label: 'Read the Home Graph', description: 'Resources and relations.', available: true },
    { scope: 'inbox.read', label: 'Read the Inbox', description: 'Incidents.', available: true },
    { scope: 'audit.read', label: 'Read the audit log', description: 'Every change.', available: false, unavailable: 'Your role can’t read this, so a token of yours can’t either.' },
  ],
  families: [{ scope: 'docker.*', label: 'Every docker action your role allows', available: true }],
  actions: [
    { scope: 'docker.container.start', label: 'Start', tier: 'T1', available: true },
    { scope: 'docker.container.remove', label: 'Remove', tier: 'T3', available: false, unavailable: 'Tokens can’t run T3 or T4 actions.' },
  ],
  all: { scope: '*', label: 'Everything your role allows', description: 'Every read and every action up to T2.', available: true },
  default_days: 90,
  max_days: 365,
  approval: 'T2 actions wait in the Inbox.',
};

function hub(over: Record<string, Handler | { status: number; body?: unknown }> = {}) {
  return fakeFetch({
    'GET /api/tokens': { status: 200, body: { tokens: TOKENS } },
    'GET /api/tokens/scopes': { status: 200, body: SCOPES },
    'POST /api/tokens': (b) => ({
      status: 201,
      body: { token: { id: 'tok_new', user_id: 'u1', username: 'owner', name: (b as { name: string }).name, scopes: (b as { scopes: string[] }).scopes, created_at: '2026-10-03T14:05:00Z', expires_at: '2027-01-01T14:05:00Z', last_used_at: null, revoked_at: null }, secret: SECRET },
    }),
    'DELETE /api/tokens/tok_a': { status: 200, body: { token: { ...TOKENS[0], revoked_at: '2026-10-03T14:06:00Z' } } },
    ...over,
  });
}

describe('Settings → API tokens', () => {
  it('lists tokens with their scopes, expiry and last use, never a secret', async () => {
    hub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(TokensPane);
    await settle();
    expect(w.find('#tk-h').text()).toBe('Everyone’s tokens');
    const rows = w.findAll('.tk-row');
    expect(rows).toHaveLength(2);
    expect(rows[0]!.text()).toContain('backup script');
    expect(rows[0]!.text()).toContain('graph.read, docker.*');
    expect(rows[0]!.text()).toContain('from 192.0.2.12');
    expect(rows[1]!.text()).toContain('sam'); // someone else's, for an owner
    expect(rows[1]!.text()).toContain('revoked');
    expect(rows[1]!.text()).toContain('everything your role allows');
    expect(rows[1]!.find('button').exists()).toBe(false);
    expect(await axeViolations()).toEqual([]);
  });

  it('makes a token from the picker and shows its secret once, with Copy', async () => {
    const f = hub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(TokensPane);
    await settle();
    await w.findAll('button').find((b) => b.text() === 'New token')!.trigger('click');
    await settle();
    // Reads the role can't use, and T3 actions, are offered disabled with the reason.
    const audit = w.find('input[value="audit.read"]');
    expect(audit.attributes('disabled')).toBeDefined();
    expect(w.text()).toContain('Your role can’t read this');
    expect(await axeViolations()).toEqual([]);
    await w.find('input[type="text"]').setValue('backup script');
    await w.find('input[value="graph.read"]').setValue(true);
    await w.find('input[type="radio"][value="pick"]').setValue(true);
    await settle();
    expect(w.find('input[value="docker.container.remove"]').attributes('disabled')).toBeDefined();
    await w.find('input[value="docker.container.start"]').setValue(true);
    await w.find('select').setValue('30');
    await w.find('form.tk-add').trigger('submit');
    await settle();
    expect(f.last('POST', '/api/tokens')?.body).toEqual({ name: 'backup script', scopes: ['graph.read', 'docker.container.start'], expires_in_days: 30 });
    const secret = w.find<HTMLInputElement>('.tk-fresh input');
    expect(secret.element.value).toBe(SECRET);
    expect(w.text()).toContain('This is the only time Cha Dash shows it');
    expect(await axeViolations()).toEqual([]);
    const writeText = vi.fn(async () => undefined);
    Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true });
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } });
    await w.findAll('button').find((b) => b.text() === 'Copy')!.trigger('click');
    await settle();
    expect(writeText).toHaveBeenCalledWith(SECRET);
    expect(w.findAll('button').some((b) => b.text() === 'Copied')).toBe(true);
    await w.findAll('button').find((b) => b.text() === 'I’ve saved it')!.trigger('click');
    await settle();
    expect(w.text()).not.toContain(SECRET);
    expect(w.findAll('.tk-row')).toHaveLength(3);
    Object.defineProperty(window, 'isSecureContext', { value: undefined, configurable: true });
  });

  it('asks before revoking, then marks the token revoked', async () => {
    const f = hub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(TokensPane);
    await settle();
    await w.find('button[aria-label="Revoke backup script"]').trigger('click');
    expect(w.find('.tk-revoke').text()).toContain('stops working at once');
    expect(await axeViolations()).toEqual([]);
    await w.find('.tk-revoke .btn.danger').trigger('click');
    await settle();
    expect(f.last('DELETE', '/api/tokens/tok_a')).toBeDefined();
    expect(w.findAll('.tk-row')[1]!.text()).toContain('revoked'); // sorted after the live ones
    expect(h.ui.state.toasts.at(-1)?.text).toContain('Revoked “backup script”');
  });

  it('a viewer manages their own tokens only', async () => {
    hub({ 'GET /api/tokens': { status: 200, body: { tokens: [TOKENS[1]] } } });
    const h = harness(docs(withIncidents));
    h.session.state.user = { id: 'u2', username: 'sam', role: 'viewer' };
    const w = h.mountIn(TokensPane);
    await settle();
    expect(w.find('#tk-h').text()).toBe('Your tokens');
    expect(w.find('.tk-row .chip').text()).toBe('revoked');
  });

  it('refuses an empty pick before asking the hub', async () => {
    const f = hub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(TokensPane);
    await settle();
    await w.findAll('button').find((b) => b.text() === 'New token')!.trigger('click');
    await settle();
    await w.find('input[type="text"]').setValue('x');
    await w.find('form.tk-add').trigger('submit');
    await settle();
    expect(w.find('.err').text()).toBe('Pick at least one thing the token may read or run.');
    expect(f.last('POST', '/api/tokens')).toBeUndefined();
  });
});

const APPROVAL: Incident = {
  id: 'approval.apv_1',
  scenario: 'approval',
  class: 'approval',
  severity: 'attention',
  status: 'open',
  phase: 'awaiting_approval',
  title: 'backup script (owner) asks to run: Restart sonarr',
  summary: 'Planned through the API by a token, a T2 action.',
  target: 'docker:magpie/c/sonarr',
  opened_at: '2026-10-03T14:00:00Z',
  causes: [],
  blast_radius: [{ resource: 'app:sonarr', via: 'runs_on', depth: 1 }],
  approval: {
    id: 'apv_1',
    state: 'awaiting_approval',
    plan: { id: 'p_1', hash: 'sha256:abc', action: 'docker.container.restart', label: 'Restart sonarr', target: 'docker:magpie/c/sonarr', tier: 'T2', steps: [{ label: 'Ask the node to restart sonarr' }], impact: [{ resource: 'app:sonarr', via: 'runs_on', depth: 1 }] },
    originator: { kind: 'token', id: 'tok_a', name: 'backup script (owner)', user: 'u1', user_name: 'owner' },
    requested_at: '2026-10-03T14:00:00Z',
    expires_at: '2026-10-03T15:00:00Z',
  },
};

function withApproval() {
  const d = docs(withIncidents);
  d.inbox.incidents[APPROVAL.id] = structuredClone(APPROVAL);
  return d;
}

describe('approvals in the Inbox', () => {
  it('have their own group, count as needing you, and never reach tiles or Resolved', () => {
    const d = withApproval();
    const g = groupInbox(d.inbox);
    expect(g.approvals.map((i) => i.id)).toEqual([APPROVAL.id]);
    expect(g.open.some((i) => i.id === APPROVAL.id)).toBe(false);
    expect(incidentsFor(d.inbox, ['docker:magpie/c/sonarr'])).toEqual([]);
    d.inbox.incidents[APPROVAL.id]!.status = 'resolved';
    expect(resolved(d.inbox).some((i) => i.id === APPROVAL.id)).toBe(false);
  });

  it('show who asked and the hub’s plan; Approve holds, steps up afresh, then approves the hash', async () => {
    const f = fakeFetch({ 'POST /api/approvals/apv_1/approve': { status: 202, body: { task: { id: 't_1', state: 'queued', action: 'docker.container.restart', label: 'Restart sonarr', target: 'docker:magpie/c/sonarr' } } } });
    const h = harness(withApproval());
    h.mountIn(InboxSheet);
    h.ui.openInbox();
    await settle();
    const item = document.querySelector('.apv')!;
    expect(item).not.toBeNull();
    expect(document.querySelector('.ib-head .n')?.textContent).toMatch(/need/);
    expect(item.textContent).toContain('Asked by backup script (owner), a token of owner’s');
    expect(item.textContent).toContain('Ask the node to restart sonarr');
    expect(item.textContent).toContain('Expires15:00');
    expect(await axeViolations()).toEqual([]);

    const w = h.mountIn(ApprovalItem, { incident: APPROVAL });
    w.findComponent(HoldToConfirm).vm.$emit('confirm');
    await settle();
    // A fresh step-up every time, before anything reaches the hub.
    expect(h.ui.state.stepUp).toMatchObject({ fresh: true });
    expect(f.last('POST', '/api/approvals/apv_1/approve')).toBeUndefined();
    h.ui.stepUpDone(true);
    await settle();
    expect(f.last('POST', '/api/approvals/apv_1/approve')?.body).toEqual({ hash: 'sha256:abc' });
    expect(h.ui.state.toasts.at(-1)?.text).toBe('Approved: Restart sonarr. It runs as backup script (owner).');
  });

  it('a cancelled step-up approves nothing; the hub’s refusal is said as is', async () => {
    const f = fakeFetch({ 'POST /api/approvals/apv_1/approve': { status: 409, body: { error: { code: 'plan_changed', message: 'The target changed since this was planned.' } } } });
    const h = harness(withApproval());
    const w = h.mountIn(ApprovalItem, { incident: APPROVAL });
    w.findComponent(HoldToConfirm).vm.$emit('confirm');
    await settle();
    h.ui.stepUpDone(false);
    await settle();
    expect(f.calls).toEqual([]);
    w.findComponent(HoldToConfirm).vm.$emit('confirm');
    await settle();
    h.ui.stepUpDone(true);
    await settle();
    expect(w.find('.err').text()).toBe('The target changed since this was planned.');
  });

  it('Deny takes an optional reason and needs no step-up', async () => {
    const f = fakeFetch({ 'POST /api/approvals/apv_1/deny': { status: 200, body: { approval: { ...APPROVAL.approval, state: 'denied' } } } });
    const h = harness(withApproval());
    const w = h.mountIn(ApprovalItem, { incident: APPROVAL });
    await w.findAll('button').find((b) => b.text() === 'Deny')!.trigger('click');
    expect(await axeViolations(w.element as Element)).toEqual([]);
    await w.find('.apv-deny input').setValue('Not during the film.');
    await w.find('.apv-deny').trigger('submit');
    await settle();
    expect(f.last('POST', '/api/approvals/apv_1/deny')?.body).toEqual({ reason: 'Not during the film.' });
    expect(h.ui.state.stepUp).toBeUndefined();
    expect(h.ui.state.toasts.at(-1)?.text).toBe('Denied: Restart sonarr.');
  });

  it('below admin, says who can approve and offers nothing', async () => {
    fakeFetch({});
    const h = harness(withApproval());
    h.session.state.user = { id: 'u2', username: 'kim', role: 'operator' };
    const w = h.mountIn(ApprovalItem, { incident: APPROVAL });
    await settle();
    expect(w.text()).toContain('An admin or the owner can approve this.');
    expect(w.findComponent(HoldToConfirm).exists()).toBe(false);
  });
});
