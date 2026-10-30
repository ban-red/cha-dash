/**
 * Hiding a warning until it turns critical (stores/muted.ts): out of Needs
 * attention and the briefing, back when it goes down, undoable, remembered.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import NeedsAttention from '../src/components/NeedsAttention.vue';
import { summarize } from '../src/model/briefing';
import type { Incident } from '../src/model/types';
import { canMute, isMarked, isMuted, MUTED_KEY, mute, resetMuted, unmute } from '../src/stores/muted';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] });
  vi.setSystemTime(new Date('2026-10-03T14:05:10Z'));
  localStorage.clear();
  resetMuted();
});
afterEach(() => {
  vi.useRealTimers();
  document.body.innerHTML = '';
});

const warning = (over: Partial<Incident> = {}): Incident => ({
  id: 'i1',
  class: 'problem',
  severity: 'attention',
  status: 'open',
  title: 'PBS datastore is filling up',
  summary: '',
  target: 'pbs:datastore',
  opened_at: '2026-10-03T02:44:00Z',
  ...over,
});

describe('the hidden set', () => {
  it('hides a warning, shows it again when it turns critical, and hides it once it eases', () => {
    const w = warning();
    expect(canMute(w)).toBe(true);
    mute(w);
    expect(isMuted(w)).toBe(true);
    const down = warning({ severity: 'down' });
    expect(isMuted(down)).toBe(false);
    expect(isMarked(down)).toBe(true);
    expect(isMuted(warning())).toBe(true);
    unmute(w);
    expect(isMuted(w)).toBe(false);
  });

  it('only warnings can be hidden', () => {
    expect(canMute(warning({ severity: 'down' }))).toBe(false);
    expect(canMute(warning({ class: 'repair' }))).toBe(false);
    expect(canMute(warning({ severity: 'info' }))).toBe(false);
  });

  it('is remembered on this device', () => {
    mute(warning());
    expect(JSON.parse(localStorage.getItem(MUTED_KEY)!)).toHaveLength(1);
    resetMuted();
    expect(isMuted(warning())).toBe(true);
  });
});

describe('Needs attention', () => {
  const pbs = (w: ReturnType<ReturnType<typeof harness>['mountIn']>) => w.findAll('.strip').find((s) => s.text().includes('PBS datastore is filling up'));

  it('hides a warning with Hide, with an Undo, and keeps critical problems unhideable', async () => {
    const h = harness(docs(withIncidents));
    const w = h.mountIn(NeedsAttention);
    await w.find('.attn-more').trigger('click');
    expect(w.find('.sh').text()).toContain('Needs attention · 9');
    const strips = w.findAll('.strip');
    expect(strips[0]!.findAll('.acts button').map((b) => b.text())).not.toContain('Hide'); // down: Nextcloud
    const hide = pbs(w)!.findAll('.acts button').find((b) => b.text() === 'Hide')!;
    await hide.trigger('click');
    expect(pbs(w)).toBeUndefined();
    expect(w.find('.sh').text()).toContain('Needs attention · 8');
    const toast = h.ui.state.toasts.at(-1)!;
    expect(toast.text).toContain('Hidden until it turns critical');
    toast.action!.run();
    await w.vm.$nextTick();
    expect(pbs(w)).toBeDefined();
  });

  it('keeps hidden warnings out of the briefing', () => {
    const d = docs(withIncidents);
    const all = summarize(d.graph, d.metrics, d.inbox, Date.now());
    const target = all.problems.find((p) => canMute(p))!;
    const some = summarize(d.graph, d.metrics, d.inbox, Date.now(), (i) => i.id === target.id);
    expect(some.problems).toHaveLength(all.problems.length - 1);
  });
});
