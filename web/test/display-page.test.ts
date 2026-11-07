/**
 * The wall display page (views/DisplayPage.vue) and its route: the fragment's
 * secret is claimed and dropped, the board renders with no chrome from
 * GET /api/display and the display's socket, nothing opens on a click, and
 * the calm (lib/display.ts) shows on the page: dim, dark, the wake line,
 * acknowledged incidents and the age of the data. Fake fetch, fake socket,
 * fake clock.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import { nextTick } from 'vue';
import { toDocument } from '@chadash/layout';
import App from '../src/App.vue';
import DisplayPage from '../src/views/DisplayPage.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { navigate, resolve, route, syncRoute } from '../src/lib/router';
import { defaultBoard } from '../src/model/defaultLayout';
import type { Incident } from '../src/model/types';
import { iconMap, resetIcons } from '../src/stores/icons';
import { axeViolations } from './axe';
import { fakeFetch, type Handler } from './fake-fetch';
import { FakeSocket, factory } from './fake-socket';
import { docs, withIncidents } from './fixtures';

const SECRET = 'cdc_aaaaaaaaaaaaaaaaaaaaaaaaaa_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb';
const NOW = Date.parse(withIncidents.now); // 14:05 UTC
const MIN = 60_000;

function displayInfo(over: Record<string, unknown> = {}) {
  const d = docs(withIncidents);
  return {
    display: { id: 'dsp_hall', name: 'Hall tablet', quiet: null },
    board: { id: 'brd_home', name: 'Home' },
    layout: { revision: 3, format_version: 1, layout: toDocument(defaultBoard(d.graph, NOW)), tiles: {} },
    zone: '',
    offset_minutes: 0,
    now: NOW,
    icons: { by_icon: {}, by_origin: { 'http://plex.example': '/api/display/icons/ic_aaaaaaaaaa?v=1' }, by_instance: {} },
    ...over,
  };
}

function hub(over: Record<string, Handler | { status: number; body?: unknown }> = {}) {
  return fakeFetch({
    'POST /api/display/claim': { status: 204 },
    'GET /api/display': { status: 200, body: displayInfo() },
    ...over,
  });
}

async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) {
    await vi.dynamicImportSettled();
    await flushPromises();
    await nextTick();
  }
}

function at(path: string): void {
  history.replaceState(null, '', path);
  syncRoute();
}

async function mountDisplay(props: Record<string, unknown> = {}) {
  const w = mount(DisplayPage, { props: { board: 'brd_home', createSocket: factory, tickMs: 1000, ...props }, attachTo: document.body });
  await settle();
  return w;
}

/** The socket's handshake with the sim's documents; a long ping so silence doesn't read as stale. */
function connect(data: Record<string, unknown> = docs(withIncidents)): FakeSocket {
  const ws = FakeSocket.last;
  ws.handshake(data, 3600);
  return ws;
}

function addIncident(ws: FakeSocket, inc: Incident, seq: number): void {
  ws.push({ op: 'diff', id: ws.subId('inbox'), seq, patch: [{ op: 'add', path: `/inbox/incidents/${inc.id}`, value: inc }] });
}

const NEW_DOWN: Incident = { id: 'nas-down', class: 'problem', severity: 'down', status: 'open', title: 'The NAS is down', summary: '', target: 'host:nas', opened_at: '2026-10-03T14:06:00Z' };

beforeEach(() => {
  FakeSocket.reset();
  resetIcons();
  setDisplayTimeZone('UTC');
  vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] });
  vi.setSystemTime(NOW);
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
  navigate('/', { replace: true });
});

describe('the display route', () => {
  it('resolves /display/<board>, with or without a board', () => {
    expect(resolve({ path: '/display/brd_home', query: {} })).toEqual({ name: 'display', board: 'brd_home' });
    expect(resolve({ path: '/display', query: {} })).toEqual({ name: 'display', board: '' });
    expect(resolve({ path: '/settings/displays', query: {} })).toEqual({ name: 'settings', pane: 'displays' });
  });
});

