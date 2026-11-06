/**
 * A fake hub log stream (plan 16 · 4): answers fetch for the logs routes
 * with a body the test writes into, as `text/event-stream` chunks, and
 * records whether the page aborted. Other requests go to whatever fetch was
 * stubbed before (fakeFetch, say).
 */
import { vi } from 'vitest';

export interface FakeStream {
  url: URL;
  /** The page aborted the request (closed the view). */
  aborted: boolean;
  /** Raw text, split wherever the test likes. */
  send(text: string): void;
  line(l: { stream?: 'stdout' | 'stderr'; line: string; at?: string; service?: string }): void;
  ping(): void;
  end(reason: string, message?: string): void;
  /** The hub closes without an end event. */
  close(): void;
}

type Reply = { status: number; body: unknown } | undefined;

export function fakeLogStreams(opts: { match?: RegExp; reply?: (url: URL) => Reply } = {}) {
  const match = opts.match ?? /\/logs(\?|$)/;
  const streams: FakeStream[] = [];
  const base = globalThis.fetch;
  const fn = vi.fn(async (input: string, init?: RequestInit) => {
    const url = new URL(input, 'http://hub.test');
    if (!match.test(url.pathname + url.search)) return base(input, init);
    const early = opts.reply?.(url);
    if (early) return new Response(JSON.stringify(early.body), { status: early.status, headers: { 'Content-Type': 'application/json' } });
    const queue: string[] = [];
    let closed = false;
    let wake: (() => void) | undefined;
    const poke = () => {
      wake?.();
      wake = undefined;
    };
    const s: FakeStream = {
      url,
      aborted: false,
      send: (text) => {
        if (closed) return;
        queue.push(text);
        poke();
      },
      line: (l) => s.send(`event: line\ndata: ${JSON.stringify({ stream: 'stdout', ...l })}\n\n`),
      ping: () => s.send(': ping\n\n'),
      end: (reason, message) => {
        s.send(`event: end\ndata: ${JSON.stringify({ reason, ...(message ? { message } : {}) })}\n\n`);
        s.close();
      },
      close: () => {
        closed = true;
        poke();
      },
    };
    init?.signal?.addEventListener('abort', () => {
      s.aborted = true;
      s.close();
    });
    streams.push(s);
    const encoder = new TextEncoder();
    const reader = {
      async read(): Promise<{ done: boolean; value?: Uint8Array }> {
        while (!queue.length && !closed) await new Promise<void>((r) => (wake = r));
        if (queue.length) return { done: false, value: encoder.encode(queue.shift()!) };
        return { done: true };
      },
      async cancel(): Promise<void> {
        s.close();
      },
    };
    return { ok: true, status: 200, headers: new Headers({ 'Content-Type': 'text/event-stream' }), body: { getReader: () => reader }, text: async () => '' } as unknown as Response;
  });
  vi.stubGlobal('fetch', fn);
  return { fn, streams, last: () => streams.at(-1)! };
}
