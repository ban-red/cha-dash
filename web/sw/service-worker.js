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

/*
 * Notifications (internal/notify). The hub sends an encrypted Web Push
 * message the browser decrypts; its JSON is {v, kind, severity, title, body,
 * path, tag, at}. Shown with the app's icon; a newer message with the same
 * tag (the incident) replaces the older one. A click focuses an open tab and
 * takes it to `path` (an incident opens the Inbox on it), or opens one.
 * Only same-origin paths are followed.
 */

/** A path inside the app, or the front page. */
const appPath = (p) => (typeof p === 'string' && p.startsWith('/') && !p.startsWith('//') ? p : '/');

self.addEventListener('push', (event) => {
  let msg = {};
  try {
    msg = event.data ? event.data.json() : {};
  } catch {
    msg = {};
  }
  const title = typeof msg.title === 'string' && msg.title ? msg.title : 'Cha Dash';
  const options = {
    body: typeof msg.body === 'string' ? msg.body : '',
    icon: '/icons/icon-192.png',
    badge: '/icons/icon-192.png',
    data: { path: appPath(msg.path) },
    timestamp: Date.parse(msg.at) || Date.now(),
  };
  if (typeof msg.tag === 'string' && msg.tag) {
    options.tag = msg.tag;
    options.renotify = msg.severity === 'down';
  }
  event.waitUntil(self.registration.showNotification(title, options));
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const path = appPath(event.notification.data && event.notification.data.path);
  const url = new URL(path, self.location.origin).href;
  event.waitUntil(
    (async () => {
      const tabs = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
      for (const tab of tabs) {
        if (new URL(tab.url).origin !== self.location.origin) continue;
        await tab.focus();
        if ('navigate' in tab) await tab.navigate(url);
        return;
      }
      await self.clients.openWindow(url);
    })(),
  );
});

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
