/**
 * The hub's first-paint snapshot (lib/inline.ts): read once from its data
 * block, the app starts signed in, the front page renders from it with no
 * loading state and no request, and the socket's snapshots supersede it
 * without changing what's on screen.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import { toDocument } from '@chadash/layout';
import App from '../src/App.vue';
import { INLINE_SNAPSHOT_ID, takeInlineSnapshot, type InlineSnapshot } from '../src/lib/inline';
import { setDisplayTimeZone } from '../src/lib/format';
import { navigate } from '../src/lib/router';
import { defaultBoard } from '../src/model/defaultLayout';
import { createBoardStore } from '../src/stores/board';
import { createSession } from '../src/stores/session';
import { fakeFetch } from './fake-fetch';
import { FakeSocket } from './fake-socket';
import { docs, withIncidents } from './fixtures';

const BS = String.fromCharCode(92);
const USER = { id: 'u1', username: 'owner', role: 'owner' };

function snapshot(): InlineSnapshot {
  const d = docs(withIncidents);
  const layout = toDocument(defaultBoard(d.graph, Date.parse(withIncidents.now)));
  return {
    v: 1,
    user: USER,
    boards: [{ id: 'b1', name: 'Home' }],
    board: { id: 'b1', name: 'Home', revision: 3, format_version: 1, layout },
    demo: true,
    ...d,
  };
}

/** The data block as internal/webui writes it: JSON with <, > and & escaped. */
function inlineIntoPage(s: unknown, attrs = 'type="application/json"'): void {
  const json = JSON.stringify(s).replace(/[<>&]/g, (c) => `${BS}u${c.charCodeAt(0).toString(16).padStart(4, '0')}`);
  document.body.insertAdjacentHTML('beforeend', `<script ${attrs} id="${INLINE_SNAPSHOT_ID}">${json}</script>`);
}

/** Wait for lazy chunks (the front page is one) and Vue; a cold transform can take a while. */
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
  setDisplayTimeZone(undefined);
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
  navigate('/', { replace: true });
});

describe('reading the data block', () => {
  it('parses it once and removes it from the page', () => {
    const s = snapshot();
    s.graph.resources['app:plex']!.name = '</script><script>alert(1)</script>';
    inlineIntoPage(s);
    const got = takeInlineSnapshot()!;
    expect(got.user).toEqual(USER);
    expect(got.board?.id).toBe('b1');
    expect(got.graph.resources['app:plex']!.name).toBe('</script><script>alert(1)</script>');
    expect(document.getElementById(INLINE_SNAPSHOT_ID)).toBeNull();
    expect(takeInlineSnapshot()).toBeUndefined();
  });

  it('ignores anything it can’t trust', () => {
    const bad: [unknown, string?][] = [
      [{ ...snapshot(), v: 2 }],
      [{ ...snapshot(), user: { id: '', username: 'x', role: 'owner' } }],
      [{ ...snapshot(), graph: undefined }],
      ['not an object'],
      [snapshot(), 'type="text/javascript"'],
    ];
    for (const [s, attrs] of bad) {
      inlineIntoPage(s, attrs);
      expect(takeInlineSnapshot()).toBeUndefined();
      expect(document.getElementById(INLINE_SNAPSHOT_ID)).toBeNull();
    }
    document.body.insertAdjacentHTML('beforeend', `<script type="application/json" id="${INLINE_SNAPSHOT_ID}">{not json</script>`);
    expect(takeInlineSnapshot()).toBeUndefined();
    document.body.insertAdjacentHTML('beforeend', `<div id="${INLINE_SNAPSHOT_ID}">{}</div>`);
    expect(takeInlineSnapshot()).toBeUndefined();
    expect(takeInlineSnapshot(undefined)).toBeUndefined();
  });

  it('starts the session signed in, without asking the hub', async () => {
    const f = fakeFetch({});
    const s = createSession({ inline: snapshot() });
    expect(s.state.phase).toBe('ready');
    expect(s.state.user).toEqual(USER);
    await s.refresh(); // the hub vouched for this session when it served the page
    expect(f.calls).toEqual([]);
    expect(s.takeInline()?.v).toBe(1);
    expect(s.takeInline()).toBeUndefined();
  });
});

