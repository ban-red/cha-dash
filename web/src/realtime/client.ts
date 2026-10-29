/**
 * The realtime client: one WebSocket per tab, one subscription per topic.
 *
 * - Handshake: `hello` on open, then a `sub` per topic once the hub says hello.
 * - State: each topic's document lives at `doc[topic]`. A `snap` replaces it;
 *   a `diff` is an RFC 6902 patch against the whole `doc` (the hub prefixes
 *   paths with the topic, e.g. `/graph/resources/…`).
 * - Resync: a `snap` with `resync: true` replaces state. If a patch doesn't
 *   apply, the client re-subscribes that topic, which always starts with a
 *   fresh snapshot.
 * - Liveness: the hub pings every `ping` seconds. Silence for longer than
 *   `watchdogFactor × ping` drops the socket. `ping()` asks the hub for a
 *   `pong` (used when the tab becomes visible or the network comes back).
 * - Reconnect: exponential backoff with jitter (`backoff.ts`), reset on the
 *   next successful hello.
 *
 * One subscription per topic (rather than one with every topic) keeps topics
 * independent: a hub that doesn't serve `inbox` still serves `graph`.
 *
 * `watch(topic)` adds a topic for as long as something needs it (a sheet's
 * `metrics:resource/<id>`); it survives reconnects until `unwatch`, and
 * never holds up `live`.
 */
import { applyPatch, PatchError, type PatchOp } from '../lib/jsonpatch';
import { backoffDelay, type BackoffOptions } from './backoff';
import { PROTOCOL_VERSION, parseFrame, type ClientFrame, type ServerFrame } from './protocol';

export type ConnectionStatus =
  /** Not started. */
  | 'idle'
  /** First connection attempt. */
  | 'connecting'
  /** Socket open, every topic has its first snapshot (or is unavailable). */
  | 'live'
  /** Lost the hub; retrying with backoff. */
  | 'reconnecting'
  /** The hub speaks another protocol version. Reloading is the fix. */
  | 'incompatible'
  | 'stopped';

export type TopicStatus = 'pending' | 'live' | 'unavailable';

export interface TopicState {
  status: TopicStatus;
  seq: number;
  error?: string;
}

export interface ConnectionInfo {
  status: ConnectionStatus;
  /** Consecutive failed attempts since the last good hello. */
  attempt: number;
  /** When the next reconnect attempt fires (ms epoch), while reconnecting. */
  nextRetryAt: number | undefined;
  /** The last time any frame arrived (ms epoch); 0 before the first. */
  lastFrameAt: number;
  /** Since when the hub has been unreachable (the last frame before the loss). */
  offlineSince: number | undefined;
  /** The hub's ping interval in ms, from its hello. */
  pingMs: number;
  epoch: string | undefined;
  topics: Record<string, TopicState>;
}

export interface WebSocketLike {
  readonly readyState: number;
  onopen: ((ev: unknown) => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
  onclose: ((ev: { code?: number; reason?: string }) => void) | null;
  onerror: ((ev: unknown) => void) | null;
  send(data: string): void;
  close(code?: number, reason?: string): void;
}

export type WebSocketFactory = (url: string) => WebSocketLike;

export interface RealtimeClientOptions {
  url: string | (() => string);
  topics: readonly string[];
  /** Holds each topic's document at `doc[topic]`. Pass a Vue `reactive()` object. */
  doc: Record<string, unknown>;
  createSocket?: WebSocketFactory;
  backoff?: BackoffOptions;
  /** Silence longer than this many ping intervals drops the socket. Default 2.5. */
  watchdogFactor?: number;
  /** How long `ping()` waits for any frame before giving up on the socket. Default 5 s. */
  pongTimeout?: number;
  /** Deadline for the hub's hello after the socket opens. Default 10 s. */
  helloTimeout?: number;
  now?: () => number;
  onChange?: (info: ConnectionInfo) => void;
  /** After every snapshot or applied patch. */
  onUpdate?: (topic: string, kind: 'snap' | 'resync' | 'diff') => void;
  onError?: (code: string, msg: string, topic?: string) => void;
}

const OPEN = 1;

export class RealtimeClient {
  readonly info: ConnectionInfo;
  private readonly opts: RealtimeClientOptions;
  private readonly now: () => number;
  private ws: WebSocketLike | undefined;
  private everLive = false;
  private nextSubId = 1;
  private subs = new Map<number, string>();
  /** Topics added with watch(), beyond opts.topics. */
  private extra = new Set<string>();
  /** The hub said hello on the current socket, so subs may go out. */
  private ready = false;
  private topicSub = new Map<string, number>();
  /** Topics settled (snapshot or refusal) on the current socket. */
  private synced = new Set<string>();
  private lastSeq = 0;
  private retryTimer: ReturnType<typeof setTimeout> | undefined;
  private watchdogTimer: ReturnType<typeof setTimeout> | undefined;
  private pongTimer: ReturnType<typeof setTimeout> | undefined;

