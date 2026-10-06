import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { computed, nextTick, watch } from 'vue';
import { createLive } from '../src/stores/live';
import { toPointer } from '../src/lib/jsonpatch';
import { FakeSocket, factory } from './fake-socket';
import { docs, resolvedIncident, withIncidents } from './fixtures';

beforeEach(() => {
  FakeSocket.reset();
  vi.useFakeTimers();
  vi.setSystemTime(new Date('2026-10-03T14:05:10Z'));
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function liveWithFixtures() {
  const live = createLive({ url: 'ws://hub.test/ws', createSocket: factory, tick: false });
  live.start();
  FakeSocket.last.handshake(docs(withIncidents));
  return { live, ws: FakeSocket.last };
}

describe('live store fed with the sim snapshots', () => {
  it('exposes the topics and derived views', () => {
    const { live } = liveWithFixtures();
    expect(live.conn.status).toBe('live');
    expect(Object.keys(live.graph.value!.resources)).toHaveLength(134);
    expect(Object.keys(live.metrics.value!.series)).toHaveLength(withIncidents.series.length);
    expect(live.problems.value).toHaveLength(8);
    expect(live.repairs.value.map((i) => i.id)).toEqual(['karakeep-api-changed', 'unraid-jupiter.repair', 'plex-token-revoked']);
    expect(live.stale.value).toBe(false);
    expect(live.loading.value).toBe(false);
  });

  it('reflects a resolution from an inbox diff, reactively', async () => {
    const { live, ws } = liveWithFixtures();
    const seen: number[] = [];
    watch(() => live.problems.value.length, (n) => seen.push(n));
    ws.push({
      op: 'diff',
      id: ws.subId('inbox'),
      seq: 2000,
      patch: [
        { op: 'replace', path: toPointer('inbox', 'incidents', 'backup-failed'), value: resolvedIncident('backup-failed') },
        { op: 'replace', path: '/inbox/now', value: '2026-10-03T15:34:00Z' },
      ],
    });
    await nextTick();
    expect(seen).toEqual([7]);
    expect(live.problems.value.map((i) => i.id)).not.toContain('backup-failed');
    expect(live.inbox.value!.incidents['backup-failed']!.resolution).toBe('The retry at 15:30 succeeded.');
  });

  it('follows metric diffs on series ids with slashes', async () => {
    const { live, ws } = liveWithFixtures();
    const key = 'unraid:jupiter/array#used.pct';
    const s = JSON.parse(JSON.stringify(live.metrics.value!.series[key]!)) as { value: number };
    s.value = 72.1;
    ws.push({ op: 'diff', id: ws.subId('metrics'), seq: 2001, patch: [{ op: 'replace', path: toPointer('metrics', 'series', key), value: s }] });
    await nextTick();
    expect(live.metrics.value!.series[key]!.value).toBe(72.1);
  });

  it('follows field-level diffs inside a series and an incident, reactively', async () => {
    // What the hub sends since live-document diffs carry changed fields only.
    const { live, ws } = liveWithFixtures();
    const key = 'unraid:jupiter/array#used.pct';
    const base = toPointer('metrics', 'series', key);
    const s = computed(() => live.metrics.value?.series[key]);
    const seen: unknown[] = [];
    watch(
      () => [s.value?.value, s.value?.as_of, s.value?.stale, s.value?.history?.at(-1)],
      (v) => seen.push(v),
    );
    ws.push({
      op: 'diff',
      id: ws.subId('metrics'),
      seq: 2002,
      patch: [
        { op: 'replace', path: `${base}/as_of`, value: '2026-10-03T14:05:30Z' },
        { op: 'add', path: `${base}/stale`, value: true },
        { op: 'replace', path: `${base}/value`, value: 72.1 },
      ],
    });
    await nextTick();
    expect(seen).toEqual([[72.1, '2026-10-03T14:05:30Z', true, s.value!.history!.at(-1)]]);

    const history = [...s.value!.history!.slice(1), 73.5];
    ws.push({
      op: 'diff',
      id: ws.subId('metrics'),
      seq: 2003,
      patch: [
        { op: 'replace', path: `${base}/history`, value: history },
        { op: 'remove', path: `${base}/stale` },
      ],
    });
    await nextTick();
    expect(seen).toHaveLength(2);
    expect(s.value).not.toHaveProperty('stale');
    expect(s.value!.history).toEqual(history);
    expect(s.value!.value).toBe(72.1);

    expect(live.problems.value.map((i) => i.id)).toContain('backup-failed');
    ws.push({
      op: 'diff',
      id: ws.subId('inbox'),
      seq: 2004,
      patch: [{ op: 'replace', path: toPointer('inbox', 'incidents', 'backup-failed', 'status'), value: 'resolved' }],
    });
    await nextTick();
    expect(live.problems.value.map((i) => i.id)).not.toContain('backup-failed');
  });

  it('follows the hub clock when demo time is far from the browser’s', () => {
    vi.setSystemTime(new Date('2026-10-05T09:00:00Z'));
    const { live } = liveWithFixtures();
    live.tick();
    expect(new Date(live.now.value).toISOString()).toBe('2026-10-03T14:05:00.000Z');
  });

  it('goes stale when the hub drops, and live again after reconnecting', () => {
    const { live, ws } = liveWithFixtures();
    ws.drop();
    live.tick();
    expect(live.conn.status).toBe('reconnecting');
    expect(live.stale.value).toBe(true);
    expect(live.problems.value).toHaveLength(8); // last-known values stay
    vi.advanceTimersByTime(live.conn.nextRetryAt! - Date.now());
    FakeSocket.last.handshake(docs(withIncidents));
    live.tick();
    expect(live.conn.status).toBe('live');
    expect(live.stale.value).toBe(false);
  });

  it('re-checks the session when reconnecting keeps failing', async () => {
    const checkSession = vi.fn().mockResolvedValue(false);
    const live = createLive({ url: 'ws://hub.test/ws', createSocket: factory, tick: false, checkSession });
    live.start();
    FakeSocket.last.drop();
    vi.advanceTimersByTime(live.conn.nextRetryAt! - Date.now());
    FakeSocket.last.drop();
    await vi.waitFor(() => expect(checkSession).toHaveBeenCalled());
    await vi.waitFor(() => expect(live.conn.status).toBe('stopped'));
  });
});