describe('seeding the board', () => {
  it('takes the layout and the tiles, with no request', () => {
    const f = fakeFetch({});
    const s = snapshot();
    const board = createBoardStore({ waitForGraph: async () => s.graph });
    const tiles = { 'service@homepage:servers/proxmox': { name: 'Proxmox', href: 'https://pve.example.com:8006' } };
    expect(board.seed(s.boards, { ...s.board!, tiles })).toBe(true);
    expect(board.board.value?.sections.length).toBeGreaterThan(0);
    expect(board.tiles.value['service@homepage:servers/proxmox']?.name).toBe('Proxmox');
    expect([board.state.status, board.state.boardId, board.state.revision, board.state.lastKnown]).toEqual(['ready', 'b1', 3, false]);
    expect(board.snapshot()?.layout).toEqual(s.board!.layout);
    expect(board.seed(s.boards, { ...s.board!, layout: { version: 99 } })).toBe(false); // a layout this version can't read
    expect(f.calls).toEqual([]);
  });
});

describe('the first paint', () => {
  it('renders the front page from the snapshot, then the socket supersedes it with no change on screen', async () => {
    const f = fakeFetch({ 'GET /api/demo': { status: 200, body: { enabled: true, now: withIncidents.now, seed: 1 } } });
    const s = snapshot();
    inlineIntoPage(s);
    const w = mount(App, { attachTo: document.body });
    // The front page is a lazy chunk; nothing else is awaited.
    await until(() => document.querySelector('.tile') !== null, 'the board');
    const page = () => document.querySelector('main.page') as HTMLElement;

    // No loading state anywhere: no busy door, tile or skeleton.
    expect(document.querySelector('[aria-busy="true"]')).toBeNull();
    expect(document.querySelector('.skel, .is-loading')).toBeNull();
    expect(document.querySelectorAll('.tile').length).toBe(17);
    expect(page().textContent).toContain('Plex');
    // Signed in and on the board without a request: no /api/setup, session, boards or layout.
    const asked = f.calls.map((c) => c.path);
    expect(asked.filter((p) => /^\/api\/(setup|auth\/session|boards|boards\/[^/]+\/layout)$/.test(p))).toEqual([]);
    // Demo times are UTC from the first paint.
    expect(page().textContent).toMatch(/\d\d:\d\d/);

    // The hub answers: the same documents arrive as snapshots.
    await flushPromises();
    const before = page().innerHTML;
    const ws = FakeSocket.last;
    ws.handshake({ graph: s.graph, metrics: s.metrics, inbox: s.inbox });
    await flushPromises();
    expect(page().innerHTML).toBe(before);
    expect(document.querySelector('.banner')).toBeNull();

    // And then it's live like any page.
    const id = ws.subId('metrics');
    ws.push({ op: 'diff', id, seq: 2, patch: [{ op: 'replace', path: '/metrics/series/app:plex#streams/value', value: 9 }] });
    await flushPromises();
    expect(page().innerHTML).not.toBe(before);
    w.unmount();
  });

  it('starts the usual way without a snapshot', async () => {
    const f = fakeFetch({ 'GET /api/setup': { status: 200, body: { required: false } }, 'GET /api/auth/session': { status: 401, body: {} } });
    const w = mount(App, { attachTo: document.body });
    expect(document.querySelector('.door[aria-busy="true"]')).not.toBeNull();
    await until(() => document.querySelector('#login-user') !== null || document.querySelector('form') !== null, 'sign-in');
    expect(f.calls.map((c) => c.path)).toEqual(['/api/setup', '/api/auth/session']);
    w.unmount();
  });
});
