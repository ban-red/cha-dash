/**
 * The Pulse strip and linked scrubbing: the last day's health in the
 * Briefing (GET /api/history/pulse), the page's time cursor, and sparklines
 * that mark the same moment.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import { setDisplayTimeZone } from '../src/lib/format';
import PeekBanner from '../src/components/PeekBanner.vue';
import PulseStrip from '../src/components/PulseStrip.vue';
import MiniGraph from '../src/ui/marks/MiniGraph.vue';
import Frame from '../src/widgets/parts/Frame.vue';
import { pastStatus, pulseTone, type PulseReply } from '../src/model/history';
import { clearCursor, cursor, pulse, setCursor } from '../src/stores/cursor';
import { axeViolations } from './axe';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';
import { createLive } from '../src/stores/live';

const NOW = Date.parse('2026-10-29T12:00:00Z');
const STEP = 900_000;
const START = NOW - 96 * STEP;

/** A day by the quarter hour: all well, but app:plex down 03:00–03:30 and app:arr needing attention at 03:15. */
function reply(over: Partial<PulseReply> = {}): PulseReply {
  const slots = Array.from({ length: 96 }, (_, i) => {
    const t = START + i * STEP;
    const hour = new Date(t).getUTCHours() + new Date(t).getUTCMinutes() / 60;
    if (i < 4) return { counts: {}, issues: {} }; // the hub was stopped
    if (hour >= 3 && hour < 3.5) {
      const issues: Record<string, string> = { 'app:plex': 'down' };
      if (hour === 3.25) issues['app:arr'] = 'attention';
      return { counts: { ok: 28, down: 1, ...(hour === 3.25 && { attention: 1 }) }, issues };
    }
    return { counts: { ok: 30 }, issues: {} };
  });
  return { level: 'standard', start: START, step: STEP, slots, ...over };
}

beforeEach(() => {
  setDisplayTimeZone('UTC');
  vi.useFakeTimers({ now: NOW, toFake: ['Date'] });
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  cursor.value = undefined;
  pulse.value = undefined;
  document.body.innerHTML = '';
});