describe('signing a display in', () => {
  it('claims the secret in the fragment, drops it, and shows the board with no chrome', async () => {
    at(`/display/brd_home#${SECRET}`);
    const setItem = vi.spyOn(Storage.prototype, 'setItem');
    const f = hub();
    const w = await mountDisplay();
    expect(f.calls[0]).toEqual({ method: 'POST', path: '/api/display/claim', body: { token: SECRET } });
    expect(f.calls[1]).toMatchObject({ method: 'GET', path: '/api/display' });
    // The secret is gone from the address, and kept nowhere.
    expect(location.hash).toBe('');
    expect(location.pathname).toBe('/display/brd_home');
    expect(setItem.mock.calls.flat().join(' ')).not.toContain('cdc_');
    // Never a session route.
    expect(f.calls.map((c) => c.path).filter((p) => !p.startsWith('/api/display'))).toEqual([]);
    // Tile icons come from the display's own route.
    expect(iconMap.value.by_origin['http://plex.example']).toBe('/api/display/icons/ic_aaaaaaaaaa?v=1');

    const ws = FakeSocket.last;
    expect(ws.url).toMatch(/\/api\/display\/ws$/);
    connect();
    await settle();
    expect(ws.ops('sub').map((s) => (s['topics'] as string[])[0])).toEqual(['graph', 'metrics', 'inbox', 'causes', 'backups']);
    expect(w.find('main.dsp').attributes('aria-label')).toBe('Hall tablet');
    expect(w.findAll('.tile').length).toBeGreaterThan(5);
    // No chrome: no Briefing, no Inbox button, no menus, no command bar.
    expect(w.find('.brief').exists()).toBe(false);
    expect(w.find('nav').exists()).toBe(false);
    expect(w.find('.tile-cog').exists()).toBe(false);
    expect(document.body.textContent).not.toMatch(/Edit layout|Sign out|Needs attention/);
    expect(await axeViolations()).toEqual([]);
    w.unmount();
  });

  it('opens on the display’s own board and fixes the address', async () => {
    at('/display/brd_other');
    hub();
    const w = await mountDisplay({ board: 'brd_other' });
    expect(location.pathname).toBe('/display/brd_home');
    w.unmount();
  });

  it('says calmly that it isn’t signed in when the hub answers 401', async () => {
    at('/display/brd_home');
    hub({ 'GET /api/display': { status: 401, body: { error: { code: 'invalid_display', message: 'not a display' } } } });
    const w = await mountDisplay();
    expect(w.find('h1').text()).toBe('This display isn’t signed in');
    expect(w.text()).toContain('Settings → Displays');
    expect(FakeSocket.all).toHaveLength(0);
    expect(await axeViolations()).toEqual([]);
    w.unmount();
  });

  it('says so too when the link was revoked before it was claimed', async () => {
    at(`/display/brd_home#${SECRET}`);
    hub({
      'POST /api/display/claim': { status: 401, body: { error: { code: 'invalid_display', message: 'revoked' } } },
      'GET /api/display': { status: 401, body: { error: { code: 'invalid_display', message: 'no cookie' } } },
    });
    const w = await mountDisplay();
    expect(location.hash).toBe('');
    expect(w.find('h1').text()).toBe('This display isn’t signed in');
    w.unmount();
  });

  it('notices a revoke: the socket keeps failing and the hub says 401', async () => {
    at('/display/brd_home');
    let revoked = false;
    hub({ 'GET /api/display': () => (revoked ? { status: 401, body: { error: { code: 'invalid_display', message: 'revoked' } } } : { status: 200, body: displayInfo() }) });
    const w = await mountDisplay({ createSocket: (url: string) => factory(url) });
    connect();
    await settle();
    revoked = true;
    // Revoking closes the socket; the client retries, fails, and asks the hub.
    vi.useRealTimers();
    FakeSocket.last.drop(1008);
    for (let i = 0; i < 400 && !w.text().includes('isn’t signed in'); i++) {
      const s = FakeSocket.last;
      if (s.readyState === 0) s.drop(1006);
      await new Promise((r) => setTimeout(r, 10));
      await settle();
    }
    expect(w.find('h1').text()).toBe('This display isn’t signed in');
    w.unmount();
  });

  it('App renders the display page for /display, before any session door', async () => {
    at('/display/brd_home');
    vi.stubGlobal('WebSocket', FakeSocket);
    const f = hub();
    const w = mount(App, { attachTo: document.body });
    for (let i = 0; i < 10; i++) await settle();
    expect(f.calls.some((c) => c.path === '/api/session')).toBe(false);
    expect(f.calls.some((c) => c.path === '/api/display')).toBe(true);
    expect(w.find('main.dsp').exists()).toBe(true);
    w.unmount();
  });
});