  constructor(opts: RealtimeClientOptions) {
    this.opts = opts;
    this.now = opts.now ?? Date.now;
    this.info = {
      status: 'idle',
      attempt: 0,
      nextRetryAt: undefined,
      lastFrameAt: 0,
      offlineSince: undefined,
      pingMs: 30_000,
      epoch: undefined,
      topics: Object.fromEntries(opts.topics.map((t) => [t, { status: 'pending', seq: 0 } satisfies TopicState])),
    };
  }

  get status(): ConnectionStatus {
    return this.info.status;
  }

  start(): void {
    if (this.info.status === 'idle' || this.info.status === 'stopped') this.connect();
  }

  stop(): void {
    this.clearTimers();
    this.dropSocket();
    this.set({ status: 'stopped', nextRetryAt: undefined });
  }

  /** Skip the backoff wait (the banner's "Retry now"). */
  reconnectNow(): void {
    if (this.info.status === 'stopped' || this.info.status === 'incompatible') return;
    this.clearTimers();
    this.dropSocket();
    this.connect();
  }

  /**
   * Check the connection now: send a ping and expect any frame within
   * `pongTimeout`. Reconnects straight away if there's no open socket.
   */
  ping(): void {
    if (this.info.status === 'stopped' || this.info.status === 'incompatible' || this.info.status === 'idle') return;
    if (!this.ws || this.ws.readyState !== OPEN) {
      this.reconnectNow();
      return;
    }
    this.send({ op: 'ping' });
    clearTimeout(this.pongTimer);
    this.pongTimer = setTimeout(() => this.lost(), this.opts.pongTimeout ?? 5000);
  }

  /** True when the page shouldn't trust its values as live. */
  isStale(at = this.now()): boolean {
    if (this.info.status !== 'live') return this.everLive || this.info.status === 'reconnecting';
    return at - this.info.lastFrameAt > this.info.pingMs * (this.opts.watchdogFactor ?? 2.5);
  }

  private connect(): void {
    this.set({ status: this.everLive || this.info.attempt > 0 ? 'reconnecting' : 'connecting', nextRetryAt: undefined });
    const url = typeof this.opts.url === 'function' ? this.opts.url() : this.opts.url;
    let ws: WebSocketLike;
    try {
      ws = (this.opts.createSocket ?? defaultSocket)(url);
    } catch {
      this.scheduleRetry();
      return;
    }
    this.ws = ws;
    ws.onopen = () => {
      if (this.ws !== ws) return;
      const hello: ClientFrame = { op: 'hello', v: PROTOCOL_VERSION };
      if (this.lastSeq > 0) hello.resume = this.lastSeq;
      this.send(hello);
      this.armWatchdog(this.opts.helloTimeout ?? 10_000);
    };
    ws.onmessage = (ev) => {
      if (this.ws !== ws) return;
      this.receive(typeof ev.data === 'string' ? ev.data : String(ev.data));
    };
    ws.onclose = () => {
      if (this.ws !== ws) return;
      this.ws = undefined;
      this.lost();
    };
    ws.onerror = () => {
      /* a close event follows */
    };
    this.armWatchdog((this.opts.helloTimeout ?? 10_000) + 5000);
  }

