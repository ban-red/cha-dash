/*
 * The Cha Dash service worker (ADR-0011). `vite build` emits it as /sw.js with
 * the build's file list filled in (see the chadash-sw plugin in vite.config.ts).
 *
 * - The app shell (index.html, boot.js, the manifest and icons, every JS and
 *   CSS file of this build) is cached on install, so the page opens with no
 *   network at all.
 * - Navigations go to the network first, falling back to the cached shell.
 * - Hashed files under /assets/ are immutable: cache first, filled on use
 *   (fonts arrive this way, only the faces a theme actually loads).
 * - /api and /ws are never touched: requests pass straight through and
 *   nothing that carries the session is ever stored. The last-known data the
 *   page shows offline lives in IndexedDB, written by the page itself.
 * - The shell is the generic index.html. A signed-in owner's navigation gets
 *   index.html with their first-paint snapshot inlined, marked no-store, so
 *   it's never kept; the precache asks without cookies, so it can't get one.
 *
 * Plain JavaScript, no imports, no eval: the CSP's default-src 'self' covers
 * workers too.
 */
/* eslint-disable no-restricted-globals */

const VERSION = '__CHADASH_VERSION__';
/** @type {string[]} */
const PRECACHE = self.__CHADASH_PRECACHE__;
const SHELL = `chadash-shell-${VERSION}`;
const ASSETS = 'chadash-assets';

/** Paths the worker must never answer for. */
const bypass = (path) => path === '/api' || path.startsWith('/api/') || path === '/ws' || path.startsWith('/ws/') || path === '/healthz';

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches
      .open(SHELL)
      .then((cache) => cache.addAll(PRECACHE.map((p) => new Request(p, { cache: 'reload', credentials: 'omit' }))))
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    (async () => {
      for (const key of await caches.keys()) {
        if (key.startsWith('chadash-shell-') && key !== SHELL) await caches.delete(key);
      }
      // Drop scripts and styles from older builds; keep fonts and images, which are content-hashed and shared.
      const assets = await caches.open(ASSETS);
      const keep = new Set(PRECACHE);
      for (const req of await assets.keys()) {
        const path = new URL(req.url).pathname;
        if (/\.(js|css)$/.test(path) && !keep.has(path)) await assets.delete(req);
      }
      await self.clients.claim();
    })(),
  );
});

/** A response worth keeping: a plain, complete, same-origin 200. */
const keepable = (res) => res && res.status === 200 && res.type === 'basic' && !/no-store/i.test(res.headers.get('Cache-Control') || '');

async function navigate(request) {
  try {
    const res = await fetch(request);
    if (keepable(res) && (res.headers.get('Content-Type') || '').includes('text/html')) {
      const cache = await caches.open(SHELL);
      await cache.put('/index.html', res.clone());
    }
    return res;
  } catch (err) {
    const cached = await caches.match('/index.html');
    if (cached) return cached;
    throw err;
  }
}

async function cacheFirst(request) {
  const cached = await caches.match(request);
  if (cached) return cached;
  const res = await fetch(request);
  if (keepable(res)) {
    const cache = await caches.open(ASSETS);
    await cache.put(request, res.clone());
  }
  return res;
}

async function staleWhileRevalidate(request, event) {
  const cached = await caches.match(request);
  const fresh = fetch(request).then(async (res) => {
    if (keepable(res)) {
      const cache = await caches.open(SHELL);
      await cache.put(request, res.clone());
    }
    return res;
  });
  if (cached) {
    event.waitUntil(fresh.catch(() => undefined));
    return cached;
  }
  return fresh;
}

self.addEventListener('fetch', (event) => {
  const request = event.request;
  if (request.method !== 'GET') return;
  const url = new URL(request.url);
  if (url.origin !== self.location.origin || bypass(url.pathname)) return;
  if (request.headers.has('Authorization')) return;
  if (request.mode === 'navigate') {
    event.respondWith(navigate(request));
  } else if (url.pathname.startsWith('/assets/')) {
    event.respondWith(cacheFirst(request));
  } else if (PRECACHE.includes(url.pathname)) {
    event.respondWith(staleWhileRevalidate(request, event));
  }
});
