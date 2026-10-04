/**
 * A scriptable fetch for the hub's HTTP API: routes by method and path,
 * records every call with its decoded body.
 */
import { vi } from 'vitest';

export type Handler = (body: unknown, url: URL) => { status: number; body?: unknown } | Promise<{ status: number; body?: unknown }>;

export interface Call {
  method: string;
  path: string;
  body: unknown;
}

export function fakeFetch(routes: Record<string, Handler | { status: number; body?: unknown }>) {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: string, init?: RequestInit) => {
    const url = new URL(input, 'http://hub.test');
    const method = (init?.method ?? 'GET').toUpperCase();
    const body = typeof init?.body === 'string' ? (JSON.parse(init.body) as unknown) : undefined;
    calls.push({ method, path: url.pathname + url.search, body });
    // Exact path (with or without the query), else a key ending in * as a prefix: 'POST /api/plans/*'.
    const wild = Object.keys(routes).find((k) => k.endsWith('*') && `${method} ${url.pathname}`.startsWith(k.slice(0, -1)));
    const route = routes[`${method} ${url.pathname}${url.search}`] ?? routes[`${method} ${url.pathname}`] ?? (wild ? routes[wild] : undefined);
    if (!route) return new Response(JSON.stringify({ error: { code: 'not_found', message: 'not found' } }), { status: 404 });
    const res = typeof route === 'function' ? await route(body, url) : route;
    if (res.status === 204) return new Response(null, { status: 204 });
    return new Response(res.body === undefined ? '' : JSON.stringify(res.body), { status: res.status, headers: { 'Content-Type': 'application/json' } });
  });
  vi.stubGlobal('fetch', fn);
  return { fn, calls, last: (method: string, path: string) => [...calls].reverse().find((c) => c.method === method && c.path === path) };
}
