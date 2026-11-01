/**
 * Acknowledging an incident from the Inbox: operator and up say they're
 * on an open problem, with an optional note; the item says who and when;
 * Take back undoes it. Viewers don't get the button.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import IncidentItem from '../src/components/IncidentItem.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { markWord } from '../src/model/timeline';
import { permissions } from '../src/model/roles';
import type { Incident } from '../src/model/types';
import { axeViolations } from './axe';
import { fakeFetch } from './fake-fetch';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

beforeEach(() => setDisplayTimeZone('UTC'));
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

async function settle(): Promise<void> {
  await flushPromises();
  await nextTick();
}

const PROBLEM: Incident = {
  id: 'docker:i_x/c/sonarr.down',
  class: 'problem',
  severity: 'down',
  status: 'open',
  title: 'Sonarr isn’t answering',
  summary: 'It stopped answering at 14:02.',
  target: 'container:sonarr',
  opened_at: '2026-11-01T14:02:00Z',
};

describe('acknowledging an incident', () => {
  it('only operators and up may', () => {
    expect(['owner', 'admin', 'operator'].every((r) => permissions(r).acknowledge)).toBe(true);
    expect(['viewer', 'household'].some((r) => permissions(r).acknowledge)).toBe(false);
  });

  it('Acknowledge asks for an optional note and posts it', async () => {
    const f = fakeFetch({ 'POST /api/inbox/*': { status: 200, body: { incident: PROBLEM.id, acknowledged: { by: 'owner', at: '2026-11-01T14:05:00Z' } } } });
    const h = harness(docs(withIncidents));
    const w = h.mountIn(IncidentItem, { incident: PROBLEM, expanded: true });
    await settle();
    await w.find('.ib-ack-btn').trigger('click');
    await settle();
    const input = w.find('.ib-ack-form input');
    expect(document.activeElement).toBe(input.element);
    await input.setValue('  replacing the disk ');
    expect(await axeViolations(document.body, ['region'])).toEqual([]);
    await w.find('.ib-ack-form').trigger('submit');
    await settle();
    const call = f.last('POST', `/api/inbox/${encodeURIComponent(PROBLEM.id)}/acknowledge`);
    expect(call?.body).toEqual({ note: 'replacing the disk' });
    expect(w.find('.ib-ack-form').exists()).toBe(false);
  });

  it('an acknowledged item says who and when, and can be taken back', async () => {
    const f = fakeFetch({ 'DELETE /api/inbox/*': { status: 200, body: { incident: PROBLEM.id, acknowledged: null } } });
    const h = harness(docs(withIncidents));
    const acked: Incident = { ...PROBLEM, acknowledged: { by: 'sam', at: '2026-11-01T14:05:00Z', note: 'on it' } };
    const w = h.mountIn(IncidentItem, { incident: acked, expanded: true });
    await settle();
    expect(w.find('.ib-ack').text()).toContain('Acknowledged by sam at 14:05');
    expect(w.find('.ib-ack q').text()).toBe('on it');
    expect(w.find('.ib-ack-btn').exists()).toBe(false);
    expect(await axeViolations(document.body, ['region'])).toEqual([]);
    await w.find('.ib-unack-btn').trigger('click');
    await settle();
    expect(f.last('DELETE', `/api/inbox/${encodeURIComponent(PROBLEM.id)}/acknowledge`)).toBeDefined();
  });

  it('a refusal is said where the button is', async () => {
    fakeFetch({ 'POST /api/inbox/*': { status: 409, body: { error: { code: 'already_acknowledged', message: 'Someone has acknowledged it already.' } } } });
    const h = harness(docs(withIncidents));
    const w = h.mountIn(IncidentItem, { incident: PROBLEM, expanded: true });
    await settle();
    await w.find('.ib-ack-btn').trigger('click');
    await settle();
    await w.find('.ib-ack-form').trigger('submit');
    await settle();
    expect(w.find('.ib-ack-acts [role="alert"]').text()).toBe('Someone has acknowledged it already.');
  });

  it('viewers see who is on it but get no button; resolved items say nothing of it', async () => {
    const h = harness(docs(withIncidents));
    h.session.state.user = { id: 'u2', username: 'val', role: 'viewer' };
    const acked: Incident = { ...PROBLEM, acknowledged: { by: 'sam', at: '2026-11-01T14:05:00Z' } };
    const w = h.mountIn(IncidentItem, { incident: acked, expanded: true });
    await settle();
    expect(w.find('.ib-ack').exists()).toBe(true);
    expect(w.find('.ib-ack-acts').exists()).toBe(false);
    const done = h.mountIn(IncidentItem, { incident: { ...acked, status: 'resolved', resolved_at: '2026-11-01T14:30:00Z' }, expanded: true });
    await settle();
    expect(done.find('.ib-ack').exists()).toBe(false);
  });

  it('the timeline marks acknowledgements', () => {
    const base = { at: '2026-11-01T14:05:00Z', kind: 'incident' as const, text: '', message: '' };
    expect(markWord({ ...base, event: 'acknowledged' })).toBe('acknowledged');
    expect(markWord({ ...base, event: 'unacknowledged' })).toBe('acknowledged');
  });
});
