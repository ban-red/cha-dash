import type { WebSocketLike } from '../src/realtime/client';

/** A scriptable stand-in for the browser WebSocket, playing the hub's side. */
export class FakeSocket implements WebSocketLike {
  static all: FakeSocket[] = [];
  static reset(): void {
    FakeSocket.all = [];
  }
  static get last(): FakeSocket {
    const s = FakeSocket.all[FakeSocket.all.length - 1];
    if (!s) throw new Error('no socket yet');
    return s;
  }

  readonly url: string;
  readyState = 0;
  sent: Record<string, unknown>[] = [];
  closed = false;
  onopen: ((ev: unknown) => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: ((ev: { code?: number; reason?: string }) => void) | null = null;
  onerror: ((ev: unknown) => void) | null = null;

  constructor(url: string) {
    this.url = url;
    FakeSocket.all.push(this);
  }

  send(data: string): void {
    this.sent.push(JSON.parse(data) as Record<string, unknown>);
  }

  close(): void {
    this.closed = true;
    this.readyState = 3;
  }

  // ── the hub's side ──
  open(): void {
    this.readyState = 1;
    this.onopen?.({});
  }

  push(frame: unknown): void {
    this.onmessage?.({ data: JSON.stringify(frame) });
  }

  drop(code = 1006): void {
    this.readyState = 3;
    this.onclose?.({ code });
  }

  /** Frames the client sent with this op. */
  ops(op: string): Record<string, unknown>[] {
    return this.sent.filter((f) => f['op'] === op);
  }

  /** The sub id the client used for a topic (the latest one). */
  subId(topic: string): number {
    const subs = this.ops('sub').filter((f) => (f['topics'] as string[])[0] === topic);
    const last = subs[subs.length - 1];
    if (!last) throw new Error(`no sub for ${topic}`);
    return last['id'] as number;
  }

  /** Answer the handshake and every subscription with a snapshot. */
  handshake(data: Record<string, unknown>, ping = 30): void {
    this.open();
    this.push({ op: 'hello', v: 1, epoch: 'e1', server: 'test', ping });
    for (const sub of this.ops('sub')) {
      const topic = (sub['topics'] as string[])[0]!;
      if (topic in data) this.push({ op: 'snap', id: sub['id'], seq: 1, data: { [topic]: data[topic] } });
    }
  }
}

export const factory = (url: string) => new FakeSocket(url);
