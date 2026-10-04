import 'fake-indexeddb/auto';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import HubBanner from '../src/components/HubBanner.vue';
import { setDisplayTimeZone } from '../src/lib/format';
import { clearSnapshot, loadSnapshot, saveSnapshot, SNAPSHOT_VERSION, type Snapshot } from '../src/lib/snapshot';
import { createBoardStore } from '../src/stores/board';
import { persistSnapshots } from '../src/stores/persist';
import { createSession } from '../src/stores/session';
import { defaultBoard } from '../src/model/defaultLayout';
import { toDocument } from '@chadash/layout';
import { docs, withIncidents } from './fixtures';
import { harness } from './harness';

const USER = { id: 'u1', username: 'owner', role: 'owner' };
const wait = (ms = 30) => new Promise((r) => setTimeout(r, ms));

beforeEach(async () => {
  setDisplayTimeZone('UTC');
  await clearSnapshot();
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('the last-known snapshot in IndexedDB', () => {
  it('saves and restores the topics, the board and who was signed in', async () => {
    const d = docs(withIncidents);
    const board = { id: 'b1', name: 'Home', revision: 3, layout: toDocument(defaultBoard(d.graph, Date.parse('2026-10-03T14:05:00Z'))) };
    expect(await saveSnapshot({ savedAt: 1_000, user: USER, ...d, board, timeZone: 'UTC', clockOffset: -5, demo: true })).toBe(true);
    const s = (await loadSnapshot())!;
    expect(s.v).toBe(SNAPSHOT_VERSION);
    expect(s.savedAt).toBe(1_000);
    expect(s.user).toEqual(USER);
    expect(Object.keys(s.graph!.resources)).toHaveLength(Object.keys(d.graph.resources).length);
    expect(s.inbox!.incidents['plex-token-revoked']!.title).toBe('Plex rejected its token');
    expect(s.metrics!.series).toEqual(d.metrics.series);
    expect(s.board).toEqual(board);
    expect([s.timeZone, s.clockOffset, s.demo]).toEqual(['UTC', -5, true]);
  });

  it('stores the live documents even though they are Vue proxies', async () => {
    const h = harness(docs(withIncidents));
    expect(await saveSnapshot({ savedAt: 2_000, user: USER, graph: h.live.doc.graph!, inbox: h.live.doc.inbox! })).toBe(true);
    expect((await loadSnapshot())!.graph!.resources['app:plex']!.name).toBe(h.live.doc.graph!.resources['app:plex']!.name);
  });

  it('forgets on clear, and ignores records it can’t trust', async () => {
    await saveSnapshot({ savedAt: 1, user: USER });
    await clearSnapshot();
    expect(await loadSnapshot()).toBeUndefined();
    await saveSnapshot({ savedAt: 1, user: { ...USER, id: '' } });
    expect(await loadSnapshot()).toBeUndefined();
  });

  it('never throws when storage is missing or broken', async () => {
    vi.stubGlobal('indexedDB', undefined);
    expect(await saveSnapshot({ savedAt: 1, user: USER })).toBe(false);
    expect(await loadSnapshot()).toBeUndefined();
    vi.stubGlobal('indexedDB', {
      open() {
        throw new Error('SecurityError');
      },
    });
    expect(await saveSnapshot({ savedAt: 1, user: USER })).toBe(false);
    await expect(clearSnapshot()).resolves.toBeUndefined();
  });
});

describe('opening while the hub is unreachable', () => {
  it('opens the front page on the snapshot instead of the unreachable door', async () => {
    await saveSnapshot({ savedAt: Date.parse('2026-10-03T14:02:00Z'), user: USER, ...docs(withIncidents) });
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')));
    const s = createSession();
    await s.refresh();
    expect(s.state.phase).toBe('ready');
    expect(s.state.offline).toBe(true);
    expect(s.state.user).toEqual(USER);
    const snap = s.takeRestored()!;
    expect(snap.inbox!.incidents['backup-failed']).toBeDefined();
    expect(s.takeRestored()).toBeUndefined(); // handed over once
  });

  it('shows the unreachable door when there is no snapshot', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')));
    const s = createSession();
    await s.refresh();
    expect(s.state.phase).toBe('unreachable');
  });

  it('treats a proxy’s 502 as unreachable too', async () => {
    await saveSnapshot({ savedAt: 5, user: USER });
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('bad gateway', { status: 502 })));
    const s = createSession();
    await s.refresh();
    expect(s.state.offline).toBe(true);
  });

  it('confirms the session once the hub is back, or asks to sign in', async () => {
    await saveSnapshot({ savedAt: 5, user: USER });
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')));
    const s = createSession();
    await s.refresh();
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ user: USER }), { status: 200 })));
    await s.confirm();
    expect(s.state.offline).toBe(false);
    expect(s.state.phase).toBe('ready');

    await s.refresh(); // reachable now: stays signed in
    s.state.offline = true;
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{}', { status: 401 })));
    await s.confirm();
    expect(s.state.phase).toBe('login');
  });

  it('shows last-known values under the offline banner, with an as-of time', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2026-10-03T14:30:00Z'));
    const h = harness();
    h.ws.drop(); // the hub doesn't answer
    h.live.restore({ savedAt: Date.parse('2026-10-03T14:02:00Z'), ...docs(withIncidents) });
    h.live.tick();
    expect(h.live.graph.value).toBeDefined();
    expect(h.live.stale.value).toBe(true);
    const w = h.mountIn(HubBanner);
    expect(w.find('.banner').text()).toContain('Cha Dash hub unreachable.');
    expect(w.find('.banner').text()).toContain('as of 14:02');
    vi.useRealTimers();
  });

  it('keeps the stored board while the hub can’t be asked', async () => {
    const d = docs(withIncidents);
    const layout = toDocument(defaultBoard(d.graph, Date.parse('2026-10-03T14:05:00Z')));
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')));
    const board = createBoardStore({ waitForGraph: async () => d.graph });
    board.restore({ id: 'b1', name: 'Home', revision: 2, layout });
    expect(board.board.value).toBeDefined();
    await board.load();
    expect(board.state.status).toBe('ready');
    expect(board.state.lastKnown).toBe(true);
    expect(board.state.name).toBe('Home');
  });
});

