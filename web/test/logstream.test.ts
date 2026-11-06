/**
 * Live logs, layer 1 (plan 16 · 4): the event-stream parser, the line cap,
 * search, Download's text, the routes, and follow() against a fake hub,
 * including 30 minutes of test-clock time that ends with the hub's limit.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  containerLogsUrl,
  countMatches,
  follow,
  LineBuffer,
  LOG_CAP,
  logsFileName,
  logsText,
  parseEnd,
  pieces,
  SseParser,
  stackLogsUrl,
  type LogEnd,
  type LogLine,
} from '../src/lib/logstream';
import { fakeLogStreams } from './fake-logstream';

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const ln = (line: string, extra: Partial<LogLine> = {}): Omit<LogLine, 'n'> => ({ stream: 'stdout', line, ...extra });

describe('SseParser', () => {
  it('reads events split anywhere across chunks, CRLF included', () => {
    const p = new SseParser();
    const text = 'event: line\r\ndata: {"line":"a"}\r\n\r\nevent: end\ndata: {"reason":"done"}\n\n';
    const out = [];
    for (const ch of text) out.push(...p.push(ch));
    expect(out).toEqual([
      { event: 'line', data: '{"line":"a"}' },
      { event: 'end', data: '{"reason":"done"}' },
    ]);
  });

  it('ignores comments, joins multi-line data, and keeps one leading space only', () => {
    const p = new SseParser();
    expect(p.push(': ping\n\n')).toEqual([]);
    expect(p.push('event: line\ndata: one\ndata:  two\n: in between\ndata\n\n')).toEqual([{ event: 'line', data: 'one\n two\n' }]);
    // An event with no event field is a message; no data, no event.
    expect(p.push('data: x\n\nevent: line\n\n')).toEqual([{ event: 'message', data: 'x' }]);
  });

  it('counts an event the stream closed before its blank line', () => {
    const p = new SseParser();
    expect(p.push('event: end\ndata: {"reason":"closed"}')).toEqual([]);
    expect(p.end()).toEqual([{ event: 'end', data: '{"reason":"closed"}' }]);
  });
});

describe('LineBuffer', () => {
  it('keeps the newest 10,000 lines of 25,000, oldest dropped first', () => {
    const b = new LineBuffer();
    for (let i = 0; i < 25; i++) b.push(Array.from({ length: 1000 }, (_, j) => ln(`line ${i * 1000 + j}`)));
    expect(b.lines).toHaveLength(LOG_CAP);
    expect(b.total).toBe(25_000);
    expect(b.lines[0]!.line).toBe('line 15000');
    expect(b.lines.at(-1)!.line).toBe('line 24999');
    // Keys stay unique and in order.
    expect(b.lines.at(-1)!.n - b.lines[0]!.n).toBe(LOG_CAP - 1);
  });
});

describe('search and download', () => {
  it('splits a line around plain-text matches, case-insensitive, with no patterns', () => {
    expect(pieces('GET /a.b ok, get /a.b', 'get')).toEqual([
      { text: 'GET', hit: true },
      { text: ' /a.b ok, ', hit: false },
      { text: 'get', hit: true },
      { text: ' /a.b', hit: false },
    ]);
    // Regex characters are only characters.
    expect(pieces('a.b axb', '.').filter((p) => p.hit)).toHaveLength(1);
    expect(pieces('(x)[y]', '(x)[')).toEqual([
      { text: '(x)[', hit: true },
      { text: 'y]', hit: false },
    ]);
    expect(pieces('abc', '')).toEqual([{ text: 'abc', hit: false }]);
    const lines = [ln('error one error'), ln('fine'), ln('ERROR')].map((l, n) => ({ ...l, n }));
    expect(countMatches(lines, 'error')).toBe(3);
    expect(countMatches(lines, '.*')).toBe(0);
  });

  it('writes time, service and stderr into the file, at most 10,000 lines', () => {
    const lines = [ln('ready', { at: '2026-11-05T12:00:00.123Z', service: 'web' }), ln('boom', { stream: 'stderr' })].map((l, n) => ({ ...l, n }));
    expect(logsText(lines)).toBe('2026-11-05T12:00:00.123Z web | ready\n[stderr] boom\n');
    expect(logsText([])).toBe('');
    const many = Array.from({ length: 12_000 }, (_, n) => ({ ...ln(`l${n}`), n }));
    const text = logsText(many);
    expect(text.split('\n').filter(Boolean)).toHaveLength(LOG_CAP);
    expect(text.startsWith('l2000\n')).toBe(true);
    expect(logsFileName('Sonarr (media)', Date.UTC(2026, 10, 5))).toBe('sonarr-media-logs-2026-11-05.txt');
  });
});

describe('the routes', () => {
  it('asks the container’s instance for it, and the stack by node and name', () => {
    expect(containerLogsUrl({ id: 'docker:i_abc/container/sonarr', source: 'integration:i_abc' })).toBe(
      '/api/integrations/instances/i_abc/logs?resource=docker%3Ai_abc%2Fcontainer%2Fsonarr&tail=200&follow=1',
    );
    expect(containerLogsUrl({ id: 'docker:i_abc/container/sonarr' }, 'all')).toBe('/api/integrations/instances/i_abc/logs?resource=docker%3Ai_abc%2Fcontainer%2Fsonarr&tail=all&follow=1');
    expect(containerLogsUrl({ id: 'nothing' })).toBeUndefined();
    expect(stackLogsUrl('lo cal', 'media/x')).toBe('/api/stacks/lo%20cal/media%2Fx/logs?tail=200&follow=1');
  });

  it('reads the end event’s reason, with a sentence when the hub sent none', () => {
    expect(parseEnd('{"reason":"error","message":"The node went away."}')).toEqual({ reason: 'error', message: 'The node went away.' });
    expect(parseEnd('{"reason":"limit"}').message).toBe('Following stops after 30 minutes. Follow again to carry on.');
    expect(parseEnd('nonsense').reason).toBe('closed');
  });
});

function collect() {
  const buf = new LineBuffer();
  const ends: LogEnd[] = [];
  let opened = 0;
  return { buf, ends, opened: () => opened, on: { open: () => opened++, lines: (b: Omit<LogLine, 'n'>[]) => buf.push(b), end: (e: LogEnd) => ends.push(e) } };
}

describe('follow()', () => {
  it('turns line events into lines and ends once with the hub’s reason', async () => {
    const net = fakeLogStreams();
    const c = collect();
    const done = follow('/api/stacks/local/media/logs?tail=200&follow=1', new AbortController().signal, c.on);
    await vi.waitFor(() => expect(net.streams).toHaveLength(1));
    const s = net.last();
    s.line({ line: 'web ready', service: 'web' });
    s.send('event: line\ndata: {"stream":"stderr","line":"db');
    s.send(' warn","service":"db"}\n\n: ping\n\n');
    s.end('done', 'The stack stopped.');
    await done;
    expect(c.opened()).toBe(1);
    expect(c.buf.lines.map((l) => [l.service, l.stream, l.line])).toEqual([
      ['web', 'stdout', 'web ready'],
      ['db', 'stderr', 'db warn'],
    ]);
    expect(c.ends).toEqual([{ reason: 'done', message: 'The stack stopped.' }]);
  });

  it('shows the hub’s error before the stream starts', async () => {
    fakeLogStreams({ reply: () => ({ status: 409, body: { error: { code: 'not_running', message: 'sonarr isn’t running.' } } }) });
    const c = collect();
    await follow('/api/integrations/instances/i_d/logs?resource=x', new AbortController().signal, c.on);
    expect(c.opened()).toBe(0);
    expect(c.ends).toEqual([{ reason: 'error', message: 'sonarr isn’t running.' }]);
  });

  it('says so when the hub closes without an end, and nothing when the page aborts', async () => {
    const net = fakeLogStreams();
    const c = collect();
    const done = follow('/x/logs', new AbortController().signal, c.on);
    await vi.waitFor(() => expect(net.streams).toHaveLength(1));
    net.last().close();
    await done;
    expect(c.ends.map((e) => e.reason)).toEqual(['closed']);

    const c2 = collect();
    const ctrl = new AbortController();
    const done2 = follow('/x/logs', ctrl.signal, c2.on);
    await vi.waitFor(() => expect(net.streams).toHaveLength(2));
    ctrl.abort();
    await done2;
    expect(net.last().aborted).toBe(true);
    expect(c2.ends).toEqual([]);
  });

  it('following for 30 minutes of test-clock time stays under the line cap, and the stream ends', async () => {
    vi.useFakeTimers();
    const net = fakeLogStreams();
    const c = collect();
    const done = follow('/x/logs', new AbortController().signal, c.on);
    await vi.waitFor(() => expect(net.streams).toHaveLength(1));
    const s = net.last();
    // The fake hub: a line every 100 ms, a ping every 15 s, and the 30-minute cap.
    const start = Date.now();
    const lines = setInterval(() => s.line({ line: `tick ${Date.now() - start}`, at: new Date().toISOString() }), 100);
    const pings = setInterval(() => s.ping(), 15_000);
    setTimeout(() => {
      clearInterval(lines);
      clearInterval(pings);
      s.end('limit', 'Following stops after 30 minutes. Follow again to carry on.');
    }, 30 * 60_000);
    let most = 0;
    for (let minute = 0; minute < 30; minute++) {
      await vi.advanceTimersByTimeAsync(60_000);
      most = Math.max(most, c.buf.lines.length);
    }
    await done;
    expect(c.buf.total).toBe(17_999);
    expect(most).toBe(LOG_CAP);
    expect(c.buf.lines).toHaveLength(LOG_CAP);
    expect(c.buf.lines.at(-1)!.line).toBe('tick 1799900');
    expect(c.ends).toEqual([{ reason: 'limit', message: 'Following stops after 30 minutes. Follow again to carry on.' }]);
  });
});
