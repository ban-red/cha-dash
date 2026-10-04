/**
 * Same-origin JSON over fetch. The session is an HttpOnly cookie, so nothing
 * here touches credentials. Errors from the hub look like
 * `{"error":{"code","message"}}`; 429 carries `Retry-After` (seconds).
 */

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  /** Seconds, from Retry-After on 429. */
  readonly retryAfter: number | undefined;
  /** The decoded body, for 409s that carry the current state. */
  readonly body: unknown;

  constructor(status: number, code: string, message: string, retryAfter?: number, body?: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.retryAfter = retryAfter;
    this.body = body;
  }
}

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
  body?: unknown;
  signal?: AbortSignal;
}

export async function api<T = unknown>(path: string, opts: RequestOptions = {}): Promise<T> {
  const init: RequestInit = {
    method: opts.method ?? 'GET',
    credentials: 'same-origin',
    headers: { Accept: 'application/json' },
  };
  if (opts.signal) init.signal = opts.signal;
  if (opts.body !== undefined) {
    init.headers = { Accept: 'application/json', 'Content-Type': 'application/json' };
    init.body = JSON.stringify(opts.body);
  }
  let res: Response;
  try {
    res = await fetch(path, init);
  } catch (err) {
    if ((err as Error).name === 'AbortError') throw err;
    throw new ApiError(0, 'network', "Can't reach the Cha Dash hub.");
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: unknown;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = undefined;
    }
  }
  if (res.ok) return data as T;
  const err = (data as { error?: { code?: string; message?: string } } | undefined)?.error;
  const ra = res.headers.get('Retry-After');
  const retryAfter = ra !== null && /^\d+$/.test(ra) ? Number(ra) : undefined;
  throw new ApiError(res.status, err?.code ?? `http_${res.status}`, err?.message ?? res.statusText ?? 'request failed', retryAfter, data);
}
