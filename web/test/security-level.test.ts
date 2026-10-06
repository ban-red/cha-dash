import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import Briefing from '../src/components/Briefing.vue';
import InboxSheet from '../src/components/InboxSheet.vue';
import SecurityLevel from '../src/settings/SecurityLevel.vue';
import SecurityPane from '../src/settings/SecurityPane.vue';
import IncidentsWidget from '../src/widgets/IncidentsWidget.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { summarize } from '../src/model/briefing';
import { groupInbox, hintsHeading, isHint, settling } from '../src/model/inbox';
import type { Incident } from '../src/model/types';
import { axeViolations } from './axe';
import { fakeFetch } from './fake-fetch';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

/** What GET /api/settings/security answers (internal/security). */
const LEVELS = [
  { id: 'relaxed', label: 'Relaxed · LAN-only home network', description: 'Security findings stay in the Inbox as hints; they never notify you.', default: true },
  { id: 'standard', label: 'Standard · reachable beyond the LAN (VPN, reverse proxy)', description: 'Exposures that hand out control or credentials need you; advice stays a hint.' },
  { id: 'strict', label: 'Strict · exposed to the internet or shared networks', description: 'Every security finding needs you, advice included.' },
];
const view = (posture: string) => ({ posture, levels: LEVELS });

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('Settings → Security → Security level', () => {
  it('is a native radio group with the hub’s levels, Relaxed marked as the default', async () => {
    fakeFetch({ 'GET /api/settings/security': { status: 200, body: view('relaxed') } });
    const { mountIn } = harness();
    const w = mountIn(SecurityLevel);
    await flushPromises();
    const group = w.find('fieldset.sl-group');
    expect(group.find('legend').text()).toBe('Security level');
    const radios = group.findAll<HTMLInputElement>('input[type="radio"]');
    expect(radios).toHaveLength(3);
    expect(new Set(radios.map((r) => r.attributes('name'))).size).toBe(1);
    expect(radios.map((r) => r.element.checked)).toEqual([true, false, false]);
    const cards = group.findAll('label.sl-card');
    expect(cards.map((c) => c.find('.sl-label').text())).toEqual([
      'Relaxed · LAN-only home networkdefault',
      'Standard · reachable beyond the LAN (VPN, reverse proxy)',
      'Strict · exposed to the internet or shared networks',
    ]);
    // Each radio is described by its one-line description.
    const described = document.getElementById(radios[2]!.attributes('aria-describedby')!);
    expect(described?.textContent).toBe(LEVELS[2]!.description);
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
  });

  it('shows the hub’s deployment notes above the levels', async () => {
    const message = 'Requests are arriving through what looks like a reverse proxy that Cha Dash doesn’t trust.';
    fakeFetch({ 'GET /api/settings/security': { status: 200, body: { ...view('relaxed'), notes: [{ code: 'untrusted_proxy', message }] } } });
    const { mountIn } = harness();
    const w = mountIn(SecurityLevel);
    await flushPromises();
    const notes = w.findAll('[role="note"][data-code]');
    expect(notes.map((n) => [n.attributes('data-code'), n.text()])).toEqual([['untrusted_proxy', message]]);
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
  });

  it('saves a choice at once, and puts it back when the hub refuses', async () => {
    let posture = 'relaxed';
    const f = fakeFetch({
      'GET /api/settings/security': { status: 200, body: view('relaxed') },
      'PUT /api/settings/security': (body) => {
        const want = (body as { posture: string }).posture;
        if (want === 'standard') return { status: 422, body: { error: { code: 'invalid', message: '"standard" isn’t a security level' } } };
        posture = want;
        return { status: 200, body: view(posture) };
      },
    });
    const { mountIn, ui } = harness();
    const w = mountIn(SecurityLevel);
    await flushPromises();
    const radios = () => w.findAll<HTMLInputElement>('input[type="radio"]');

    await radios()[2]!.setValue(true);
    await flushPromises();
    expect(f.last('PUT', '/api/settings/security')!.body).toEqual({ posture: 'strict' });
    expect(radios().map((r) => r.element.checked)).toEqual([false, false, true]);
    expect(w.find('.sl-card.on').text()).toContain('Strict');
    expect(ui.state.toasts.at(-1)!.text).toBe('Security level: Strict.');

    await radios()[1]!.setValue(true);
    await flushPromises();
    expect(radios().map((r) => r.element.checked)).toEqual([false, false, true]);
    expect(w.find('.err[role="alert"]').text()).toContain('isn’t a security level');
    expect(posture).toBe('strict');
  });

  it('sits at the top of the Security pane, above passkeys, and says so when it can’t load', async () => {
    fakeFetch({ 'GET /api/auth/passkeys': { status: 200, body: [] } });
    const { mountIn } = harness();
    const w = mountIn(SecurityPane);
    await flushPromises();
    const headings = w.findAll('.st-section > header h3').map((h) => h.text());
    expect(headings).toEqual(['Security level', 'Confirming destructive actions', 'Passkeys', 'Where Cha Dash runs']);
    expect(w.find('.st-section .st-note.bad').text()).toContain('doesn’t have that yet');
  });
});

/**
 * The hub publishes a security finding at the severity its level gives it
 * (internal/security): Relaxed every one at info, Standard as declared,
 * Strict every one at attention. This builds the Demo Home's inbox as each
 * level would publish it, plus one exposure declared attention (Traefik
 * sending credentials over plain http).
 */
