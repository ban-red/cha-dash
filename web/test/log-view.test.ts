/**
 * The live log view (plan 16 · 4, components/LogView.vue) against a fake hub
 * stream: following, Pause with the lines waiting, search, stderr and the
 * service told apart without colour, Download, the end with Follow again,
 * the line cap over 30 minutes of test-clock time, unmount aborting, and axe.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import LogView from '../src/components/LogView.vue';
import { LOG_CAP } from '../src/lib/logstream';
import { axeViolations } from './axe';
import { fakeLogStreams } from './fake-logstream';
import { harness } from './harness';

const URL_ = '/api/integrations/instances/i_d/logs?resource=docker%3Ai_d%2Fcontainer%2Fsonarr&tail=200&follow=1';

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response('[]', { status: 200, headers: { 'content-type': 'application/json' } })));
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

async function view(opts: Parameters<typeof fakeLogStreams>[0] = {}) {
  const net = fakeLogStreams(opts);
  const h = harness();
  const w = h.mountIn(LogView, { url: URL_, name: 'sonarr' });
  await flushPromises();
  const root = () => document.querySelector<HTMLElement>('.lv')!;
  const lines = () => [...root().querySelectorAll<HTMLElement>('.lv-l')];
  const button = (name: string) => [...root().querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent!.trim() === name);
  const status = () => root().querySelector('[role="status"]')!.textContent!.trim();
  return { ...h, w, net, root, lines, button, status };
}

describe('LogView', () => {
  it('follows the stream: stderr and the service told apart in words, and axe clean', async () => {
    const v = await view();
    expect(v.net.last().url.pathname + v.net.last().url.search).toBe(URL_);
    v.net.last().line({ line: 'listening on :8989', service: 'web' });
    v.net.last().line({ line: 'database is locked', stream: 'stderr', service: 'db' });
    await flushPromises();
    expect(v.status()).toBe('Following live.');
    const [out, err] = v.lines();
    expect(out!.textContent).toBe('weblistening on :8989\n');
    expect(out!.classList.contains('err')).toBe(false);
    expect(err!.classList.contains('err')).toBe(true);
    // A visible tag and words for screen readers, not colour alone.
    expect(err!.querySelector('.lv-tag')!.textContent).toBe('err');
    expect(err!.querySelector('.sr')!.textContent).toBe('stderr: ');
    expect(err!.querySelector('.lv-svc')!.textContent).toBe('db');
    expect(await axeViolations(v.root(), ['region'])).toEqual([]);
  });

  it('Pause keeps receiving, holds the view and counts what waits; Resume shows it all', async () => {
    const v = await view();
    v.net.last().line({ line: 'one' });
    await flushPromises();
    v.button('Pause')!.click();
    await flushPromises();
    expect(v.button('Resume')!.getAttribute('aria-pressed')).toBe('true');
    expect(v.status()).toBe('Paused.');
    v.net.last().line({ line: 'two' });
    v.net.last().line({ line: 'three' });
    await flushPromises();
    expect(v.lines().map((l) => l.textContent!.trim())).toEqual(['one']);
    expect(v.root().querySelector('.lv-wait')!.textContent).toBe('2 new lines waiting.');
    v.button('Resume')!.click();
    await flushPromises();
    expect(v.lines().map((l) => l.textContent!.trim())).toEqual(['one', 'two', 'three']);
    expect(v.root().querySelector('.lv-wait')).toBeNull();
  });

  it('search highlights plain text and counts the matches', async () => {
    const v = await view();
    v.net.last().line({ line: 'GET /api ok' });
    v.net.last().line({ line: 'get /a.i failed, get again' });
    v.net.last().line({ line: 'nothing here' });
    await flushPromises();
    const input = v.root().querySelector<HTMLInputElement>('input[type="search"]')!;
    expect(document.querySelector(`label[for="${input.id}"]`)!.textContent).toBe('Search the log');
    input.value = 'get';
    input.dispatchEvent(new Event('input'));
    await flushPromises();
    expect([...v.root().querySelectorAll('mark')].map((m) => m.textContent)).toEqual(['GET', 'get', 'get']);
    expect(v.root().querySelector('.lv-n')!.textContent).toBe('3 matches');
    input.value = '.*';
    input.dispatchEvent(new Event('input'));
    await flushPromises();
    expect(v.root().querySelectorAll('mark')).toHaveLength(0);
    expect(v.root().querySelector('.lv-n')!.textContent).toBe('No matches');
    expect(await axeViolations(v.root(), ['region'])).toEqual([]);
  });

  it('Download saves the lines as a .txt', async () => {
    const v = await view();
    v.net.last().line({ line: 'ready', at: '2026-11-05T12:00:00.123Z' });
    v.net.last().line({ line: 'boom', stream: 'stderr' });
    await flushPromises();
    let saved: Blob | undefined;
    let name = '';
    const real = { create: URL.createObjectURL, revoke: URL.revokeObjectURL };
    URL.createObjectURL = (b: Blob) => ((saved = b), 'blob:x');
    URL.revokeObjectURL = () => {};
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      name = this.download;
    });
    v.button('Download')!.click();
    Object.assign(URL, { createObjectURL: real.create, revokeObjectURL: real.revoke });
    click.mockRestore();
    expect(name).toMatch(/^sonarr-logs-\d{4}-\d{2}-\d{2}\.txt$/);
    expect(saved!.type).toContain('text/plain');
    expect(await saved!.text()).toBe('2026-11-05T12:00:00.123Z ready\n[stderr] boom\n');
  });

  it('says why it ended, and Follow again carries on without repeating the tail', async () => {
    const v = await view();
    v.net.last().line({ line: 'a', at: '2026-11-05T12:00:00.100Z' });
    v.net.last().line({ line: 'b', at: '2026-11-05T12:00:00.200Z' });
    v.net.last().end('limit', 'Following stops after 30 minutes. Follow again to carry on.');
    await flushPromises();
    expect(v.status()).toBe('Following stops after 30 minutes. Follow again to carry on.');
    v.button('Follow again')!.click();
    await flushPromises();
    expect(v.net.streams).toHaveLength(2);
    expect(v.status()).toBe('Following live.');
    // The hub sends its tail again: what's already here is skipped.
    v.net.last().line({ line: 'a', at: '2026-11-05T12:00:00.100Z' });
    v.net.last().line({ line: 'b', at: '2026-11-05T12:00:00.200Z' });
    v.net.last().line({ line: 'c', at: '2026-11-05T12:00:00.300Z' });
    await flushPromises();
    expect(v.lines().map((l) => l.textContent!.trim())).toEqual(['a', 'b', 'c']);
  });

  it('shows the hub’s error when the stream can’t start', async () => {
    const v = await view({ reply: () => ({ status: 409, body: { error: { code: 'not_running', message: 'sonarr isn’t running.' } } }) });
    await flushPromises();
    expect(v.status()).toBe('sonarr isn’t running.');
    expect(v.button('Follow again')).toBeDefined();
  });

  it('unmounting aborts the stream', async () => {
    const v = await view();
    expect(v.net.last().aborted).toBe(false);
    v.w.unmount();
    expect(v.net.last().aborted).toBe(true);
  });

  it('following a container for 30 minutes of test-clock time keeps the tab under its line cap, and the stream ends', async () => {
    vi.useFakeTimers();
    const v = await view();
    const s = v.net.last();
    // The fake hub: 70 lines every 10 s (12,600 in all), a ping every 15 s, and the 30-minute cap.
    let n = 0;
    const lines = setInterval(() => {
      s.send(Array.from({ length: 70 }, () => `event: line\ndata: ${JSON.stringify({ stream: 'stdout', line: `line ${n++}` })}\n\n`).join(''));
    }, 10_000);
    const pings = setInterval(() => s.ping(), 15_000);
    setTimeout(() => {
      clearInterval(lines);
      clearInterval(pings);
      s.end('limit', 'Following stops after 30 minutes. Follow again to carry on.');
    }, 30 * 60_000 + 1);
    let most = 0;
    for (let minute = 0; minute <= 30; minute++) {
      await vi.advanceTimersByTimeAsync(60_000);
      most = Math.max(most, v.lines().length);
    }
    await flushPromises();
    expect(n).toBe(12_600);
    expect(most).toBe(LOG_CAP);
    expect(v.lines()).toHaveLength(LOG_CAP);
    expect(v.lines().at(-1)!.textContent!.trim()).toBe('line 12599');
    expect(v.root().querySelector('.lv-foot')!.textContent).toContain('The newest 10,000 lines.');
    expect(v.status()).toBe('Following stops after 30 minutes. Follow again to carry on.');
    expect(v.button('Follow again')).toBeDefined();
  });
});
