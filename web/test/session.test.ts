import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../src/lib/api';
import { authMessage, createSession } from '../src/stores/session';

afterEach(() => vi.unstubAllGlobals());

const json = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } });

const user = { id: 'u1', username: 'owner', role: 'owner' };

describe('session store', () => {
  it('opens setup on first run', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(200, { required: true })));
    const s = createSession();
    await s.refresh();
    expect(s.state.phase).toBe('setup');
  });

  it('opens sign-in when there is no session, and the app when there is', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(json(200, { required: false }))
      .mockResolvedValueOnce(json(401, { error: { code: 'unauthenticated', message: 'sign in required' } }))
      .mockResolvedValueOnce(json(200, { required: false }))
      .mockResolvedValueOnce(json(200, { user, session: { id: 's1', method: 'password' } }));
    vi.stubGlobal('fetch', fetchMock);
    const s = createSession();
    await s.refresh();
    expect(s.state.phase).toBe('login');
    await s.refresh();
    expect(s.state.phase).toBe('ready');
    expect(s.state.user).toEqual(user);
  });

  it('shows an unreachable state rather than a sign-in form when the hub is down', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')));
    const s = createSession();
    await s.refresh();
    expect(s.state.phase).toBe('unreachable');
  });

  it('completes setup with the token, username and password', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(201, { user }));
    vi.stubGlobal('fetch', fetchMock);
    const s = createSession();
    await s.setup('  cds_abc  ', 'owner', 'correct horse battery');
    const [url, init] = fetchMock.mock.calls[0]!;
    expect(url).toBe('/api/setup');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body)).toEqual({ token: 'cds_abc', username: 'owner', password: 'correct horse battery' });
    expect(s.state.phase).toBe('ready');
  });

  it('moves to sign-in when setup was already done', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(409, { error: { code: 'setup_complete', message: 'setup has already been completed' } })));
    const s = createSession();
    await expect(s.setup('t', 'owner', 'correct horse battery')).rejects.toBeInstanceOf(ApiError);
    expect(s.state.phase).toBe('login');
  });

  it('signs in and out', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(json(200, { user })).mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal('fetch', fetchMock);
    const s = createSession();
    await s.login('owner', 'correct horse battery');
    expect(s.state.phase).toBe('ready');
    await s.logout();
    expect(fetchMock.mock.calls[1]![0]).toBe('/api/auth/logout');
    expect(s.state.phase).toBe('login');
    expect(s.state.user).toBeUndefined();
  });

  it('turns hub errors into sentences', () => {
    expect(authMessage(new ApiError(401, 'bad_credentials', 'invalid username or password'), 'login')).toBe('That username and password don’t match.');
    expect(authMessage(new ApiError(403, 'bad_setup_token', 'invalid setup token'), 'setup')).toMatch(/setup token doesn't match/);
    expect(authMessage(new ApiError(422, 'invalid', 'password must be at least 12 characters'), 'setup')).toBe('Password must be at least 12 characters.');
    expect(authMessage(new ApiError(429, 'rate_limited', 'too many attempts', 42), 'login')).toBe('Too many attempts. Try again in 42 s.');
    expect(authMessage(new ApiError(0, 'network', 'x'), 'login')).toMatch(/Can't reach/);
  });

  it('reads Retry-After from a 429', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(429, { error: { code: 'rate_limited', message: 'too many attempts; try again later' } }, { 'Retry-After': '30' })));
    const s = createSession();
    const err = await s.login('owner', 'x'.repeat(12)).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).retryAfter).toBe(30);
  });
});
