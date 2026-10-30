/**
 * Live state: the realtime client feeding one reactive document with the
 * `graph`, `metrics`, `inbox` and `tasks` topics, plus connection status and
 * the hub's clock. `fixes` is the per-incident state of actions run on the
 * engine (stores/actions.ts).
 *
 * Demo and real hubs fill the same topics, and the Demo Home's fixes run on
 * the engine like any action; the only difference the page sees is `demo`
 * (from GET /api/demo): times in the Demo Home's display zone.
 * `restore()` fills the document from a last-known snapshot when the page
 * opens while the hub is unreachable; the first live snapshot replaces it.
 * `hydrate()` fills it from the snapshot the hub inlined into index.html,
 * which is current: no skeletons, no stale treatment, and the socket's
 * snapshots supersede it in place.
 *
 * Plain Vue reactivity. Patches mutate `doc` in place, so a metrics diff only
 * re-renders the widgets that read the series it touched.
 */
import { computed, inject, provide, reactive, ref, type InjectionKey } from 'vue';
import { RealtimeClient, sameOriginWs, type ConnectionInfo, type WebSocketFactory } from '../realtime/client';
import type { BackoffOptions } from '../realtime/backoff';
import type { GraphDoc, InboxDoc, LiveDoc, MetricsDoc } from '../model/types';
import { openProblems, openRepairs } from '../model/inbox';
import { rewind } from '../model/history';
import { cursor } from './cursor';

export const TOPICS = ['graph', 'metrics', 'inbox', 'tasks'] as const;

export type FixState = 'sending' | 'accepted' | 'failed';

export interface FixStatus {
  state: FixState;
  error?: string;
  /** The incident phase when the fix was accepted, to show progress. */
  phase?: string;
  /** The engine's task working on it (stores/actions.ts). */
  task?: string;
  /** Refused as unavailable: the instance whose recipe grants the action. */
  recipe?: string;
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
    // Opened on a snapshot and nothing has arrived since: that snapshot is the last word.
    if (!conn.lastFrameAt && restoredAt.value !== undefined) return restoredAt.value + offset.value;
    const t = conn.offlineSince ?? (conn.lastFrameAt || undefined);
    return t === undefined ? undefined : t + offset.value;
  });
  const fixes = reactive<Record<string, FixStatus>>({});
  /** GET /api/demo said demo mode: incidents' suggested actions run through the demo Fix endpoint. */
  const demo = ref(false);
  /** When the snapshot on screen was taken, if the page opened on one (browser clock). */
  const restoredAt = ref<number | undefined>(undefined);
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

  /** Fill empty topics from a last-known snapshot. Live snapshots replace these as they arrive. */
  function restore(snap: { savedAt: number; clockOffset?: number; graph?: GraphDoc; metrics?: MetricsDoc; inbox?: InboxDoc }): void {
    if (!doc.graph && snap.graph) doc.graph = snap.graph;
    if (!doc.metrics && snap.metrics) doc.metrics = snap.metrics;
    if (!doc.inbox && snap.inbox) {
      doc.inbox = snap.inbox;
      // The stored inbox's `now` is old; the hub's clock skew at save time is what still holds.
      lastInboxNow = snap.inbox.now;
    }
    offset.value = snap.clockOffset ?? 0;
    restoredAt.value = snap.savedAt;
  }

  /** Fill the document from the hub's first-paint snapshot (lib/inline.ts): current values, so not marked last-known. */
  function hydrate(snap: { graph?: GraphDoc; metrics?: MetricsDoc; inbox?: InboxDoc }): void {
    if (snap.graph) doc.graph = snap.graph;
    if (snap.metrics) doc.metrics = snap.metrics;
    if (snap.inbox) {
      doc.inbox = snap.inbox;
      syncClock();
    }
  }

  const graph = computed<GraphDoc | undefined>(() => doc.graph);
  /** The metrics, or, while the Pulse strip pins a moment, as they were then (model/history.ts → rewind). */
  const metrics = computed<MetricsDoc | undefined>(() => {
    const m = doc.metrics;
    const c = cursor.value;
    return m && c?.source === 'pulse' ? rewind(m, c.at, Date.now()) : m;
  });
  const inbox = computed<InboxDoc | undefined>(() => doc.inbox);
  const problems = computed(() => openProblems(doc.inbox));
  const repairs = computed(() => openRepairs(doc.inbox));

  /** Values on screen are last-known (hub unreachable or silent). */
  const stale = computed(() => {
    void conn.lastFrameAt;
    // A stored snapshot is last-known until the hub says otherwise.
    if (restoredAt.value !== undefined && !conn.lastFrameAt) return true;
    return conn.status !== 'connecting' && client.isStale(tickNow.value);
  });
  /** Nothing has ever arrived: show skeletons, not zeros. */
  const loading = computed(() => !doc.graph && conn.status !== 'live');

  /**
   * Watch one resource closely while something holds it (a sheet open on
   * it): the hub reads it as interest (`metrics:resource/<id>`), and sources
   * publish its costly series, such as a container's stats, into `metrics`
   * meanwhile. Counted, so two holders share one subscription. Returns the
   * release.
   */
  const watching = new Map<string, number>();
  function watchResource(id: string): () => void {
    const topic = `metrics:resource/${id}`;
    const n = watching.get(topic) ?? 0;
    watching.set(topic, n + 1);
    if (n === 0) client.watch(topic);
    let released = false;
    return () => {
      if (released) return;
      released = true;
      const left = (watching.get(topic) ?? 1) - 1;
      if (left > 0) {
        watching.set(topic, left);
        return;
      }
      watching.delete(topic);
      client.unwatch(topic);
    };
  }

  return {
    doc,
    conn,
    client,
    now,
    wall,
    offlineAt,
    fixes,
    demo,
    restoredAt,
    restore,
    hydrate,
    /** Hub clock minus browser clock, for the snapshot. */
    clockOffset: () => offset.value,
    graph,
    metrics,
    inbox,
    problems,
    repairs,
    stale,
    loading,
    start,
    stop,
    setHubTime,
    watchResource,
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
