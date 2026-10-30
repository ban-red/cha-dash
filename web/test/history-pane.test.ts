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

const H = 3_600_000;
const D = 24 * H;
/** As the hub sends them: what each keeps, the level's own, and the choices. */
const tier = (t: { tier: string; step_ms: number; keep_ms: number; chunks: number; bytes: number; oldest: number | null }, choices: number[]) => ({
  ...t,
  default_keep_ms: t.keep_ms,
  custom: false,
  choices_ms: choices,
});
const tiers = [
  tier({ tier: '1m', step_ms: 60_000, keep_ms: 48 * H, chunks: 38_976, bytes: 9_800_000, oldest: Date.parse('2026-10-01T14:00:00Z') }, [6 * H, 12 * H, D, 2 * D, 7 * D, 14 * D]),
  tier({ tier: '15m', step_ms: 900_000, keep_ms: 14 * D, chunks: 9_744, bytes: 5_100_000, oldest: Date.parse('2026-09-28T12:00:00Z') }, [2 * D, 7 * D, 14 * D, 30 * D, 90 * D, 180 * D, 365 * D]),
  tier({ tier: '1h', step_ms: 3_600_000, keep_ms: 365 * D, chunks: 0, bytes: 0, oldest: null }, [30 * D, 90 * D, 180 * D, 365 * D, 730 * D, 1825 * D]),
  tier({ tier: '1d', step_ms: 86_400_000, keep_ms: 0, chunks: 0, bytes: 0, oldest: null }, [365 * D, 730 * D, 1825 * D, 3650 * D, 0]),
];
/** A row's cells as shown, a select by its chosen option. */
const cells = (r: { findAll: (s: string) => { element: Element; text: () => string }[] }) =>
  r.findAll('th, td').map((c) => {
    const sel = c.element.querySelector('select');
    return sel ? sel.options[sel.selectedIndex]!.text : c.text();
  });
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
  instances: [
    { id: 'i_pve', series: 412, bytes: 9_100_000 },
    { id: 'i_arr', series: 60, bytes: 800_000 },
    { id: 'i_gone', series: 3, bytes: 20_000 },
  ],
  excluded: [] as string[],
  exclude_keys: [] as { pattern: string; series: number }[],
  estimates: { standard: 28_000_000, 'long-term': 101_000_000 },
  ...over,
});

beforeEach(() => setDisplayTimeZone('UTC'));
/** The connections Settings → Integrations lists; Docker has recorded nothing yet. */
const INSTANCES = {
  status: 200,
  body: {
    instances: [
      { id: 'i_pve', integration: 'proxmox', name: 'Proxmox', status: 'ok' },
      { id: 'i_arr', integration: 'sonarr', name: 'Sonarr', status: 'ok' },
      { id: 'i_dock', integration: 'docker', name: 'Docker', status: 'ok' },
    ],
  },
};

afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('Settings → History', () => {
  it('offers the levels with what each grows to, and what’s on disk', async () => {
    fakeFetch({ 'GET /api/integrations/instances': INSTANCES, 'GET /api/settings/history': { status: 200, body: view() } });
    const w = harness().mountIn(HistoryPane);
    await flushPromises();
    const cards = w.findAll('label.sl-card');
    expect(cards.map((c) => c.find('.sl-label').text())).toEqual(['Standarddefault', 'Long-term', 'Off']);
    expect(w.findAll<HTMLInputElement>('input[type="radio"]').map((r) => r.element.checked)).toEqual([true, false, false]);
    expect(cards[0]!.find('.sl-desc').text()).toContain('About 28 MB at 812 series.');
    expect(cards[1]!.find('.sl-desc').text()).toContain('About 101 MB at 812 series, after two years.');
    expect(w.find('#hp-disk-h + .st-lede, .st-section:nth-of-type(2) .st-lede').text()).toContain('21 MB in history.db: 812 series and the health of 240 resources.');
    // Standard shows its two levels of detail; the long-term ones appear when they hold something.
    const rows = w.findAll('.hp-tiers tbody tr').map(cells);
    expect(rows).toEqual([
      ['Each minute', '48 hours (level’s)', 'Thu 1 Oct', '10 MB'],
      ['Each quarter hour', '14 days (level’s)', 'Mon 28 Sept', '5 MB'],
    ]);
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
  });

  it('saves a level on Save, not on choosing it', async () => {
    let saved: unknown;
    const f = fakeFetch({
      'GET /api/integrations/instances': INSTANCES,
      'GET /api/settings/history': { status: 200, body: view() },
      'PUT /api/settings/history': (body) => {
        saved = body;
        return { status: 200, body: view({ level: 'long-term', tiers: tiers.map((t) => (t.tier === '15m' ? { ...t, keep_ms: 30 * D, default_keep_ms: 30 * D } : t)) }) };
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
    expect(w.findAll('.hp-tiers tbody tr').map((r) => cells(r)[1])).toEqual(['48 hours (level’s)', '30 days (level’s)', '1 year (level’s)', 'forever (level’s)']);
    expect(w.find('button[aria-label="Save history level"]').attributes('aria-disabled')).toBe('true');
  });

  it('clears only after asking', async () => {
    let cleared = 0;
    fakeFetch({
      'GET /api/integrations/instances': INSTANCES,
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
    fakeFetch({ 'GET /api/integrations/instances': INSTANCES, 'GET /api/settings/history': { status: 200, body: view({ level: 'off', locked: true, tiers: [], estimates: {} }) } });
    const w = harness().mountIn(HistoryPane);
    await flushPromises();
    expect(w.find('[role="note"]').text()).toContain('CHADASH_HISTORY=off');
    expect(w.find('fieldset').exists()).toBe(false);
    expect(w.find('.hp-tiers').exists()).toBe(false);
    fakeFetch({ 'GET /api/integrations/instances': INSTANCES, 'GET /api/settings/history': { status: 200, body: view({ level: 'off', locked: true, demo: true, tiers: [], estimates: {} }) } });
    const d = harness().mountIn(HistoryPane);
    await flushPromises();
    expect(d.find('[role="note"]').text()).toContain('The Demo Home makes its own history');
  });

  it('sets a size limit on its own Save, and says when it last trimmed', async () => {
    let sent: unknown;
    fakeFetch({
      'GET /api/integrations/instances': INSTANCES,
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

  it('lists every integration with what it recorded, leaves one out on Save, and clears one after asking', async () => {
    let sent: unknown;
    let cleared = '';
    const f = fakeFetch({
      'GET /api/integrations/instances': INSTANCES,
      'GET /api/settings/history': { status: 200, body: view() },
      'PUT /api/settings/history': (body) => {
        sent = body;
        return { status: 200, body: view({ excluded: ['i_arr'] }) };
      },
      'POST /api/settings/history/clear': (_b, url) => {
        cleared = url.searchParams.get('instance') ?? 'all';
        return { status: 200, body: view({ instances: [view().instances[1], view().instances[2]] }) };
      },
    });
    const w = harness().mountIn(HistoryPane);
    await flushPromises();
    const rows = () => w.findAll('.hp-ints tbody tr').filter((r) => r.find('th').exists()).map((r) => r.findAll('th, td').slice(0, 3).map((c) => c.text()));
    // Most series first; a connection with nothing yet, and history from a removed one, are listed too.
    expect(rows()).toEqual([
      ['Proxmox', '412', '9 MB'],
      ['Sonarr', '60', '800 kB'],
      ['Removed (i_gone)', '3', '20 kB'],
      ['Docker', '0', '—'],
    ]);
    const box = (name: string) => w.find<HTMLInputElement>(`input[aria-label="Record ${name}’s metrics"]`);
    expect(box('Sonarr').element.checked).toBe(true);
    const save = w.find('button[aria-label="Save what’s recorded"]');
    expect(save.attributes('aria-disabled')).toBe('true');
    await box('Sonarr').setValue(false);
    expect(sent).toBeUndefined();
    expect(save.attributes('aria-disabled')).toBeUndefined();
    await save.trigger('click');
    await flushPromises();
    expect(sent).toEqual({ exclude: ['i_arr'] });
    expect(box('Sonarr').element.checked).toBe(false);

    await w.find('button[aria-label="Clear Proxmox’s history…"]').trigger('click');
    const ask = w.find('[role="alertdialog"]');
    expect(ask.text()).toContain('Delete the 412 series Proxmox recorded? Its health history stays.');
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
    expect(f.calls.some((c) => c.method === 'POST')).toBe(false);
    await ask.findAll('button').find((b) => b.text().startsWith('Clear'))!.trigger('click');
    await flushPromises();
    expect(cleared).toBe('i_pve');
    expect(w.find('[role="alertdialog"]').exists()).toBe(false);
  });

  it('keeps each level of detail as chosen, on Save, and refuses coarser points kept for less', async () => {
    let sent: unknown;
    fakeFetch({
      'GET /api/integrations/instances': INSTANCES,
      'GET /api/settings/history': { status: 200, body: view() },
      'PUT /api/settings/history': (body) => {
        sent = body;
        return { status: 200, body: view({ tiers: tiers.map((t) => (t.tier === '1m' ? { ...t, keep_ms: 7 * D, custom: true } : t)) }) };
      },
    });
    const w = harness().mountIn(HistoryPane);
    await flushPromises();
    const save = w.find('button[aria-label="Save how long each level of detail is kept"]');
    expect(save.attributes('aria-disabled')).toBe('true');
    const minute = w.find<HTMLSelectElement>('select[aria-label="Keep each minute points for"]');
    expect([...minute.element.options].map((o) => o.text)).toEqual(['48 hours (level’s)', '6 hours', '12 hours', '24 hours', '48 hours', '7 days', '14 days']);
    // Fourteen days of minutes against seven of quarter hours: not allowed.
    await w.find('select[aria-label="Keep each quarter hour points for"]').setValue(String(7 * D));
    await minute.setValue(String(14 * D));
    expect(w.find('[role="alert"]').text()).toBe('Each quarter hour can’t be kept for less time than each minute.');
    await save.trigger('click');
    await flushPromises();
    expect(sent).toBeUndefined();
    await w.find('select[aria-label="Keep each quarter hour points for"]').setValue('-1');
    await minute.setValue(String(7 * D));
    await save.trigger('click');
    await flushPromises();
    expect(sent).toEqual({ keep: { '1m': 7 * D, '15m': null, '1h': null, '1d': null } });
    expect(cells(w.findAll('.hp-tiers tbody tr')[0]!)[1]).toBe('7 days');
    expect(save.attributes('aria-disabled')).toBe('true');
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
  });

  it('leaves single metrics out by pattern, and records them again', async () => {
    const sent: unknown[] = [];
    fakeFetch({
      'GET /api/integrations/instances': INSTANCES,
      'GET /api/settings/history': { status: 200, body: view() },
      'PUT /api/settings/history': (body) => {
        sent.push(body);
        const keys = (body as { exclude_keys: string[] }).exclude_keys;
        return { status: 200, body: view({ exclude_keys: keys.map((p) => ({ pattern: p, series: 12 })) }) };
      },
    });
    const w = harness().mountIn(HistoryPane);
    await flushPromises();
    await w.find('#hp-key').setValue('  docker:*#net_rx ');
    await w.find('#hp-key').element.closest('form')!.dispatchEvent(new Event('submit'));
    await flushPromises();
    expect(sent).toEqual([{ exclude_keys: ['docker:*#net_rx'] }]);
    expect(w.find('.hp-keys li').text()).toContain('docker:*#net_rx');
    expect(w.find('.hp-keys li').text()).toContain('12 series');
    expect(w.find<HTMLInputElement>('#hp-key').element.value).toBe('');
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
    await w.find('button[aria-label="Record docker:*#net_rx again"]').trigger('click');
    await flushPromises();
    expect(sent[1]).toEqual({ exclude_keys: [] });
    expect(w.find('.hp-keys').exists()).toBe(false);
  });
});