describe('the Pulse strip', () => {
  it('colours each quarter hour by the worst, and scrubs by keyboard with the names of what wasn’t well', async () => {
    const net = fakeFetch({ 'GET /api/history/pulse': { status: 200, body: reply() } });
    const w = harness().mountIn(PulseStrip);
    await flushPromises();
    const q = new URL(net.calls[0]!.path, 'http://hub.test').searchParams;
    expect([q.get('from'), q.get('to'), q.get('points')]).toEqual([String(NOW - 86_400_000), String(NOW), '96']);
    const slots = w.findAll('.pulse-slot');
    expect(slots).toHaveLength(96);
    expect(slots.filter((s) => s.classes('t-down'))).toHaveLength(2);
    expect(slots.filter((s) => s.classes('t-none'))).toHaveLength(4);
    const bar = w.find('[role="slider"]');
    expect(bar.attributes('aria-valuetext')).toBe('Health over the last 24 hours: something down in 2 quarter hours');
    expect(await axeViolations(w.element.parentElement!, ['region'])).toEqual([]); // the Briefing is its landmark

    await bar.trigger('focus');
    expect(cursor.value).toBeUndefined(); // focus alone pins nothing
    await bar.trigger('keydown', { key: 'End' });
    expect(cursor.value?.source).toBe('pulse');
    expect(bar.attributes('aria-valuenow')).toBe('95');
    expect(bar.attributes('aria-valuetext')).toBe('11:45–12:00: all 30 resources healthy');
    // 03:15 is 35 quarter hours from noon yesterday.
    await bar.trigger('keydown', { key: 'Home' });
    expect(bar.attributes('aria-valuetext')).toBe('12:00–12:15: nothing watched');
    for (let i = 0; i < 61; i++) await bar.trigger('keydown', { key: 'ArrowRight' });
    expect(bar.attributes('aria-valuetext')).toBe('03:15–03:30: app:plex down, app:arr needs attention');
    expect(w.find('.pulse-read').text()).toBe('03:15–03:30: app:plex down, app:arr needs attention');
    expect(pastStatus(pulse.value, 'app:plex', cursor.value!.at)).toBe('down');
    expect(pastStatus(pulse.value, 'app:other', cursor.value!.at)).toBe('ok');
    await bar.trigger('keydown', { key: 'Escape' });
    expect(cursor.value).toBeUndefined();
    expect(w.find('.pulse-read').exists()).toBe(false);
  });

  it('hovering reads the strip alone; a click pins the moment and a second click lets go', async () => {
    fakeFetch({ 'GET /api/history/pulse': { status: 200, body: reply() } });
    const w = harness().mountIn(PulseStrip);
    await flushPromises();
    const bar = w.find('[role="slider"]');
    const box = { left: 0, top: 0, width: 960, height: 10, right: 960, bottom: 10, x: 0, y: 0, toJSON: () => ({}) };
    bar.element.getBoundingClientRect = () => box as DOMRect;
    // Slot 61 is 03:15.
    await bar.trigger('pointermove', { clientX: 615 });
    expect(w.find('.pulse-read').text()).toBe('03:15–03:30: app:plex down, app:arr needs attention');
    expect(cursor.value).toBeUndefined();
    await bar.trigger('pointerdown', { clientX: 615 });
    await bar.trigger('pointerup');
    expect(cursor.value?.source).toBe('pulse');
    expect(pastStatus(pulse.value, 'app:plex', cursor.value!.at)).toBe('down');
    expect(w.findAll('.pulse-slot.pin')).toHaveLength(1);
    // The pin outlasts the pointer.
    await bar.trigger('pointerleave');
    expect(cursor.value?.source).toBe('pulse');
    expect(w.find('.pulse-read').text()).toBe('03:15–03:30: app:plex down, app:arr needs attention');
    // Dragging moves it; a click on the pinned slot lets go.
    await bar.trigger('pointerdown', { clientX: 615 });
    await bar.trigger('pointermove', { clientX: 955 });
    await bar.trigger('pointerup');
    expect(bar.attributes('aria-valuenow')).toBe('95');
    await bar.trigger('pointerdown', { clientX: 955 });
    await bar.trigger('pointerup');
    expect(cursor.value).toBeUndefined();
    await bar.trigger('pointerleave');
    expect(w.find('.pulse-read').exists()).toBe(false);
  });

  it('the Peek banner says the board shows the past, and goes back to now', async () => {
    pulse.value = reply();
    const w = mount(PeekBanner, { attachTo: document.body });
    expect(w.find('.peek').exists()).toBe(false);
    setCursor(Date.parse('2026-10-29T03:20:00Z'), 'pulse');
    await flushPromises();
    expect(w.find('.peek').text()).toContain('Peeking at 03:15–03:30');
    expect(w.find('.peek').text()).toContain('9 h ago');
    expect(w.find('.peek-frame').exists()).toBe(true);
    expect(await axeViolations(document.body, ['region'])).toEqual([]);
    await w.find('.peek button').trigger('click');
    expect(cursor.value).toBeUndefined();
    // Esc and a click elsewhere go back too; a sparkline's moment isn't a peek.
    setCursor(Date.parse('2026-10-29T03:20:00Z'), 'pulse');
    await flushPromises();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(cursor.value).toBeUndefined();
    setCursor(Date.parse('2026-10-29T03:20:00Z'), 'pulse');
    await flushPromises();
    document.body.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }));
    expect(cursor.value).toBeUndefined();
    setCursor(Date.parse('2026-10-29T03:20:00Z'), {});
    await flushPromises();
    expect(w.find('.peek').exists()).toBe(false);
    w.unmount();
  });

  it('hides while the hub keeps no history', async () => {
    fakeFetch({ 'GET /api/history/pulse': { status: 200, body: { level: 'off', start: NOW, step: 0, slots: [] } } });
    const w = harness().mountIn(PulseStrip);
    await flushPromises();
    expect(w.find('.pulse').exists()).toBe(false);
  });

  it('says what a slot is: nothing watched, all well, or the worst', () => {
    expect(pulseTone({ counts: {}, issues: {} })).toBe('none');
    expect(pulseTone({ counts: { ok: 3, repair: 1 }, issues: { a: 'repair' } })).toBe('attention');
    expect(pulseTone({ counts: { ok: 3, attention: 1, down: 1 }, issues: {} })).toBe('down');
    expect(pastStatus(reply(), 'app:plex', START)).toBeUndefined(); // the hub was stopped
    expect(pastStatus(reply(), 'app:plex', NOW + STEP)).toBeUndefined(); // after the range
  });
});