describe('the calm on the page', () => {
  it('is bright after a change, dims after 2 minutes, and moves against burn-in', async () => {
    at('/display/brd_home');
    hub();
    const w = await mountDisplay();
    connect();
    await settle();
    const main = () => w.find('main.dsp');
    expect(main().attributes('data-brightness')).toBe('full');
    const before = w.find<HTMLElement>('.dsp-content').element.style.transform;
    vi.advanceTimersByTime(2 * MIN + 1000);
    await settle();
    expect(main().attributes('data-brightness')).toBe('dim');
    vi.advanceTimersByTime(3 * MIN);
    await settle();
    expect(w.find<HTMLElement>('.dsp-content').element.style.transform).not.toBe(before);
    expect(main().attributes('data-tint')).toBeUndefined();
    w.unmount();
  });

  it('wakes for a new incident with its one line, and dims once someone is on it', async () => {
    at('/display/brd_home');
    hub();
    const w = await mountDisplay();
    const ws = connect();
    await settle();
    vi.advanceTimersByTime(3 * MIN);
    await settle();
    // Incidents open when the page first saw the Inbox don't wake it.
    expect(w.find('main.dsp').attributes('data-brightness')).toBe('dim');
    expect(w.find('.dsp-wake').exists()).toBe(false);

    addIncident(ws, NEW_DOWN, 2);
    await settle();
    expect(w.find('main.dsp').attributes('data-brightness')).toBe('full');
    expect(w.find('.dsp-wake').text()).toContain('The NAS is down');
    expect(w.find('.dsp-wake').attributes('role')).toBe('status');
    vi.advanceTimersByTime(10 * MIN);
    await settle();
    expect(w.find('.dsp-wake').text()).toContain('The NAS is down'); // until it's resolved or acknowledged

    ws.push({ op: 'diff', id: ws.subId('inbox'), seq: 3, patch: [{ op: 'add', path: '/inbox/incidents/nas-down/acknowledged', value: { by: 'sam', at: '2026-10-03T14:20:00Z' } }] });
    await settle();
    expect(w.find('.dsp-wake').exists()).toBe(false);
    expect(w.find('.dsp-acked').text()).toContain('The NAS is down');
    expect(w.find('.dsp-acked').text()).toContain('sam is on it');
    vi.advanceTimersByTime(3 * MIN);
    await settle();
    expect(w.find('main.dsp').attributes('data-brightness')).toBe('dim');
    expect(await axeViolations()).toEqual([]);
    w.unmount();
  });

  it('is dark in quiet hours, and only a down incident wakes it', async () => {
    at('/display/brd_home');
    // Quiet from 14:00 to 15:00 hub time, which is now.
    hub({ 'GET /api/display': { status: 200, body: displayInfo({ display: { id: 'dsp_hall', name: 'Hall tablet', quiet: { start: '14:00', end: '15:00' } } }) } });
    const w = await mountDisplay();
    const ws = connect();
    await settle();
    expect(w.find('main.dsp').attributes('data-brightness')).toBe('dark');
    addIncident(ws, { ...NEW_DOWN, id: 'slow', severity: 'attention', title: 'Speed test is slow' }, 2);
    await settle();
    expect(w.find('main.dsp').attributes('data-brightness')).toBe('dark');
    expect(w.find('.dsp-wake').exists()).toBe(false);
    addIncident(ws, NEW_DOWN, 3);
    await settle();
    expect(w.find('main.dsp').attributes('data-brightness')).toBe('full');
    expect(w.find('.dsp-wake').text()).toContain('The NAS is down');
    w.unmount();
  });

  it('tints at night, hub time, from the hub’s offset', async () => {
    at('/display/brd_home');
    // 14:05 UTC at UTC+8 is 22:05.
    hub({ 'GET /api/display': { status: 200, body: displayInfo({ offset_minutes: 480 }) } });
    const w = await mountDisplay();
    connect();
    await settle();
    expect(w.find('main.dsp').attributes('data-tint')).toBeDefined();
    w.unmount();
  });

  it('says how old the data is while the socket is down', async () => {
    at('/display/brd_home');
    hub();
    const w = await mountDisplay();
    const ws = connect();
    await settle();
    expect(w.find('.dsp-stale').exists()).toBe(false);
    ws.drop();
    vi.advanceTimersByTime(5 * MIN);
    await settle();
    expect(w.find('.dsp-stale').text()).toBe('Data from 5 minutes ago');
    w.unmount();
  });
});

