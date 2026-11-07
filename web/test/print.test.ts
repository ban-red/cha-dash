/**
 * Printing a board (plan 18, "Print with absolute times"): print CSS can't
 * change text, so /print/<board> renders the board at the ink level, where
 * times are absolute, with none of the page's chrome, and calls the
 * browser's print once it has data. Afterwards it goes back to the board.
 * Print is in the command bar and the Settings menu.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import { toDocument } from '@chadash/layout';
import App from '../src/App.vue';
import { INLINE_SNAPSHOT_ID, type InlineSnapshot } from '../src/lib/inline';
import { setDisplayTimeZone } from '../src/lib/format';
import { navigate, printPath, resolve, route } from '../src/lib/router';
import { buildCommands } from '../src/model/commands';
import { defaultBoard } from '../src/model/defaultLayout';
import { permissions } from '../src/model/roles';
import { fakeFetch } from './fake-fetch';
import { FakeSocket } from './fake-socket';
import { docs, withIncidents } from './fixtures';

const BS = String.fromCharCode(92);

function inline(): void {
  const d = docs(withIncidents);
  const s: InlineSnapshot = {
    v: 1,
    user: { id: 'u1', username: 'owner', role: 'owner' },
    boards: [{ id: 'b1', name: 'Home' }],
    board: { id: 'b1', name: 'Home', revision: 3, format_version: 1, layout: toDocument(defaultBoard(d.graph, Date.parse(withIncidents.now))) },
    demo: true,
    ...d,
  };
  const json = JSON.stringify(s).replace(/[<>&]/g, (c) => `${BS}u${c.charCodeAt(0).toString(16).padStart(4, '0')}`);
  document.body.insertAdjacentHTML('beforeend', `<script type="application/json" id="${INLINE_SNAPSHOT_ID}">${json}</script>`);
}

async function until(cond: () => boolean, what: string): Promise<void> {
  for (let i = 0; i < 1000 && !cond(); i++) {
    await vi.dynamicImportSettled();
    await flushPromises();
    await new Promise((r) => setTimeout(r, 5));
  }
  if (!cond()) throw new Error(`timed out waiting for ${what}`);
}

beforeEach(() => {
  FakeSocket.reset();
  vi.stubGlobal('WebSocket', FakeSocket);
  vi.stubGlobal('print', vi.fn()); // happy-dom has none
  setDisplayTimeZone(undefined);
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  document.body.innerHTML = '';
  navigate('/', { replace: true });
});

describe('the print route', () => {
  it('is the front page of that board, printing', () => {
    expect(resolve({ path: '/print/b1', query: {} })).toEqual({ name: 'front', board: 'b1', print: true });
    expect(resolve({ path: '/print', query: {} })).toEqual({ name: 'front', print: true });
    expect(resolve({ path: '/boards/b1', query: {} })).toEqual({ name: 'front', board: 'b1' });
    expect(printPath('a b')).toBe('/print/a%20b');
  });
});

describe('printing a board', () => {
  it('renders it at the ink level with absolute times and no chrome, prints once, then goes back', async () => {
    const print = vi.fn();
    vi.stubGlobal('print', print);
    fakeFetch({ 'GET /api/demo': { status: 200, body: { enabled: true, now: withIncidents.now, seed: 1 } } });
    navigate('/print/b1', { replace: true });
    inline();
    const w = mount(App, { attachTo: document.body });
    await until(() => document.querySelector('.tile') !== null, 'the board');
    await until(() => print.mock.calls.length > 0, 'print');

    const page = document.querySelector('main.print-page') as HTMLElement;
    expect(page).not.toBeNull();
    expect(page.querySelector('.tile.lvl-ink')).not.toBeNull();
    expect(page.querySelector('.tile:not(.lvl-ink)')).toBeNull();
    // No relative times: they'd be wrong by the time the paper is read.
    expect(page.textContent).not.toMatch(/\bago\b|just now/);
    // None of the page's chrome.
    expect(document.querySelector('.brief, main.page')).toBeNull();

    // A new snapshot doesn't print again.
    FakeSocket.last.handshake(docs(withIncidents));
    await flushPromises();
    expect(print).toHaveBeenCalledTimes(1);

    window.dispatchEvent(new Event('afterprint'));
    await flushPromises();
    expect(route.value.path).toBe('/boards/b1');
    w.unmount();
  });

  it('is in the Settings menu for the board on screen', async () => {
    fakeFetch({ 'GET /api/demo': { status: 200, body: { enabled: true, now: withIncidents.now, seed: 1 } } });
    inline();
    const w = mount(App, { attachTo: document.body });
    await until(() => document.querySelector('.tile') !== null, 'the board');
    (document.querySelector('.settings-menu [aria-label="Settings"]') as HTMLButtonElement).click();
    await flushPromises();
    const item = [...document.querySelectorAll<HTMLAnchorElement>('a[role="menuitem"]')].find((a) => a.textContent?.trim() === 'Print this board');
    expect(item?.getAttribute('href')).toBe('/print/b1');
    w.unmount();
  });

  it('is in the command bar for the board on screen', () => {
    const d = { openInbox: vi.fn(), toggleEdit: vi.fn(), openBoard: vi.fn(), reveal: vi.fn(), openSettings: vi.fn(), addIntegration: vi.fn(), setTheme: vi.fn(), signOut: vi.fn() };
    const print = buildCommands({ can: permissions('household'), currentBoard: 'b1', do: d }).find((c) => c.id === 'print');
    expect(print?.label).toBe('Print this board');
    print!.run();
    expect(route.value.path).toBe('/print/b1');
    expect(buildCommands({ can: permissions('household'), do: d }).some((c) => c.id === 'print')).toBe(false);
  });
});