  private receive(text: string): void {
    const at = this.now();
    clearTimeout(this.pongTimer);
    this.pongTimer = undefined;
    this.info.lastFrameAt = at;
    this.armWatchdog(this.info.pingMs * (this.opts.watchdogFactor ?? 2.5));
    const f = parseFrame(text);
    if (f) this.handle(f);
    this.emit();
  }

  private handle(f: ServerFrame): void {
    switch (f.op) {
      case 'hello': {
        if (f.v !== PROTOCOL_VERSION) {
          this.incompatible(`the hub speaks protocol v${f.v}`);
          return;
        }
        this.info.pingMs = Math.max(1, f.ping || 30) * 1000;
        this.info.epoch = f.epoch;
        this.info.attempt = 0;
        this.armWatchdog(this.info.pingMs * (this.opts.watchdogFactor ?? 2.5));
        this.subs.clear();
        this.topicSub.clear();
        this.synced.clear();
        this.ready = true;
        for (const topic of [...this.opts.topics, ...this.extra]) this.subscribe(topic);
        return;
      }
      case 'snap': {
        const topic = this.subs.get(f.id);
        if (topic === undefined) return; // a subscription we already dropped
        this.opts.doc[topic] = f.data[topic];
        this.lastSeq = Math.max(this.lastSeq, f.seq);
        this.info.topics[topic] = { status: 'live', seq: f.seq };
        this.synced.add(topic);
        this.maybeLive();
        this.opts.onUpdate?.(topic, f.resync ? 'resync' : 'snap');
        return;
      }
      case 'diff': {
        const topic = this.subs.get(f.id);
        if (topic === undefined) return;
        try {
          applyPatch(this.opts.doc, f.patch);
        } catch (err) {
          if (!(err instanceof PatchError)) throw err;
          this.opts.onError?.('patch_failed', err.message, topic);
          this.resubscribe(topic);
          return;
        }
        this.lastSeq = Math.max(this.lastSeq, f.seq);
        const ts = this.info.topics[topic];
        if (ts) ts.seq = f.seq;
        this.opts.onUpdate?.(topic, 'diff');
        return;
      }
      case 'error': {
        if (f.code === 'unsupported_version') {
          this.incompatible(f.msg);
          return;
        }
        const topic = f.id !== undefined ? this.subs.get(f.id) : undefined;
        this.opts.onError?.(f.code, f.msg, topic);
        if (topic === undefined || f.id === undefined) return;
        this.subs.delete(f.id);
        this.topicSub.delete(topic);
        if (f.code === 'duplicate_id') {
          this.subscribe(topic);
          return;
        }
        // unknown_topic, too_many_subs, bad_request: this hub won't serve it.
        this.info.topics[topic] = { status: 'unavailable', seq: 0, error: f.msg };
        this.synced.add(topic);
        this.maybeLive();
        return;
      }
      case 'ping':
      case 'pong':
        return;
    }
  }

  /** Subscribe to topic until unwatch(topic), across reconnects. */
  watch(topic: string): void {
    if (this.opts.topics.includes(topic) || this.extra.has(topic)) return;
    this.extra.add(topic);
    this.info.topics[topic] = { status: 'pending', seq: 0 };
    if (this.ready) this.subscribe(topic);
    this.emit();
  }

  /** Drop a topic added with watch(), and its document. */
  unwatch(topic: string): void {
    if (!this.extra.delete(topic)) return;
    const id = this.topicSub.get(topic);
    if (id !== undefined) {
      this.subs.delete(id);
      this.topicSub.delete(topic);
      this.send({ op: 'unsub', id });
    }
    this.synced.delete(topic);
    delete this.info.topics[topic];
    delete this.opts.doc[topic];
    this.emit();
  }

