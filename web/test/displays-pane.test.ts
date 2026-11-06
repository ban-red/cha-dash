/**
 * Settings → Displays: list, make a display and show its link once with
 * Copy, rename and set quiet hours, revoke with a confirmation; admin and
 * owner only. The hub's side is faked to the wave 33 contract.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import DisplaysPane from '../src/settings/DisplaysPane.vue';
import SettingsSheet from '../src/settings/SettingsSheet.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { navigate } from '../src/lib/router';
import { permissions } from '../src/model/roles';
import { axeViolations } from './axe';
import { fakeFetch, type Handler } from './fake-fetch';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

const SECRET = 'cdd_aaaaaaaaaaaaaaaaaaaaaaaaaa_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb';
const T = Date.parse('2026-10-03T14:00:00Z');

const DISPLAYS = [
  { id: 'dsp_hall', name: 'Hall tablet', board: { id: 'brd_home', name: 'Home' }, quiet: { start: '22:00', end: '07:00' }, created_at: T - 86_400_000, created_by: 'sam', last_seen_at: T - 60_000, last_seen_ip: '192.0.2.40' },
  { id: 'dsp_tv', name: 'Kitchen TV', board: { id: 'brd_media', name: 'Media' }, quiet: null, created_at: T - 3_600_000, created_by: 'owner', last_seen_at: null, last_seen_ip: null },
];
const BOARDS = [
  { id: 'brd_home', name: 'Home' },
  { id: 'brd_media', name: 'Media' },
];

function hub(over: Record<string, Handler | { status: number; body?: unknown }> = {}) {
  return fakeFetch({
    'GET /api/displays': { status: 200, body: { displays: DISPLAYS } },
    'GET /api/boards': { status: 200, body: { boards: BOARDS } },
    'POST /api/displays': (b) => {
      const body = b as { name: string; board: string; quiet: unknown };
      return { status: 201, body: { display: { id: 'dsp_new', name: body.name, board: BOARDS.find((x) => x.id === body.board), quiet: body.quiet, created_at: T, created_by: 'owner', last_seen_at: null, last_seen_ip: null }, secret: SECRET } };
    },
    'PATCH /api/displays/dsp_tv': (b) => ({ status: 200, body: { display: { ...DISPLAYS[1], ...(b as object) } } }),
    'DELETE /api/displays/dsp_hall': { status: 204 },
    ...over,
  });
}

async function settle(): Promise<void> {
  await vi.dynamicImportSettled();
  await flushPromises();
  await nextTick();
}

const button = (w: { findAll: (s: string) => { text: () => string; trigger: (e: string) => Promise<void> }[] }, text: string) => w.findAll('button').find((b) => b.text() === text)!;

beforeEach(() => {
  setDisplayTimeZone('UTC');
  navigate('/', { replace: true });
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('Settings → Displays', () => {
  it('lists displays with their board, quiet hours and when they were last seen', async () => {
    hub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(DisplaysPane);
    await settle();
    const rows = w.findAll('.dpl-row');
    expect(rows).toHaveLength(2);
    expect(rows[0]!.text()).toContain('Hall tablet');
    expect(rows[0]!.text()).toContain('Home');
    expect(rows[0]!.text()).toContain('quiet 22:00–07:00');
    expect(rows[0]!.text()).toContain('by sam');
    expect(rows[0]!.text()).toContain('from 192.0.2.40');
    expect(rows[1]!.text()).toContain('no quiet hours');
    expect(rows[1]!.text()).toContain('never opened');
    expect(w.text()).not.toContain('cdd_');
    expect(await axeViolations()).toEqual([]);
  });

  it('makes a display and shows its link once, with Copy', async () => {
    const f = hub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(DisplaysPane);
    await settle();
    await button(w, 'New display').trigger('click');
    await settle();
    expect(await axeViolations()).toEqual([]);
    // A name first.
    await w.find('form.dpl-add').trigger('submit');
    await settle();
    expect(w.find('.dpl-add').text()).toContain('Name it after where it hangs');
    expect(f.last('POST', '/api/displays')).toBeUndefined();

    await w.find('input[type="text"]').setValue('Landing');
    await w.findAll('select')[0]!.setValue('brd_media');
    await w.findAll('select')[1]!.setValue('tv');
    await w.find('input[type="checkbox"]').setValue(true);
    await settle();
    const times = w.findAll('input[type="time"]');
    await times[0]!.setValue('23:00');
    await times[1]!.setValue('06:30');
    await w.find('form.dpl-add').trigger('submit');
    await settle();
    expect(f.last('POST', '/api/displays')?.body).toEqual({ name: 'Landing', board: 'brd_media', style: 'tv', quiet: { start: '23:00', end: '06:30' } });
    const link = `${location.origin}/display/brd_media#${SECRET}`;
    expect(w.find<HTMLInputElement>('.dpl-fresh input').element.value).toBe(link);
    expect(w.text()).toContain('This is the only time Cha Dash shows it');
    expect(await axeViolations()).toEqual([]);

    const writeText = vi.fn(async () => undefined);
    Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true });
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } });
    await button(w, 'Copy').trigger('click');
    await settle();
    expect(writeText).toHaveBeenCalledWith(link);
    expect(w.findAll('button').some((b) => b.text() === 'Copied')).toBe(true);
    await button(w, 'Done').trigger('click');
    await settle();
    expect(w.text()).not.toContain(SECRET);
    expect(w.findAll('.dpl-row')).toHaveLength(3);
    Object.defineProperty(window, 'isSecureContext', { value: undefined, configurable: true });
  });

  it('sends no quiet hours when the box is off, and says why the hub refused', async () => {
    const f = hub({ 'POST /api/displays': { status: 400, body: { error: { code: 'bad_board', message: 'That board no longer exists.' } } } });
    const h = harness(docs(withIncidents));
    const w = h.mountIn(DisplaysPane);
    await settle();
    await button(w, 'New display').trigger('click');
    await settle();
    await w.find('input[type="text"]').setValue('Hall');
    await w.find('form.dpl-add').trigger('submit');
    await settle();
    expect(f.last('POST', '/api/displays')?.body).toEqual({ name: 'Hall', board: 'brd_home', style: 'wall', quiet: null });
    expect(w.find('.dpl-add .err').text()).toBe('That board no longer exists.');
  });

  it('renames a display and sets its quiet hours, sending only what changed', async () => {
    const f = hub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(DisplaysPane);
    await settle();
    await w.find('button[aria-label="Edit Kitchen TV"]').trigger('click');
    await settle();
    const form = w.find('form.dpl-edit');
    await form.find('input[type="text"]').setValue('Kitchen');
    await form.find('input[type="checkbox"]').setValue(true);
    await settle();
    expect(await axeViolations()).toEqual([]);
    await w.find('form.dpl-edit').trigger('submit');
    await settle();
    expect(f.last('PATCH', '/api/displays/dsp_tv')?.body).toEqual({ name: 'Kitchen', quiet: { start: '22:00', end: '07:00' } });
    expect(w.find('form.dpl-edit').exists()).toBe(false);
    expect(w.text()).toContain('Kitchen');
  });

  it('clears quiet hours with null', async () => {
    const f = hub({ 'PATCH /api/displays/dsp_hall': (b) => ({ status: 200, body: { display: { ...DISPLAYS[0], ...(b as object) } } }) });
    const h = harness(docs(withIncidents));
    const w = h.mountIn(DisplaysPane);
    await settle();
    await w.find('button[aria-label="Edit Hall tablet"]').trigger('click');
    await settle();
    await w.find('form.dpl-edit input[type="checkbox"]').setValue(false);
    await w.find('form.dpl-edit').trigger('submit');
    await settle();
    expect(f.last('PATCH', '/api/displays/dsp_hall')?.body).toEqual({ quiet: null });
    expect(w.findAll('.dpl-row')[0]!.text()).toContain('no quiet hours');
  });

  it('asks before revoking, then removes the display', async () => {
    const f = hub();
    const h = harness(docs(withIncidents));
    const w = h.mountIn(DisplaysPane);
    await settle();
    await w.find('button[aria-label="Revoke Hall tablet"]').trigger('click');
    await settle();
    expect(w.find('.dpl-revoke').text()).toContain('stops showing the board at once');
    expect(await axeViolations()).toEqual([]);
    expect(f.last('DELETE', '/api/displays/dsp_hall')).toBeUndefined();
    await w.find('.dpl-revoke .btn.danger').trigger('click');
    await settle();
    expect(f.last('DELETE', '/api/displays/dsp_hall')).toBeDefined();
    expect(w.findAll('.dpl-row')).toHaveLength(1);
    expect(h.ui.state.toasts.at(-1)?.text).toContain('Revoked “Hall tablet”');
  });

  it('is admin and owner only', () => {
    expect(permissions('owner').panes).toContain('displays');
    expect(permissions('admin').panes).toContain('displays');
    for (const r of ['operator', 'viewer', 'household']) expect(permissions(r).panes).not.toContain('displays');
  });

  it('sits under People & access in the Settings sheet', async () => {
    hub();
    const h = harness(docs(withIncidents));
    navigate('/settings/displays');
    const w = h.mountIn(SettingsSheet);
    await settle();
    await settle();
    expect(document.querySelector('#settings-h')?.textContent).toBe('Displays');
    const group = document.querySelector('[role="group"][aria-label="People & access"]');
    expect(group?.textContent).toContain('Displays');
    expect(document.querySelector('#dpl-h')).not.toBeNull();
    w.unmount();
    // Below admin it isn't listed, and its path falls back to the first pane.
    h.session.state.user = { id: 'u2', username: 'sam', role: 'operator' };
    const w2 = h.mountIn(SettingsSheet);
    await settle();
    expect(document.querySelector('#settings-h')?.textContent).not.toBe('Displays');
    expect(document.body.textContent).not.toContain('wall screens');
    w2.unmount();
  });
});