describe('linked scrubbing', () => {
  const values = Array.from({ length: 48 }, (_, i) => i);
  it('hovering a sparkline reads it alone', async () => {
    const a = mount(MiniGraph, { props: { values, label: 'A' }, attachTo: document.body });
    const b = mount(MiniGraph, { props: { values, label: 'B' }, attachTo: document.body });
    const box = { left: 0, top: 0, width: 470, height: 30, right: 470, bottom: 30, x: 0, y: 0, toJSON: () => ({}) };
    a.element.getBoundingClientRect = () => box as DOMRect;
    await a.trigger('pointermove', { clientX: 240 });
    expect(a.find('.mg-read').text()).toBe('24 12 h ago');
    expect(cursor.value).toBeUndefined();
    expect(b.find('.mg-read').exists()).toBe(false);
    await a.trigger('pointerleave');
  });

  it('a sparkline pressed and dragged moves the cursor, and the others mark the same moment', async () => {
    const a = mount(MiniGraph, { props: { values, label: 'A' }, attachTo: document.body });
    const b = mount(MiniGraph, { props: { values: values.map((v) => v * 2), label: 'B' }, attachTo: document.body });
    const box = { left: 0, top: 0, width: 470, height: 30, right: 470, bottom: 30, x: 0, y: 0, toJSON: () => ({}) };
    a.element.getBoundingClientRect = () => box as DOMRect;
    // Point 24 of 48 over 24 hours: 23 half hours ago.
    await a.trigger('pointerdown', { clientX: 0 });
    await a.trigger('pointermove', { clientX: 240 });
    expect(cursor.value!.at).toBe(NOW - 23 * 30 * 60_000);
    expect(b.find('.mg-read').text()).toBe('48 12 h ago');
    expect(a.find('.mg-read').text()).toBe('24 12 h ago');
    await a.trigger('pointerup');
    expect(cursor.value).toBeUndefined();
    expect(b.find('.mg-read').exists()).toBe(false);
    expect(a.find('.mg-read').exists()).toBe(true); // still hovered
    await a.trigger('pointerleave');
    // The Pulse strip's moment, too; a graph with its own times (a sheet's range) isn't linked.
    const own = mount(MiniGraph, { props: { values, label: 'C', at: () => 'then' }, attachTo: document.body });
    setCursor(NOW - 60 * 60_000, 'pulse');
    await b.vm.$nextTick();
    expect(b.find('.mg-read').text()).toBe('90 60 min ago');
    expect(own.find('.mg-read').exists()).toBe(false);
    clearCursor('pulse');
  });
});

describe('time travel', () => {
  it('while peeking, numbers come from each series’ history; the rest stays as now', () => {
    const live = createLive();
    // 48 points over the day, half an hour apart; the newest is now.
    const history = Array.from({ length: 48 }, (_, i) => (i === 30 ? null : i));
    live.doc.metrics = { series: { 'app:plex|streams': { value: 99, history, unit: '' }, 'app:plex|cost': { value: 7 } } };
    expect(live.metrics.value!.series['app:plex|streams']!.value).toBe(99);
    setCursor(NOW - 10 * 30 * 60_000, 'pulse'); // point 37
    expect(live.metrics.value!.series['app:plex|streams']!.value).toBe(37);
    expect(live.metrics.value!.series['app:plex|streams']!.history).toBe(live.doc.metrics.series['app:plex|streams']!.history);
    expect(live.metrics.value!.series['app:plex|cost']!.value).toBe(7); // keeps no history: stays live
    expect(Object.keys(live.metrics.value!.series)).toEqual(['app:plex|streams', 'app:plex|cost']);
    setCursor(NOW - 17 * 30 * 60_000, 'pulse'); // point 30 is a gap: the nearest known
    expect(live.metrics.value!.series['app:plex|streams']!.value).toBe(29);
    setCursor(NOW - 10 * 30 * 60_000, {}); // a sparkline's moment isn't a peek
    expect(live.metrics.value!.series['app:plex|streams']!.value).toBe(99);
  });

  it('a tile shows its health at the moment the Pulse strip is on, and only then', async () => {
    pulse.value = reply();
    const subject = { resource: { id: 'app:plex', kind: 'app', name: 'Plex' }, health: { status: 'ok' }, status: 'ok', needsRepair: false };
    const w = harness().mountIn(Frame, { size: 'small', name: 'Plex', subject });
    await flushPromises();
    const tile = () => w.find('.tile');
    expect(tile().classes('is-down')).toBe(false);
    const at = Date.parse('2026-10-29T03:20:00Z');
    setCursor(at, 'pulse');
    await flushPromises();
    expect(tile().classes('is-down')).toBe(true);
    expect(tile().text()).toContain('down at 03:20');
    // A sparkline's moment marks the graphs, not the tiles.
    setCursor(at, {});
    await flushPromises();
    expect(tile().classes('is-down')).toBe(false);
    setCursor(Date.parse('2026-10-29T09:00:00Z'), 'pulse');
    await flushPromises();
    expect(tile().text()).toContain('healthy at 09:00');
  });
});

