/**
 * The timeline (GET /api/timeline, wave 28): the model's grouping and
 * Markdown, TimelineList in every state, Settings → Timeline, and the other
 * wave 28 settings: incident retention in History, the morning digest in
 * Notifications, and a Proxmox instance's PVE notifications secret.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import TimelineList from '../src/components/TimelineList.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { navigate } from '../src/lib/router';
import { byDay, timelineMarkdown, type Timeline } from '../src/model/timeline';
import HistoryPane from '../src/settings/HistoryPane.vue';
import NotificationsPane from '../src/settings/NotificationsPane.vue';
import PveHook from '../src/settings/PveHook.vue';
import TimelinePane from '../src/settings/TimelinePane.vue';
import { axeViolations } from './axe';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

async function settle(): Promise<void> {
  await vi.dynamicImportSettled();
  await flushPromises();
  await nextTick();
  await flushPromises();
}

beforeEach(() => {
  setDisplayTimeZone('UTC');
  navigate('/settings/timeline', { replace: true });
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

const home = () => ({
  graph: {
    resources: {
      'container:sonarr': { id: 'container:sonarr', kind: 'container', name: 'sonarr', health: 'ok' },
      'host:stork': { id: 'host:stork', kind: 'host', name: 'stork', health: 'ok' },
    },
    relations: {},
  },
  metrics: { series: {} },
  inbox: { now: '2026-10-31T12:05:00Z', incidents: {} },
});

const entry = (at: string, kind: string, text: string, extra: Record<string, unknown> = {}) => ({ at, kind, text, message: `timeline.${kind}`, resource: 'container:sonarr', ...extra });
const TL: Timeline = {
  from: '2026-10-24T12:00:00Z',
  to: '2026-10-31T12:00:00Z',
  resource: 'container:sonarr',
  truncated: false,
  history: 'standard',
  entries: [
    entry('2026-10-30T23:50:00Z', 'window', 'Maintenance window Sunday updates began', { event: 'start', resource: 'host:stork' }),
    entry('2026-10-31T02:10:00Z', 'incident', 'Sonarr isn’t answering opened', { event: 'opened', severity: 'down' }),
    entry('2026-10-31T02:10:00Z', 'cause', 'Sonarr isn’t answering: stork is out of memory', { event: 'caused', resource: 'host:stork' }),
    entry('2026-10-31T02:15:00Z', 'action', 'owner ran Restart on sonarr', { event: 'ok', severity: 'ok' }),
    entry('2026-10-31T02:16:00Z', 'state', 'sonarr went from down to ok', { severity: 'ok' }),
    entry('2026-10-31T02:16:00Z', 'incident', 'Sonarr isn’t answering resolved after an action [see](http://x.example)', { event: 'resolved', severity: 'ok' }),
  ] as Timeline['entries'],
};

describe('the timeline model', () => {
  it('groups newest first by day, and copies oldest first as escaped Markdown', () => {
    const days = byDay(TL.entries);
    expect(days.map((d) => d.day)).toEqual(['Saturday, 31 October 2026', 'Friday, 30 October 2026']);
    expect(days[0]!.entries[0]!.kind).toBe('incident');
    expect(days[0]!.entries[0]!.event).toBe('resolved');
    const md = timelineMarkdown(TL, 'sonarr');
    expect(md).toContain('# Timeline: sonarr');
    expect(md).toContain('## Friday, 30 October 2026\n\n- 23:50 Maintenance window Sunday updates began');
    expect(md).toContain('- 02:10 Sonarr isn’t answering opened\n- 02:10 Sonarr isn’t answering: stork is out of memory');
    // Copy from the hub can't make a link or a heading.
    expect(md).toContain('after an action \\[see\\](http://x.example)');
    expect(md.indexOf('23:50')).toBeLessThan(md.indexOf('02:16'));
    expect(timelineMarkdown({ ...TL, entries: [] }, 'sonarr')).toContain('Nothing happened in this range.');
  });
});

describe('TimelineList', () => {
  it('reads the resource’s week, marks each entry in words, copies it, and passes axe', async () => {
    const hub = fakeFetch({ 'GET /api/timeline': { status: 200, body: TL } });
    const writeText = vi.fn(async () => undefined);
    Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true });
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    const h = harness(home());
    const w = h.mountIn(TimelineList, { resource: 'container:sonarr', title: 'sonarr' });
    await settle();
    const call = hub.calls.find((c) => c.path.startsWith('/api/timeline'))!;
    expect(call.path).toContain('resource=container%3Asonarr');
    expect(w.findAll('.tl-day-h').map((x) => x.text())).toEqual(['Saturday, 31 October 2026', 'Friday, 30 October 2026']);
    const items = w.findAll('.tl-item');
    expect(items).toHaveLength(6);
    expect(items[0]!.find('[role="img"]').attributes('aria-label')).toBe('resolved');
    expect(items.map((i) => i.find('[role="img"]').attributes('aria-label'))).toContain('cause');
    expect(items[0]!.find('time').attributes('datetime')).toBe('2026-10-31T02:16:00Z');
    expect(await axeViolations(w.element.parentElement!, ['region'])).toEqual([]); // the sheet or pane is its landmark

    await w.findAll('button').find((b) => b.text() === 'Copy as Markdown')!.trigger('click');
    await settle();
    expect(writeText).toHaveBeenCalledOnce();
    expect(String((writeText.mock.calls[0] as unknown[])[0])).toContain('# Timeline: sonarr');

    // A day instead of a week.
    await w.findAll('button').find((b) => b.text() === '24 hours')!.trigger('click');
    await settle();
    expect(w.find('button[aria-pressed="true"]').text()).toBe('24 hours');
    expect(hub.calls.filter((c) => c.path.startsWith('/api/timeline'))).toHaveLength(2);
  });

  it('says when nothing happened, when history is off, and recovers from an error', async () => {
    let fail = true;
    fakeFetch({
      'GET /api/timeline': () => (fail ? { status: 500, body: { error: { code: 'timeline_failed', message: 'The timeline couldn’t be read.' } } } : { status: 200, body: { ...TL, entries: [], history: 'off' } }),
    });
    const h = harness(home());
    const w = h.mountIn(TimelineList, { title: 'the whole home' });
    await settle();
    expect(w.find('[role="alert"]').text()).toContain('The timeline couldn’t be read.');
    expect(await axeViolations(w.element.parentElement!, ['region'])).toEqual([]); // the sheet or pane is its landmark
    fail = false;
    await w.findAll('button').find((b) => b.text() === 'Try again')!.trigger('click');
    await settle();
    expect(w.text()).toContain('Nothing happened in the last 7 days.');
    expect(w.text()).toContain('keeps no health history');
    expect(await axeViolations(w.element.parentElement!, ['region'])).toEqual([]); // the sheet or pane is its landmark
  });
});

describe('Settings → Timeline', () => {
  it('shows the whole home with every range and names, or one resource from ?resource=', async () => {
    const hub = fakeFetch({ 'GET /api/timeline': { status: 200, body: { ...TL, resource: undefined } } });
    const h = harness(home());
    const w = h.mountIn(TimelinePane);
    await settle();
    expect(w.find('h3').text()).toBe('Timeline');
    expect(w.findAll('.tl-span').map((b) => b.text())).toEqual(['24 hours', '7 days', '30 days', '90 days']);
    expect(w.findAll('.tl-res').map((b) => b.text())).toContain('sonarr');
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
    w.unmount();

    navigate('/settings/timeline?resource=container%3Asonarr', { replace: true });
    const w2 = h.mountIn(TimelinePane);
    await settle();
    expect(w2.find('h3').text()).toBe('Timeline · sonarr');
    expect(hub.calls.at(-1)!.path).toContain('resource=container%3Asonarr');
    await w2.findAll('button').find((b) => b.text() === 'Show the whole home')!.trigger('click');
    await settle();
    expect(w2.find('h3').text()).toBe('Timeline');
  });
});

describe('Settings → History: incident retention', () => {
  it('saves how long incidents are kept, even while history is locked', async () => {
    const view = {
      level: 'off', locked: true, demo: false, series: 0, resources: 0, file_bytes: 0, max_bytes: 0, trimmed_at: null, tiers: [], instances: [], excluded: [],
      exclude_keys: [], estimates: {}, incident_keep_ms: 90 * 86_400_000, incident_default_keep_ms: 90 * 86_400_000,
      incident_choices_ms: [7, 30, 90, 180, 365].map((d) => d * 86_400_000),
    };
    const hub = fakeFetch({
      'GET /api/settings/history': { status: 200, body: view },
      'PUT /api/settings/history': (b) => ({ status: 200, body: { ...view, ...(b as object) } }),
      'GET /api/integrations/instances': { status: 200, body: { instances: [] } },
    });
    const h = harness(home());
    const w = h.mountIn(HistoryPane);
    await settle();
    const select = w.find('select[id$="-inc"]');
    expect(select.findAll('option').map((o) => o.text())).toEqual(['7 days', '30 days', '90 days (default)', '180 days', '1 year']);
    await select.setValue(String(30 * 86_400_000));
    await w.find('button[aria-label="Save how long incidents are kept"]').trigger('click');
    await settle();
    expect(hub.last('PUT', '/api/settings/history')?.body).toEqual({ incident_keep_ms: 30 * 86_400_000 });
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
  });
});

describe('Settings → Notifications: the morning digest', () => {
  const RULE = { id: 'digest', label: 'Morning digest', description: 'Once a day.', enabled: true, resolved: false, channels: null, has_resolved: false, defaults: { enabled: false, resolved: false, channels: null }, updated_at: null };
  const DIGEST = { time: '07:30', all_quiet: false, enabled: true, zone: 'Europe/Berlin', last_sent_at: null, next_at: '2026-11-01T06:30:00Z' };
  it('sets its time and all quiet under its rule', async () => {
    const hub = fakeFetch({
      'GET /api/notify/rules': { status: 200, body: { rules: [RULE] } },
      'GET /api/notify/channels': { status: 200, body: { channels: [], public_url: true, web_push: true } },
      'GET /api/notify/deliveries': { status: 200, body: { deliveries: [] } },
      'GET /api/notify/push/subscriptions': { status: 200, body: { subscriptions: [] } },
      'GET /api/notify/digest': { status: 200, body: DIGEST },
      'PUT /api/notify/digest': (b) => ({ status: 200, body: { ...DIGEST, ...(b as object) } }),
    });
    const h = harness(home());
    const w = h.mountIn(NotificationsPane);
    await settle();
    const digest = w.find('.nr-digest');
    expect(digest.text()).toContain('Europe/Berlin, the hub’s time zone.');
    await digest.find('input[type="time"]').setValue('06:45');
    await digest.find('button[aria-label="Save the digest\'s time"]').trigger('click');
    await settle();
    expect(hub.last('PUT', '/api/notify/digest')?.body).toEqual({ time: '06:45' });
    await digest.find('input[type="checkbox"]').setValue(true);
    await settle();
    expect(hub.last('PUT', '/api/notify/digest')?.body).toEqual({ all_quiet: true });
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
  });
});

describe('a Proxmox instance’s PVE notifications', () => {
  it('makes a secret, shows it once, rotates and removes it after asking', async () => {
    let set = false;
    const view = () => ({ instance: 'proxmox-wren', set, path: '/api/hooks/pve/proxmox-wren', header: 'X-Chadash-Webhook-Secret', ...(set ? { created_at: Date.parse('2026-10-31T12:00:00Z') } : {}) });
    const hub = fakeFetch({
      'GET /api/integrations/instances/proxmox-wren/pve-hook': () => ({ status: 200, body: view() }),
      'POST /api/integrations/instances/proxmox-wren/pve-hook': () => {
        set = true;
        return { status: 200, body: { ...view(), secret: 'f'.repeat(64) } };
      },
      'DELETE /api/integrations/instances/proxmox-wren/pve-hook': () => {
        set = false;
        return { status: 204 };
      },
    });
    const h = harness(home());
    const w = h.mountIn(PveHook, { instance: 'proxmox-wren' });
    await settle();
    expect(w.text()).toContain('No secret yet');
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);

    await w.findAll('button').find((b) => b.text() === 'Make a secret')!.trigger('click');
    await settle();
    expect((w.find('input[readonly][id$="-s"]').element as HTMLInputElement).value).toBe('f'.repeat(64));
    expect((w.find('input[readonly][id$="-u"]').element as HTMLInputElement).value).toMatch(/\/api\/hooks\/pve\/proxmox-wren$/);
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
    await w.findAll('button').find((b) => b.text() === 'I’ve saved it')!.trigger('click');
    await settle();
    expect(w.text()).not.toContain('f'.repeat(64));
    expect(w.text()).toContain('A secret is set');

    await w.findAll('button').find((b) => b.text() === 'Remove')!.trigger('click');
    expect(w.find('[role="group"]').text()).toContain('Remove the secret?');
    await w.find('[role="group"]').findAll('button').find((b) => b.text() === 'Remove')!.trigger('click');
    await settle();
    expect(hub.calls.some((c) => c.method === 'DELETE')).toBe(true);
    expect(w.text()).toContain('No secret yet');
  });
});