describe('keeping the snapshot fresh', () => {
  it('writes while live, and never while showing a restored snapshot', async () => {
    const d = docs(withIncidents);
    const h = harness(d);
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ revision: 1, format_version: 1, layout: null }), { status: 200 })));
    const stop = persistSnapshots(h.live, h.board, h.session, 10_000);
    await wait(5); // a frame in a later millisecond than the handshake's
    h.ws.push({ op: 'ping' });
    await wait();
    await flushPromises();
    await wait();
    const s = (await loadSnapshot())!;
    expect(s.user).toEqual(USER);
    expect(Object.keys(s.graph!.resources).length).toBeGreaterThan(100);
    expect(s.inbox!.incidents['plex-token-revoked']).toBeDefined();
    stop();

    await clearSnapshot();
    h.session.state.offline = true;
    const stop2 = persistSnapshots(h.live, h.board, h.session, 0);
    await wait(5);
    h.ws.push({ op: 'ping' });
    await wait();
    expect(await loadSnapshot()).toBeUndefined();
    stop2();
  });

  it('is deleted when you sign out', async () => {
    await saveSnapshot({ savedAt: 5, user: USER });
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 204 })));
    const s = createSession();
    s.state.phase = 'ready';
    await s.logout();
    expect(s.state.phase).toBe('login');
    expect(await loadSnapshot()).toBeUndefined();
  });
});

// The type is exported for the shell; keep it honest.
const _typed: Pick<Snapshot, 'v'> = { v: SNAPSHOT_VERSION };
void _typed;