  private subscribe(topic: string): void {
    const id = this.nextSubId++;
    this.subs.set(id, topic);
    this.topicSub.set(topic, id);
    const prev = this.info.topics[topic];
    this.info.topics[topic] = { status: prev?.status === 'live' ? 'live' : 'pending', seq: prev?.seq ?? 0 };
    this.send({ op: 'sub', id, topics: [topic] });
  }

  private resubscribe(topic: string): void {
    const old = this.topicSub.get(topic);
    if (old !== undefined) {
      this.subs.delete(old);
      this.send({ op: 'unsub', id: old });
    }
    this.subscribe(topic);
  }

  /** Live once every topic has a snapshot (or a refusal) on the current socket. */
  private maybeLive(): void {
    if (this.info.status === 'live') return;
    if (!this.opts.topics.every((t) => this.synced.has(t))) return;
    this.everLive = true;
    this.set({ status: 'live', offlineSince: undefined, nextRetryAt: undefined, attempt: 0 });
  }

  private lost(): void {
    if (this.info.status === 'stopped' || this.info.status === 'incompatible') return;
    this.dropSocket();
    this.scheduleRetry();
  }

  private scheduleRetry(): void {
    clearTimeout(this.watchdogTimer);
    clearTimeout(this.pongTimer);
    clearTimeout(this.retryTimer);
    const delay = backoffDelay(this.info.attempt, this.opts.backoff);
    this.set({
      status: 'reconnecting',
      attempt: this.info.attempt + 1,
      nextRetryAt: this.now() + delay,
      offlineSince: this.info.offlineSince ?? (this.info.lastFrameAt || this.now()),
    });
    this.retryTimer = setTimeout(() => {
      this.retryTimer = undefined;
      this.connect();
    }, delay);
  }

  private incompatible(msg: string): void {
    this.clearTimers();
    this.dropSocket();
    this.opts.onError?.('unsupported_version', msg);
    this.set({ status: 'incompatible', nextRetryAt: undefined });
  }

  private armWatchdog(ms: number): void {
    clearTimeout(this.watchdogTimer);
    this.watchdogTimer = setTimeout(() => this.lost(), ms);
  }

  private dropSocket(): void {
    const ws = this.ws;
    this.ws = undefined;
    this.ready = false;
    this.subs.clear();
    this.topicSub.clear();
    this.synced.clear();
    clearTimeout(this.watchdogTimer);
    clearTimeout(this.pongTimer);
    if (!ws) return;
    ws.onopen = ws.onmessage = ws.onclose = ws.onerror = null;
    try {
      ws.close(1000, 'client reconnecting');
    } catch {
      /* already closed */
    }
  }

  private clearTimers(): void {
    clearTimeout(this.retryTimer);
    clearTimeout(this.watchdogTimer);
    clearTimeout(this.pongTimer);
    this.retryTimer = this.watchdogTimer = this.pongTimer = undefined;
  }

  private send(frame: ClientFrame): void {
    if (this.ws && this.ws.readyState === OPEN) this.ws.send(JSON.stringify(frame));
  }

  private set(patch: Partial<ConnectionInfo>): void {
    Object.assign(this.info, patch);
    this.emit();
  }

  private emit(): void {
    this.opts.onChange?.(this.info);
  }
}

function defaultSocket(url: string): WebSocketLike {
  return new WebSocket(url) as unknown as WebSocketLike;
}

/** Same-origin WebSocket URL for a path (CSP allows only 'self'). */
export function sameOriginWs(path: string, loc: Pick<Location, 'protocol' | 'host'> = location): string {
  return `${loc.protocol === 'https:' ? 'wss:' : 'ws:'}//${loc.host}${path}`;
}

export type { PatchOp };