type Level = 'relaxed' | 'standard' | 'strict';
const PLAIN_HTTP = 'traefik.hint.plain_http_credentials';
const SECURITY = ['traefik.hint.api_no_auth', 'docker-jupiter.hint.docker_endpoint', 'frigate.unauthenticated-admin', PLAIN_HTTP];

function publish(level: Level, declared: Incident['severity']): Incident['severity'] {
  return level === 'relaxed' ? 'info' : level === 'strict' ? 'attention' : declared;
}

function atLevel(level: Level, opts: { fixing?: boolean } = {}) {
  const d = docs(withIncidents);
  const hint = d.inbox.incidents['traefik.hint.api_no_auth']!;
  d.inbox.incidents[PLAIN_HTTP] = {
    ...structuredClone(hint),
    id: PLAIN_HTTP,
    title: 'Traefik sends its credentials in clear text',
    severity: 'attention',
  };
  for (const id of SECURITY) {
    const i = d.inbox.incidents[id]!;
    expect(i.category).toBe('security');
    i.severity = publish(level, i.severity);
    // The user changed the instance: the hub says it's being fixed.
    if (opts.fixing) i.phase = 'fixing';
  }
  return d;
}

function withoutSecurity() {
  const d = docs(withIncidents);
  for (const id of SECURITY) delete d.inbox.incidents[id];
  return d;
}

describe('security findings at each level', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
    vi.setSystemTime(new Date('2026-10-03T14:05:10Z'));
    setDisplayTimeZone('UTC');
  });

  async function surfaces(data: ReturnType<typeof docs>) {
    const h = harness(data, { demo: true });
    const brief = h.mountIn(Briefing);
    const tile = h.mountIn(IncidentsWidget, { size: 'large' });
    h.mountIn(InboxSheet);
    h.ui.openInbox();
    await flushPromises();
    await nextTick();
    const panel = document.body.querySelector<HTMLElement>('.inbox')!;
    const inbox = h.live.inbox.value!;
    const res = {
      badge: Number(brief.find('.pill .badge').text()),
      needsAttention: h.live.problems.value.map((i) => i.id),
      briefing: summarize(h.live.graph.value, h.live.metrics.value, inbox, Date.now()).problems.map((i) => i.id),
      tile: tile.findAll('.li .t').map((t) => t.text()),
      groups: [...panel.querySelectorAll('.ib-grp')].map((g) => g.textContent?.trim()),
      hints: groupInbox(inbox).hints.map((i) => i.id),
    };
    document.body.innerHTML = '';
    return res;
  }

  it('Relaxed: every finding is a security hint, in no core surface', async () => {
    const base = await surfaces(withoutSecurity());
    const r = await surfaces(atLevel('relaxed'));
    expect(r.badge).toBe(base.badge);
    expect(r.needsAttention).toEqual(base.needsAttention);
    expect(r.briefing).toEqual(base.briefing);
    expect(r.tile).toEqual(base.tile);
    expect(r.hints.sort()).toEqual([...SECURITY].sort());
    expect(r.groups).toContain('Security hints · 4');
  });

  it('Standard: the exposure needs you; advice stays a hint', async () => {
    const base = await surfaces(withoutSecurity());
    const r = await surfaces(atLevel('standard'));
    expect(r.badge).toBe(base.badge + 1);
    expect(r.needsAttention).toContain(PLAIN_HTTP);
    expect(r.needsAttention.filter((id) => SECURITY.includes(id))).toEqual([PLAIN_HTTP]);
    expect(r.briefing).toContain(PLAIN_HTTP);
    expect(r.tile).toContain('Traefik sends its credentials in clear text');
    expect(r.hints).toHaveLength(3);
    expect(r.groups).toContain('Security hints · 3');
  });

  it('Strict: every finding needs you, and there are no hints left', async () => {
    const base = await surfaces(withoutSecurity());
    const r = await surfaces(atLevel('strict'));
    expect(r.badge).toBe(base.badge + 4);
    expect(r.needsAttention.filter((id) => SECURITY.includes(id)).sort()).toEqual([...SECURITY].sort());
    expect(r.briefing.filter((id) => SECURITY.includes(id))).toHaveLength(4);
    expect(r.hints).toEqual([]);
    expect(r.groups.some((g) => g?.includes('hints'))).toBe(false);
  });

  it('an info finding stays a hint while it’s being fixed, so the Incidents tile never lists it', async () => {
    const d = atLevel('relaxed', { fixing: true });
    for (const id of SECURITY) expect(isHint(d.inbox.incidents[id]!)).toBe(true);
    expect(settling(d.inbox).filter((i) => SECURITY.includes(i.id))).toEqual([]);
    const base = await surfaces(withoutSecurity());
    const r = await surfaces(d);
    expect(r.tile).toEqual(base.tile);
    expect(r.badge).toBe(base.badge);
    expect(r.groups).toContain('Security hints · 4');
  });
});

describe('the Hints heading', () => {
  const hint = (id: string, category?: string): Incident =>
    ({ id, class: 'problem', severity: 'info', status: 'open', title: id, summary: '', target: 'app:x', opened_at: '2026-10-03T00:00:00Z', ...(category ? { category } : {}) }) as Incident;

  it('says “Security hints” only when every hint is a security finding', () => {
    expect(hintsHeading([hint('a', 'security'), hint('b', 'security')])).toBe('Security hints · 2');
    expect(hintsHeading([hint('a', 'security'), hint('b')])).toBe('Hints · 2');
    expect(hintsHeading([hint('a')])).toBe('Hints · 1');
  });
});