describe('the display styles', () => {
  it('a wall display draws its board at the simplified level', async () => {
    at('/display/brd_home');
    hub();
    const w = await mountDisplay();
    connect();
    await settle();
    const tiles = w.findAll('.tile');
    expect(tiles.length).toBeGreaterThan(0);
    expect(tiles.every((t) => t.classes('lvl-simplified'))).toBe(true);
    expect(w.find('main.dsp').attributes('data-style')).toBe('wall');
    expect(w.find('.tile-cog').exists()).toBe(false);
    w.unmount();
  });

  it('an e-ink display is ink: never dimmed or moved, with absolute times', async () => {
    at('/display/brd_home');
    hub({ 'GET /api/display': { status: 200, body: displayInfo({ display: { id: 'dsp_ink', name: 'Fridge', quiet: { start: '00:00', end: '23:59' }, style: 'ink' } }) } });
    const w = await mountDisplay();
    connect();
    await settle();
    expect(w.findAll('.tile').every((t) => t.classes('lvl-ink'))).toBe(true);
    vi.advanceTimersByTime(10 * MIN);
    await settle();
    const main = w.find('main.dsp');
    expect(main.attributes('data-brightness')).toBe('full');
    expect(main.attributes('data-tint')).toBeUndefined();
    expect(w.find<HTMLElement>('.dsp-content').element.style.transform).toBe('translate(0px, 0px)');
    expect(w.text()).not.toMatch(/\d+[smhd] ago/);
    w.unmount();
  });

  it('a TV moves focus between tiles with the arrow keys', async () => {
    at('/display/brd_home');
    hub({ 'GET /api/display': { status: 200, body: displayInfo({ display: { id: 'dsp_tv', name: 'Living room', quiet: null, style: 'tv' } }) } });
    const w = await mountDisplay();
    connect();
    await settle();
    vi.useRealTimers();
    await new Promise((r) => setTimeout(r, 5));
    const cells = [...(w.element as HTMLElement).querySelectorAll<HTMLElement>('.cell')];
    expect(cells.length).toBeGreaterThan(1);
    expect(cells.filter((c) => c.tabIndex === 0)).toHaveLength(1);
    // happy-dom lays nothing out: give the cells a row of boxes.
    cells.forEach((c, i) => (c.getBoundingClientRect = () => ({ left: i * 100, top: 0, width: 90, height: 90, right: i * 100 + 90, bottom: 90, x: i * 100, y: 0, toJSON() {} }) as DOMRect));
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight' }));
    expect(document.activeElement).toBe(cells[0]);
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight' }));
    expect(document.activeElement).toBe(cells[1]);
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft' }));
    expect(document.activeElement).toBe(cells[0]);
    expect(w.find('main.dsp').attributes('data-style')).toBe('tv');
    w.unmount();
  });

  it('a TV zooms by the width its browser reports, and follows a resize', async () => {
    at('/display/brd_home');
    hub({ 'GET /api/display': { status: 200, body: displayInfo({ display: { id: 'dsp_tv', name: 'Living room', quiet: null, style: 'tv' } }) } });
    const before = window.innerWidth;
    const resize = (width: number): void => {
      Object.defineProperty(window, 'innerWidth', { configurable: true, value: width });
      window.dispatchEvent(new Event('resize'));
    };
    resize(1920);
    const w = await mountDisplay();
    connect();
    await settle();
    const zoom = (): string => w.find<HTMLElement>('.dsp-content').element.style.getPropertyValue('--tv-zoom');
    expect(zoom()).toBe('1.5');
    resize(3840);
    await nextTick();
    expect(zoom()).toBe('2.5');
    w.unmount();
    resize(before);
  });
});

describe('nothing opens on a display', () => {
  it('a click on a tile opens no sheet, no Inbox and no link', async () => {
    at('/display/brd_home');
    hub();
    const w = await mountDisplay();
    connect();
    await settle();
    const before = route.value.path + JSON.stringify(route.value.query);
    for (const t of w.findAll('.tile.clickable').slice(0, 5)) await t.trigger('click');
    for (const b of w.findAll('.dsp-content button').slice(0, 10)) await b.trigger('click');
    await settle();
    expect(route.value.path + JSON.stringify(route.value.query)).toBe(before);
    expect(document.querySelector('.inbox, .sheet, [role="dialog"]')).toBeNull();
    const link = w.find('.dsp-content a[href]');
    if (link.exists()) {
      const e = new MouseEvent('click', { bubbles: true, cancelable: true });
      link.element.dispatchEvent(e);
      expect(e.defaultPrevented).toBe(true);
    }
    w.unmount();
  });
});
