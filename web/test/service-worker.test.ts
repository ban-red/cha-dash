/**
 * The service worker keeps the generic index.html as its shell, never a
 * signed-in owner's page with the first-paint snapshot in it: the precache
 * asks without cookies, and a no-store navigation response is never kept.
 */
import { describe, expect, it } from 'vitest';
import source from '../sw/service-worker.js?raw';

type Listener = (event: { waitUntil(p: Promise<unknown>): void; respondWith(p: Promise<unknown>): void; request?: unknown }) => void;

class FakeRequest {
  readonly url: string;
  readonly cache?: string;
  readonly credentials?: string;
  readonly method = 'GET';
  readonly mode: string;
  readonly headers = new Headers();
  constructor(url: string, init: { cache?: string; credentials?: string; mode?: string } = {}) {
    this.url = new URL(url, 'https://hub.test').href;
    if (init.cache !== undefined) this.cache = init.cache;
    if (init.credentials !== undefined) this.credentials = init.credentials;
    this.mode = init.mode ?? 'cors';
  }
}

function worker(network: (req: FakeRequest) => Response) {
  const listeners: Record<string, Listener> = {};
  const added: FakeRequest[] = [];
  const put: string[] = [];
  const cache = {
    addAll: async (reqs: FakeRequest[]) => void added.push(...reqs),
    put: async (key: string | FakeRequest) => void put.push(typeof key === 'string' ? key : key.url),
    keys: async () => [],
    delete: async () => true,
  };
  const self = {
    __CHADASH_PRECACHE__: ['/index.html', '/boot.js', '/assets/index-abc.js'],
    location: { origin: 'https://hub.test' },
    addEventListener: (type: string, fn: Listener) => (listeners[type] = fn),
    skipWaiting: async () => undefined,
    clients: { claim: async () => undefined },
  };
  const caches = { open: async () => cache, keys: async () => [], match: async () => undefined };
  const fetch = async (req: FakeRequest) => network(req);
  // The worker is a classic script; run it against the fakes above.
  new Function('self', 'caches', 'fetch', 'Request', source)(self, caches, fetch, FakeRequest);
  return { listeners, added, put };
}

async function fire(l: Listener | undefined, request?: FakeRequest): Promise<unknown> {
  let wait: Promise<unknown> = Promise.resolve();
  let answer: Promise<unknown> | undefined;
  l?.({ waitUntil: (p) => (wait = p), respondWith: (p) => (answer = p), ...(request ? { request } : {}) });
  await wait;
  return answer;
}

const html = (cacheControl: string) => new Response('<!doctype html>', { status: 200, headers: { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': cacheControl } });

describe('the service worker’s shell', () => {
  it('precaches without cookies, so the shell is the generic page', async () => {
    const w = worker(() => html('no-cache'));
    await fire(w.listeners['install']);
    expect(w.added.map((r) => new URL(r.url).pathname)).toEqual(['/index.html', '/boot.js', '/assets/index-abc.js']);
    for (const r of w.added) expect(r.credentials).toBe('omit');
  });

  it('never keeps a no-store page, and refreshes the shell from a generic one', async () => {
    const personal = worker(() => html('no-store'));
    const res = (await fire(personal.listeners['fetch'], new FakeRequest('/boards/b1', { mode: 'navigate' }))) as Response;
    expect(res.status).toBe(200);
    expect(personal.put).toEqual([]);

    // A same-origin response from the network is type "basic"; a constructed one is "default".
    const generic = worker(() => {
      const res = html('no-cache');
      Object.defineProperty(res, 'type', { value: 'basic' });
      return res;
    });
    await fire(generic.listeners['fetch'], new FakeRequest('/', { mode: 'navigate' }));
    expect(generic.put).toEqual(['/index.html']);
  });
});
