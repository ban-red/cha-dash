import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { RealtimeClient, sameOriginWs, type ConnectionInfo } from '../src/realtime/client';
import { backoffDelay } from '../src/realtime/backoff';
import { FakeSocket, factory } from './fake-socket';

const TOPICS = ['graph', 'metrics', 'inbox'];

function setup(extra: Partial<ConstructorParameters<typeof RealtimeClient>[0]> = {}) {
  const doc: Record<string, unknown> = {};
  const changes: ConnectionInfo['status'][] = [];
  const errors: string[] = [];
  const client = new RealtimeClient({
    url: 'ws://hub.test/ws',
    topics: TOPICS,
    doc,
    createSocket: factory,
    backoff: { base: 1000, max: 8000, random: () => 1 },
    onChange: (i) => {
      if (changes[changes.length - 1] !== i.status) changes.push(i.status);
    },
    onError: (code) => errors.push(code),
    ...extra,
  });
  return { client, doc, changes, errors };
}

const snapData = {
  graph: { resources: { 'pve:wren': { id: 'pve:wren', kind: 'host', health: 'ok' } }, relations: {} },
  metrics: { series: { 'pve:wren#cpu.pct': { value: 9, history: [8, 9] } } },
  inbox: { now: '2026-10-03T14:05:00Z', incidents: {} },
};

