/**
 * Settings → History: the recording level, what history.db holds, and
 * clearing it (GET/PUT /api/settings/history, POST …/clear).
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { setDisplayTimeZone } from '../src/lib/format';
import HistoryPane from '../src/settings/HistoryPane.vue';
import { axeViolations } from './axe';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

const tiers = [
  { tier: '1m', step_ms: 60_000, keep_ms: 172_800_000, chunks: 38_976, bytes: 9_800_000, oldest: Date.parse('2026-10-01T14:00:00Z') },
  { tier: '15m', step_ms: 900_000, keep_ms: 1_209_600_000, chunks: 9_744, bytes: 5_100_000, oldest: Date.parse('2026-09-28T12:00:00Z') },
  { tier: '1h', step_ms: 3_600_000, keep_ms: 31_536_000_000, chunks: 0, bytes: 0, oldest: null },
  { tier: '1d', step_ms: 86_400_000, keep_ms: 0, chunks: 0, bytes: 0, oldest: null },
];
const view = (over: Record<string, unknown> = {}) => ({
  level: 'standard',
  locked: false,
  demo: false,
  series: 812,
  resources: 240,
  file_bytes: 21_400_000,
  max_bytes: 1e9,
  trimmed_at: null,
  tiers,
  estimates: { standard: 28_000_000, 'long-term': 101_000_000 },
  ...over,
});

beforeEach(() => setDisplayTimeZone('UTC'));
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('Settings → History', () => {
  it('offers the levels with what each grows to, and what’s on disk', async () => {
    fakeFetch({ 'GET /api/settings/history': { status: 200, body: view() } });
    const w = harness().mountIn(HistoryPane);
    await flushPromises();
    const cards = w.findAll('label.sl-card');
    expect(cards.map((c) => c.find('.sl-label').text())).toEqual(['Standarddefault', 'Long-term', 'Off']);
    expect(w.findAll<HTMLInputElement>('input[type="radio"]').map((r) => r.element.checked)).toEqual([true, false, false]);
    expect(cards[0]!.find('.sl-desc').text()).toContain('About 28 MB at 812 series.');
    expect(cards[1]!.find('.sl-desc').text()).toContain('About 101 MB at 812 series, after two years.');
    expect(w.find('#hp-disk-h + .st-lede, .st-section:nth-of-type(2) .st-lede').text()).toContain('21 MB in history.db: 812 series and the health of 240 resources.');
    // Standard shows its two levels of detail; the long-term ones appear when they hold something.
    const rows = w.findAll('.hp-tiers tbody tr').map((r) => r.findAll('th, td').map((c) => c.text()));
    expect(rows).toEqual([
      ['Each minute', '48 hours', 'Thu 1 Oct', '10 MB'],
      ['Each quarter hour', '14 days', 'Mon 28 Sept', '5 MB'],
    ]);
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
  });

  it('saves a level on Save, not on choosing it', async () => {
    let saved: unknown;
    const f = fakeFetch({
      'GET /api/settings/history': { status: 200, body: view() },
      'PUT /api/settings/history': (body) => {
        saved = body;
        return { status: 200, body: view({ level: 'long-term', tiers: tiers.map((t) => (t.tier === '15m' ? { ...t, keep_ms: 2_592_000_000 } : t)) }) };
      },
    });
    const h = harness();
    const w = h.mountIn(HistoryPane);
    await flushPromises();
    await w.findAll('input[type="radio"]')[1]!.setValue(true);
    expect(f.calls.some((c) => c.method === 'PUT')).toBe(false);
    await w.find('button[aria-label="Save history level"]').trigger('click');
    await flushPromises();
    expect(saved).toEqual({ level: 'long-term' });
    // Long-term lists every level of detail, with what each keeps.
    expect(w.findAll('.hp-tiers tbody th').map((t) => t.text())).toEqual(['Each minute', 'Each quarter hour', 'Each hour', 'Each day']);
    expect(w.findAll('.hp-tiers tbody tr').map((r) => r.findAll('td')[0]!.text())).toEqual(['48 hours', '30 days', '1 year', 'forever']);
    expect(w.find('button[aria-label="Save history level"]').attributes('aria-disabled')).toBe('true');
  });

  it('clears only after asking', async () => {
    let cleared = 0;
    fakeFetch({
      'GET /api/settings/history': { status: 200, body: view() },
      'POST /api/settings/history/clear': () => {
        cleared++;
        return { status: 200, body: view({ series: 0, resources: 0, file_bytes: 40_960, tiers: tiers.map((t) => ({ ...t, chunks: 0, bytes: 0, oldest: null })) }) };
      },
    });
    const w = harness().mountIn(HistoryPane);
    await flushPromises();
    await w.findAll('button').find((b) => b.text() === 'Clear history…')!.trigger('click');
    const ask = w.find('[role="alertdialog"]');
    expect(ask.text()).toContain('This can’t be undone.');
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
    expect(cleared).toBe(0);
    await ask.findAll('button').find((b) => b.text().startsWith('Clear history'))!.trigger('click');
    await flushPromises();
    expect(cleared).toBe(1);
    expect(w.find('[role="alertdialog"]').exists()).toBe(false);
    expect(w.findAll('button').find((b) => b.text() === 'Clear history…')!.attributes('disabled')).toBeDefined();
  });

  it('says why there’s nothing to choose when the hub keeps no history', async () => {
    fakeFetch({ 'GET /api/settings/history': { status: 200, body: view({ level: 'off', locked: true, tiers: [], estimates: {} }) } });
    const w = harness().mountIn(HistoryPane);
    await flushPromises();
    expect(w.find('[role="note"]').text()).toContain('CHADASH_HISTORY=off');
    expect(w.find('fieldset').exists()).toBe(false);
    expect(w.find('.hp-tiers').exists()).toBe(false);
    fakeFetch({ 'GET /api/settings/history': { status: 200, body: view({ level: 'off', locked: true, demo: true, tiers: [], estimates: {} }) } });
    const d = harness().mountIn(HistoryPane);
    await flushPromises();
    expect(d.find('[role="note"]').text()).toContain('The Demo Home makes its own history');
  });

  it('sets a size limit on its own Save, and says when it last trimmed', async () => {
    let sent: unknown;
    fakeFetch({
      'GET /api/settings/history': { status: 200, body: view({ trimmed_at: Date.parse('2026-10-20T03:00:00Z') }) },
      'PUT /api/settings/history': (body) => {
        sent = body;
        return { status: 200, body: view({ max_bytes: 512e6 }) };
      },
    });
    const w = harness().mountIn(HistoryPane);
    await flushPromises();
    const select = w.find<HTMLSelectElement>('#hp-limit');
    expect(select.findAll('option').map((o) => o.text())).toEqual(['256 MB', '512 MB', '1 GB', '2 GB', '5 GB', '10 GB', 'No limit']);
    expect(select.element.value).toBe(String(1e9));
    expect(w.find('#hp-limit-d').text()).toMatch(/are kept\. It last did on Tue 20 Oct\.$/);
    await select.setValue(String(512e6));
    expect(sent).toBeUndefined();
    await w.find('button[aria-label="Save size limit"]').trigger('click');
    await flushPromises();
    expect(sent).toEqual({ max_bytes: 512e6 });
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
  });
});
