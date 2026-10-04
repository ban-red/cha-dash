/**
 * Live state: the realtime client feeding one reactive document with the
 * `graph`, `metrics` and `inbox` topics, plus connection status, the hub's
 * clock, and the demo actions (Fix).
 *
 * Plain Vue reactivity. Patches mutate `doc` in place, so a metrics diff only
 * re-renders the widgets that read the series it touched.
 */
import { computed, inject, provide, reactive, ref, type InjectionKey } from 'vue';
import { ApiError, api } from '../lib/api';
import { RealtimeClient, sameOriginWs, type ConnectionInfo, type WebSocketFactory } from '../realtime/client';
import type { BackoffOptions } from '../realtime/backoff';
import type { GraphDoc, InboxDoc, LiveDoc, MetricsDoc } from '../model/types';
import { openProblems, openRepairs } from '../model/inbox';

export const TOPICS = ['graph', 'metrics', 'inbox'] as const;

export type FixState = 'sending' | 'accepted' | 'failed';

export interface FixStatus {
  state: FixState;
  error?: string;
  /** The incident phase when the fix was accepted, to show progress. */
  phase?: string;
}

export interface LiveOptions {
  url?: string;
  createSocket?: WebSocketFactory;
  backoff?: BackoffOptions;
  now?: () => number;
  /** Re-check the session when the socket keeps failing (a 401 upgrade looks like a network error). */
  checkSession?: () => Promise<boolean>;
  /** Tick the clock every second. Off in tests. */
  tick?: boolean;
}

export function createLive(opts: LiveOptions = {}) {
  const wallNow = opts.now ?? Date.now;
  const doc = reactive<LiveDoc>({});
  const conn = reactive<ConnectionInfo>({
    status: 'idle',
    attempt: 0,
    nextRetryAt: undefined,
    lastFrameAt: 0,
    offlineSince: undefined,
    pingMs: 30_000,
    epoch: undefined,
    topics: {},
  });

  /** Hub clock minus browser clock, from the inbox's `now` (demo time may differ). */
  const offset = ref(0);
  const tickNow = ref(wallNow());
  const now = computed(() => tickNow.value + offset.value);
  /** The browser's clock, ticking; connection timestamps use it. */
  const wall = computed(() => tickNow.value);
  /** When the hub went quiet, on the hub's clock (connection times are browser time). */
  const offlineAt = computed(() => {
    const t = conn.offlineSince ?? (conn.lastFrameAt || undefined);
    return t === undefined ? undefined : t + offset.value;
  });
  const fixes = reactive<Record<string, FixStatus>>({});
  let lastInboxNow = '';
  let checking = false;

  const client = new RealtimeClient({
    url: opts.url ?? (() => sameOriginWs('/ws')),
    topics: TOPICS,
    doc,
    ...(opts.createSocket ? { createSocket: opts.createSocket } : {}),
    ...(opts.backoff ? { backoff: opts.backoff } : {}),
    now: wallNow,
    onChange(info) {
      Object.assign(conn, { ...info, topics: { ...info.topics } });
      if (info.status === 'reconnecting' && info.attempt >= 2 && opts.checkSession && !checking) {
        checking = true;
        void opts.checkSession().then((ok) => {
          checking = false;
          if (!ok) stop();
        });
      }
    },
    onUpdate(topic) {
      if (topic === 'inbox') syncClock();
    },
  });

  function syncClock(): void {
    const n = doc.inbox?.now;
    if (!n || n === lastInboxNow) return;
    lastInboxNow = n;
    const hub = Date.parse(n);
    if (Number.isNaN(hub)) return;
    const skew = hub - wallNow();
    // The hub stamps `now` when it builds the document; ignore small drift.
    offset.value = Math.abs(skew) > 90_000 ? skew : 0;
  }

  /** Use the demo endpoint's `now` before the inbox arrives. */
  function setHubTime(iso: string): void {
    const t = Date.parse(iso);
    if (Number.isNaN(t)) return;
    const skew = t - wallNow();
    offset.value = Math.abs(skew) > 90_000 ? skew : 0;
  }

  let timer: ReturnType<typeof setInterval> | undefined;
  const onVisible = () => {
    if (document.visibilityState === 'visible') client.ping();
  };
  const onOnline = () => client.ping();

  function start(): void {
    client.start();
    if (opts.tick !== false && timer === undefined) {
      timer = setInterval(() => (tickNow.value = wallNow()), 1000);
    }
    if (typeof document !== 'undefined') document.addEventListener('visibilitychange', onVisible);
    if (typeof window !== 'undefined') window.addEventListener('online', onOnline);
  }

  function stop(): void {
    client.stop();
    clearInterval(timer);
    timer = undefined;
    if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', onVisible);
    if (typeof window !== 'undefined') window.removeEventListener('online', onOnline);
  }

  const graph = computed<GraphDoc | undefined>(() => doc.graph);
  const metrics = computed<MetricsDoc | undefined>(() => doc.metrics);
  const inbox = computed<InboxDoc | undefined>(() => doc.inbox);
  const problems = computed(() => openProblems(doc.inbox));
  const repairs = computed(() => openRepairs(doc.inbox));

  /** Values on screen are last-known (hub unreachable or silent). */
  const stale = computed(() => {
    void conn.lastFrameAt;
    return conn.status !== 'connecting' && client.isStale(tickNow.value);
  });
  /** Nothing has ever arrived: show skeletons, not zeros. */
  const loading = computed(() => !doc.graph && conn.status !== 'live');

  /**
   * Press an open incident's suggested action (demo mode). 202 means accepted;
   * the resolution arrives as inbox diffs.
   */
  async function fix(id: string): Promise<void> {
    fixes[id] = { state: 'sending' };
    try {
      await api(`/api/demo/incidents/${encodeURIComponent(id)}/fix`, { method: 'POST' });
      const phase = doc.inbox?.incidents?.[id]?.phase;
      fixes[id] = phase !== undefined ? { state: 'accepted', phase } : { state: 'accepted' };
    } catch (err) {
      let error = 'The hub couldn’t start that. Try again.';
      if (err instanceof ApiError) {
        if (err.status === 404) error = 'Actions run only in demo mode for now.';
        else if (err.status === 409) error = 'That’s already being handled, or it has resolved.';
        else if (err.status === 403) error = 'Only the owner can run actions.';
        else if (err.status === 0) error = 'Can’t reach the hub.';
        else if (err.status === 429) error = 'Too many actions at once. Try again shortly.';
      }
      fixes[id] = { state: 'failed', error };
      throw err;
    }
  }

  return {
    doc,
    conn,
    client,
    now,
    wall,
    offlineAt,
    fixes,
    graph,
    metrics,
    inbox,
    problems,
    repairs,
    stale,
    loading,
    start,
    stop,
    fix,
    setHubTime,
    retry: () => client.reconnectNow(),
    /** For tests: move the ticking clock. */
    tick: (ms?: number) => (tickNow.value = ms ?? wallNow()),
  };
}

export type Live = ReturnType<typeof createLive>;

const KEY: InjectionKey<Live> = Symbol('live');
export const provideLive = (l: Live) => provide(KEY, l);
export function useLive(): Live {
  const l = inject(KEY);
  if (!l) throw new Error('useLive() outside the app');
  return l;
}