beforeEach(() => {
  FakeSocket.reset();
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

describe('handshake and subscriptions', () => {
  it('says hello first, then subscribes once per topic after the hub says hello', () => {
    const { client, doc, changes } = setup();
    client.start();
    const ws = FakeSocket.last;
    expect(ws.url).toBe('ws://hub.test/ws');
    ws.open();
    expect(ws.sent).toEqual([{ op: 'hello', v: 1 }]);
    ws.push({ op: 'hello', v: 1, epoch: 'e1', ping: 30 });
    expect(ws.ops('sub').map((f) => f['topics'])).toEqual([['graph'], ['metrics'], ['inbox']]);
    expect(new Set(ws.ops('sub').map((f) => f['id'])).size).toBe(3);
    expect(client.status).toBe('connecting');

    for (const t of TOPICS) ws.push({ op: 'snap', id: ws.subId(t), seq: 10, data: { [t]: snapData[t as keyof typeof snapData] } });
    expect(client.status).toBe('live');
    expect(doc['graph']).toEqual(snapData.graph);
    expect(client.info.topics['inbox']).toEqual({ status: 'live', seq: 10 });
    expect(changes).toEqual(['connecting', 'live']);
  });

  it('is live without a topic the hub refuses, and keeps the others', () => {
    const { client, doc, errors } = setup();
    client.start();
    const ws = FakeSocket.last;
    ws.open();
    ws.push({ op: 'hello', v: 1, epoch: 'e1', ping: 30 });
    ws.push({ op: 'snap', id: ws.subId('graph'), seq: 1, data: { graph: snapData.graph } });
    ws.push({ op: 'snap', id: ws.subId('metrics'), seq: 1, data: { metrics: snapData.metrics } });
    ws.push({ op: 'error', id: ws.subId('inbox'), code: 'unknown_topic', msg: 'realtime: unknown topic: "inbox"' });
    expect(client.status).toBe('live');
    expect(client.info.topics['inbox']?.status).toBe('unavailable');
    expect(doc['inbox']).toBeUndefined();
    expect(errors).toContain('unknown_topic');
  });

  it('stops for good when the hub speaks another protocol version', () => {
    const { client } = setup();
    client.start();
    const ws = FakeSocket.last;
    ws.open();
    ws.push({ op: 'error', code: 'unsupported_version', msg: 'this hub speaks protocol v2' });
    expect(client.status).toBe('incompatible');
    vi.advanceTimersByTime(120_000);
    expect(FakeSocket.all).toHaveLength(1);
  });
});

describe('watched topics', () => {
  const topic = 'metrics:resource/docker:i_d/container/db';

  it('subscribes a watched topic after hello and across reconnects, without holding up live', () => {
    const { client, doc } = setup();
    client.watch(topic);
    client.start();
    const ws = FakeSocket.last;
    ws.open();
    ws.push({ op: 'hello', v: 1, epoch: 'e1', ping: 30 });
    expect(ws.ops('sub').map((f) => f['topics'])).toEqual([['graph'], ['metrics'], ['inbox'], [topic]]);
    for (const t of TOPICS) ws.push({ op: 'snap', id: ws.subId(t), seq: 1, data: { [t]: snapData[t as keyof typeof snapData] } });
    expect(client.status).toBe('live');
    const series = { series: { 'docker:i_d/container/db#cpu.pct': { value: 3 } } };
    ws.push({ op: 'snap', id: ws.subId(topic), seq: 2, data: { [topic]: series } });
    expect(doc[topic]).toEqual(series);

    ws.drop();
    vi.advanceTimersByTime(1000);
    const next = FakeSocket.last;
    expect(next).not.toBe(ws);
    next.open();
    next.push({ op: 'hello', v: 1, epoch: 'e1', ping: 30 });
    expect(next.ops('sub').map((f) => f['topics'])).toContainEqual([topic]);
  });

  it('subscribes at once when already connected, and unwatch drops the sub and its document', () => {
    const { client, doc } = setup();
    client.start();
    const ws = FakeSocket.last;
    ws.open();
    ws.push({ op: 'hello', v: 1, epoch: 'e1', ping: 30 });
    client.watch(topic);
    client.watch(topic);
    expect(ws.ops('sub').filter((f) => (f['topics'] as string[])[0] === topic)).toHaveLength(1);
    const id = ws.subId(topic);
    ws.push({ op: 'snap', id, seq: 2, data: { [topic]: { series: {} } } });
    client.unwatch(topic);
    expect(ws.ops('unsub')).toEqual([{ op: 'unsub', id }]);
    expect(doc[topic]).toBeUndefined();
    expect(client.info.topics[topic]).toBeUndefined();
    ws.push({ op: 'snap', id, seq: 3, data: { [topic]: { series: {} } } });
    expect(doc[topic]).toBeUndefined();
    client.unwatch('metrics');
    expect(ws.ops('unsub')).toHaveLength(1);
  });
});

describe('diffs and resync', () => {
  function live() {
    const s = setup();
    s.client.start();
    FakeSocket.last.handshake(structuredClone(snapData));
    return { ...s, ws: FakeSocket.last };
  }

  it('applies RFC 6902 patches prefixed with the topic', () => {
    const { doc, ws, client } = live();
    ws.push({
      op: 'diff',
      id: ws.subId('graph'),
      seq: 11,
      patch: [
        { op: 'replace', path: '/graph/resources/pve:wren', value: { id: 'pve:wren', kind: 'host', health: 'down' } },
        { op: 'add', path: '/graph/resources/pve:owl', value: { id: 'pve:owl', kind: 'host' } },
      ],
    });
    ws.push({ op: 'diff', id: ws.subId('metrics'), seq: 12, patch: [{ op: 'replace', path: '/metrics/series/pve:wren#cpu.pct', value: { value: 12, history: [9, 12] } }] });
    const g = doc['graph'] as typeof snapData.graph & { resources: Record<string, { health?: string }> };
    expect(g.resources['pve:wren']?.health).toBe('down');
    expect(Object.keys(g.resources)).toEqual(['pve:wren', 'pve:owl']);
    expect((doc['metrics'] as typeof snapData.metrics).series['pve:wren#cpu.pct'].value).toBe(12);
    expect(client.info.topics['metrics']?.seq).toBe(12);
  });

  it('replaces state on a resync snapshot', () => {
    const { doc, ws } = live();
    const fresh = { resources: {}, relations: {} };
    ws.push({ op: 'snap', id: ws.subId('graph'), seq: 90, data: { graph: fresh }, resync: true });
    expect(doc['graph']).toEqual(fresh);
  });

  it('re-subscribes a topic whose patch does not apply, and recovers from the new snapshot', () => {
    const { doc, ws, errors, client } = live();
    const oldId = ws.subId('inbox');
    ws.push({ op: 'diff', id: oldId, seq: 20, patch: [{ op: 'replace', path: '/inbox/incidents/nope', value: {} }] });
    expect(errors).toContain('patch_failed');
    expect(ws.ops('unsub')).toEqual([{ op: 'unsub', id: oldId }]);
    const newId = ws.subId('inbox');
    expect(newId).not.toBe(oldId);
    // a late diff for the old subscription is ignored
    ws.push({ op: 'diff', id: oldId, seq: 21, patch: [{ op: 'add', path: '/inbox/x', value: 1 }] });
    expect((doc['inbox'] as Record<string, unknown>)['x']).toBeUndefined();
    const fresh = { now: '2026-10-03T14:06:00Z', incidents: { a: { id: 'a' } } };
    ws.push({ op: 'snap', id: newId, seq: 22, data: { inbox: fresh } });
    expect(doc['inbox']).toEqual(fresh);
    expect(client.status).toBe('live');
  });
});

describe('liveness and reconnect', () => {
  it('reconnects with exponential backoff and jitter, and resets after a good hello', () => {
    const { client, changes } = setup();
    client.start();
    FakeSocket.last.handshake(snapData);
    expect(client.status).toBe('live');

    FakeSocket.last.drop();
    expect(client.status).toBe('reconnecting');
    expect(client.info.offlineSince).toBeDefined();
    expect(client.info.nextRetryAt! - Date.now()).toBe(backoffDelay(0, { base: 1000, max: 8000, random: () => 1 }));

    // each failed attempt doubles the wait (random() = 1: the ceiling)
    const waits: number[] = [];
    for (let i = 0; i < 4; i++) {
      const wait = client.info.nextRetryAt! - Date.now();
      waits.push(wait);
      vi.advanceTimersByTime(wait);
      FakeSocket.last.drop();
    }
    expect(waits).toEqual([1000, 2000, 4000, 8000]);
    expect(client.info.attempt).toBe(5);

    vi.advanceTimersByTime(client.info.nextRetryAt! - Date.now());
    const ws = FakeSocket.last;
    ws.open();
    expect(ws.ops('hello')[0]).toEqual({ op: 'hello', v: 1, resume: 1 });
    ws.handshake(snapData);
    expect(client.status).toBe('live');
    expect(client.info.attempt).toBe(0);
    expect(client.info.offlineSince).toBeUndefined();
    expect(changes).toEqual(['connecting', 'live', 'reconnecting', 'live']);
  });

  it('jitter spreads the delay between half and all of the ceiling', () => {
    expect(backoffDelay(3, { base: 1000, random: () => 0 })).toBe(4000);
    expect(backoffDelay(3, { base: 1000, random: () => 1 })).toBe(8000);
    expect(backoffDelay(30, { base: 1000, max: 30_000, random: () => 0.5 })).toBe(22_500);
  });

  it('drops a silent socket after 2.5 ping intervals; server pings keep it alive', () => {
    const { client } = setup();
    client.start();
    const ws = FakeSocket.last;
    ws.handshake(snapData, 10);
    vi.advanceTimersByTime(20_000);
    ws.push({ op: 'ping' });
    vi.advanceTimersByTime(20_000);
    expect(client.status).toBe('live');
    expect(client.isStale()).toBe(false);
    vi.advanceTimersByTime(6000); // 26 s of silence > 25 s
    expect(client.status).toBe('reconnecting');
    expect(ws.closed).toBe(true);
    expect(client.isStale()).toBe(true);
  });

  it('ping() expects a pong and reconnects when none comes', () => {
    const { client } = setup();
    client.start();
    const ws = FakeSocket.last;
    ws.handshake(snapData);
    client.ping();
    expect(ws.ops('ping')).toHaveLength(1);
    ws.push({ op: 'pong' });
    vi.advanceTimersByTime(6000);
    expect(client.status).toBe('live');

    client.ping();
    vi.advanceTimersByTime(5001);
    expect(client.status).toBe('reconnecting');
  });

  it('reconnectNow skips the wait', () => {
    const { client } = setup();
    client.start();
    FakeSocket.last.handshake(snapData);
    FakeSocket.last.drop();
    expect(FakeSocket.all).toHaveLength(1);
    client.reconnectNow();
    expect(FakeSocket.all).toHaveLength(2);
  });

  it('stop() closes the socket and never retries', () => {
    const { client } = setup();
    client.start();
    const ws = FakeSocket.last;
    ws.handshake(snapData);
    client.stop();
    expect(ws.closed).toBe(true);
    vi.advanceTimersByTime(60_000);
    expect(FakeSocket.all).toHaveLength(1);
    expect(client.status).toBe('stopped');
  });

  it('builds a same-origin URL', () => {
    expect(sameOriginWs('/ws', { protocol: 'https:', host: 'dash.home.example' })).toBe('wss://dash.home.example/ws');
    expect(sameOriginWs('/ws', { protocol: 'http:', host: 'localhost:5173' })).toBe('ws://localhost:5173/ws');
  });
});
