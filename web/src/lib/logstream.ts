/**
 * Live logs from the hub (plan 16 · 4): the event-stream parser, the line
 * buffer the log view keeps, and the request that follows a stream.
 *
 * Routes: a container's `GET /api/integrations/instances/{id}/logs?resource=…`
 * and a stack's `GET /api/stacks/{node}/{stack}/logs`, both with `tail` and
 * `follow=1`. The answer is `text/event-stream`: `line` events
 * (`{stream, line, at?, service?}`), one `end` event (`{reason, message}`)
 * before the hub closes, and `: ping` comments to ignore. Before the stream
 * starts, an error is the hub's JSON envelope with a status.
 *
 * Transport: fetch with a ReadableStream, not EventSource. fetch can be
 * aborted when the view goes, reads the hub's JSON error with its status
 * (EventSource only sees "error"), never reconnects on its own (EventSource
 * would, replaying the tail each time), and a test can feed it chunks.
 */

export type LogStreamName = 'stdout' | 'stderr';

export interface LogLine {
  /** Its place in the stream since the view opened: a stable key. */
  n: number;
  stream: LogStreamName;
  line: string;
  at?: string;
  /** The compose service, on a stack's stream. */
  service?: string;
}

export type EndReason = 'done' | 'limit' | 'closed' | 'error';

export interface LogEnd {
  reason: EndReason;
  message: string;
}

/** The most lines a view keeps, and what Download saves. */
export const LOG_CAP = 10_000;
/** How many lines a stream starts with. */
export const DEFAULT_TAIL = 200;

export interface SseEvent {
  event: string;
  data: string;
}

/**
 * The event-stream format, as far as the hub uses it: `event:` and `data:`
 * fields, a blank line ends an event, `:` starts a comment. Chunks may split
 * anywhere, even between a CR and its LF.
 */
export class SseParser {
  private rest = '';
  private event = '';
  private data: string[] = [];

  push(chunk: string): SseEvent[] {
    const out: SseEvent[] = [];
    let text = this.rest + chunk;
    // A CR at the very end may be half of a CRLF: wait for the next chunk.
    let held = '';
    if (text.endsWith('\r')) {
      held = '\r';
      text = text.slice(0, -1);
    }
    const lines = text.split(/\r\n|\r|\n/);
    this.rest = lines.pop()! + held;
    for (const line of lines) this.line(line, out);
    return out;
  }

  /** The stream closed: an event without its blank line still counts. */
  end(): SseEvent[] {
    const out: SseEvent[] = [];
    const last = this.rest.replace(/\r$/, '');
    this.rest = '';
    if (last) this.line(last, out);
    this.line('', out);
    return out;
  }

  private line(line: string, out: SseEvent[]): void {
    if (line === '') {
      if (this.data.length) out.push({ event: this.event || 'message', data: this.data.join('\n') });
      this.event = '';
      this.data = [];
      return;
    }
    if (line.startsWith(':')) return;
    const colon = line.indexOf(':');
    const field = colon < 0 ? line : line.slice(0, colon);
    let value = colon < 0 ? '' : line.slice(colon + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    if (field === 'event') this.event = value;
    else if (field === 'data') this.data.push(value);
  }
}

/**
 * The lines a view holds: at most `cap`, the oldest dropped first, so a long
 * follow never grows without bound. `total` counts every line seen.
 */
export class LineBuffer {
  lines: LogLine[] = [];
  total = 0;
  constructor(readonly cap = LOG_CAP) {}

  push(batch: Omit<LogLine, 'n'>[]): void {
    for (const l of batch) this.lines.push({ ...l, n: this.total++ });
    if (this.lines.length > this.cap) this.lines.splice(0, this.lines.length - this.cap);
  }

  /** The newest line's time, to skip a tail the hub sends again on Follow again. */
  get lastAt(): string | undefined {
    return this.lines.at(-1)?.at;
  }

  clear(): void {
    this.lines = [];
  }
}

/** A line event's data, or undefined when it isn't one. */
export function parseLine(data: string): Omit<LogLine, 'n'> | undefined {
  try {
    const v = JSON.parse(data) as Partial<LogLine>;
    if (typeof v.line !== 'string') return undefined;
    const out: Omit<LogLine, 'n'> = { stream: v.stream === 'stderr' ? 'stderr' : 'stdout', line: v.line };
    if (typeof v.at === 'string' && v.at) out.at = v.at;
    if (typeof v.service === 'string' && v.service) out.service = v.service;
    return out;
  } catch {
    return undefined;
  }
}

const END_WORDS: Record<EndReason, string> = {
  done: 'The log ended.',
  limit: 'Following stops after 30 minutes. Follow again to carry on.',
  closed: 'The hub closed the stream.',
  error: 'The logs stopped with an error.',
};
const REASONS: readonly EndReason[] = ['done', 'limit', 'closed', 'error'];

export function parseEnd(data: string): LogEnd {
  let v: { reason?: string; message?: string } = {};
  try {
    v = JSON.parse(data) as typeof v;
  } catch {
    /* an end without a body still ends */
  }
  const reason = (REASONS as readonly string[]).includes(v.reason ?? '') ? (v.reason as EndReason) : 'closed';
  return { reason, message: typeof v.message === 'string' && v.message ? v.message : END_WORDS[reason] };
}

/** A line as Download writes it: time, service, `[stderr]`, then the text. */
export function lineText(l: LogLine): string {
  return `${l.at ? `${l.at} ` : ''}${l.service ? `${l.service} | ` : ''}${l.stream === 'stderr' ? '[stderr] ' : ''}${l.line}`;
}

export function logsText(lines: readonly LogLine[]): string {
  return lines.slice(-LOG_CAP).map(lineText).join('\n') + (lines.length ? '\n' : '');
}

/** "sonarr-logs-2026-11-05.txt" */
export function logsFileName(name: string, now: number): string {
  const base = name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 60);
  return `${base || 'container'}-logs-${new Date(now).toISOString().slice(0, 10)}.txt`;
}

/** A line split around the search's matches, case-insensitive, plain text (no patterns). */
export interface Piece {
  text: string;
  hit: boolean;
}
export function pieces(line: string, q: string): Piece[] {
  if (!q) return [{ text: line, hit: false }];
  const low = line.toLowerCase();
  const needle = q.toLowerCase();
  const out: Piece[] = [];
  let at = 0;
  for (let i = low.indexOf(needle); i >= 0; i = low.indexOf(needle, i + needle.length)) {
    if (i > at) out.push({ text: line.slice(at, i), hit: false });
    out.push({ text: line.slice(i, i + needle.length), hit: true });
    at = i + needle.length;
  }
  if (at < line.length || !out.length) out.push({ text: line.slice(at), hit: false });
  return out;
}

export function countMatches(lines: readonly LogLine[], q: string): number {
  if (!q) return 0;
  const needle = q.toLowerCase();
  let n = 0;
  for (const l of lines) {
    const low = l.line.toLowerCase();
    for (let i = low.indexOf(needle); i >= 0; i = low.indexOf(needle, i + needle.length)) n++;
  }
  return n;
}

/** A container's stream: the instance is its source (`integration:<id>`), else the ID's `docker:<id>/…` prefix. */
export function containerLogsUrl(resource: { id: string; source?: string }, tail: number | 'all' = DEFAULT_TAIL): string | undefined {
  const inst = resource.source?.startsWith('integration:') ? resource.source.slice('integration:'.length) : /^[^:]+:([^/]+)\//.exec(resource.id)?.[1];
  if (!inst) return undefined;
  const q = new URLSearchParams({ resource: resource.id, tail: String(tail), follow: '1' });
  return `/api/integrations/instances/${encodeURIComponent(inst)}/logs?${q.toString()}`;
}

/** A stack's stream: every service, interleaved, each line tagged. */
export function stackLogsUrl(node: string, stack: string, tail: number | 'all' = DEFAULT_TAIL): string {
  return `/api/stacks/${encodeURIComponent(node)}/${encodeURIComponent(stack)}/logs?tail=${encodeURIComponent(String(tail))}&follow=1`;
}

export interface FollowHandlers {
  /** The hub accepted: the stream is open. */
  open?(): void;
  lines(batch: Omit<LogLine, 'n'>[]): void;
  end(e: LogEnd): void;
}

/**
 * Follows a stream until it ends or the signal aborts. Every way it can stop
 * comes back through `end` exactly once, except an abort, which is the
 * caller's own doing and says nothing.
 */
export async function follow(url: string, signal: AbortSignal, on: FollowHandlers): Promise<void> {
  let res: Response;
  try {
    res = await fetch(url, { credentials: 'same-origin', headers: { Accept: 'text/event-stream' }, signal });
  } catch {
    if (!signal.aborted) on.end({ reason: 'error', message: 'Can’t reach the Cha Dash hub.' });
    return;
  }
  if (!res.ok || !res.body) {
    let message = `The hub answered ${res.status}.`;
    try {
      const body = JSON.parse(await res.text()) as { error?: { message?: string } };
      if (body.error?.message) message = body.error.message;
    } catch {
      /* not the hub's envelope: keep the status */
    }
    if (!signal.aborted) on.end({ reason: 'error', message });
    return;
  }
  on.open?.();
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  const parser = new SseParser();
  let ended = false;
  const handle = (events: SseEvent[]): void => {
    const batch: Omit<LogLine, 'n'>[] = [];
    for (const ev of events) {
      if (ended) break;
      if (ev.event === 'line') {
        const l = parseLine(ev.data);
        if (l) batch.push(l);
      } else if (ev.event === 'end') {
        if (batch.length) on.lines(batch.splice(0));
        ended = true;
        on.end(parseEnd(ev.data));
      }
    }
    if (batch.length) on.lines(batch);
  };
  const stopReading = () => void reader.cancel().catch(() => undefined);
  signal.addEventListener('abort', stopReading, { once: true });
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (signal.aborted) return;
      if (done) break;
      const chunk = value as Uint8Array | string;
      handle(parser.push(typeof chunk === 'string' ? chunk : decoder.decode(chunk, { stream: true })));
      if (ended) {
        stopReading();
        return;
      }
    }
    handle(parser.end());
    if (!ended && !signal.aborted) on.end({ reason: 'closed', message: 'The connection to the hub closed.' });
  } catch {
    if (!ended && !signal.aborted) on.end({ reason: 'error', message: 'The connection to the hub dropped.' });
  } finally {
    signal.removeEventListener('abort', stopReading);
  }
}
