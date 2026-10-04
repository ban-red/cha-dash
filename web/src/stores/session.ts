/**
 * Who is signed in, and which door the app shows: first-run setup, sign-in,
 * or the front page. Plain Vue reactivity; no store library.
 *
 * Offline: when the hub can't be reached at start-up but this device kept a
 * last-known snapshot (lib/snapshot.ts, written while signed in), the front
 * page opens on it under the offline banner. The session is confirmed with
 * the hub as soon as the realtime socket is back; signing out deletes the
 * snapshot.
 *
 * First paint: when the hub inlined a snapshot into index.html
 * (lib/inline.ts), it checked the session to do so, so the app starts
 * signed in with no request at all, and the front page renders from it.
 */
import { inject, provide, reactive, type InjectionKey } from 'vue';
import { ApiError, api } from '../lib/api';
import type { InlineSnapshot } from '../lib/inline';
import type { Snapshot } from '../lib/snapshot';

export interface User {
  id: string;
  username: string;
  role: string;
}

export type SessionPhase = 'loading' | 'setup' | 'login' | 'ready' | 'unreachable';

export interface SessionState {
  phase: SessionPhase;
  user: User | undefined;
  /** Why the hub couldn't be asked, in `unreachable`. */
  error: string;
  /**
   * The hub offers one-click dev login (a `-tags devlogin` build, as `bun run dev`
   * makes). Only ever true in the Vite dev server; the button is src/dev/.
   */
  devLogin: boolean;
  /** Just created the owner: offer to add a passkey before the front page (skippable). */
  offerPasskey: boolean;
  /** Showing a last-known snapshot because the hub can't be reached; the session isn't confirmed yet. */
  offline: boolean;
}

/** Mirrors internal/auth: 1–64 of letters, digits, . _ @ -, starting with a letter or digit. */
export const USERNAME_RE = /^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$/;
export const MIN_PASSWORD = 12;

/** Password length the way the hub counts it: characters, not bytes. */
export const passwordLength = (p: string) => [...p].length;

/** A sentence for the person at the form. Never raw server text for auth failures. */
export function authMessage(err: unknown, context: 'setup' | 'login' | 'dev'): string {
  if (!(err instanceof ApiError)) return 'Something went wrong. Try again.';
  if (err.status === 0) return "Can't reach the Cha Dash hub. Check that it's running, then try again.";
  if (err.status === 429) {
    return err.retryAfter ? `Too many attempts. Try again in ${err.retryAfter} s.` : 'Too many attempts. Try again in a minute.';
  }
  if (context === 'setup') {
    if (err.status === 403) return "That setup token doesn't match. Copy it again from the hub's log.";
    if (err.status === 409) return 'Setup is already done. Sign in instead.';
    if (err.status === 422) return sentence(err.message);
  } else if (context === 'login' && err.status === 401) {
    return 'That username and password don’t match.';
  } else if (context === 'dev' && err.status === 404) {
    return 'This hub wasn’t built with dev login. Start it with bun run dev.';
  }
  return 'The hub couldn’t do that. Try again.';
}

const sentence = (s: string) => (s ? s[0]!.toUpperCase() + s.slice(1) + (/[.!?]$/.test(s) ? '' : '.') : s);

export interface SessionOptions {
  /** The snapshot the hub inlined into index.html (takeInlineSnapshot()), if any. */
  inline?: InlineSnapshot | undefined;
}

export function createSession(opts: SessionOptions = {}) {
  const state = reactive<SessionState>({ phase: 'loading', user: undefined, error: '', devLogin: false, offerPasskey: false, offline: false });
  /** The snapshot the offline front page opens on; taken once by the shell. Not reactive. */
  let restored: Snapshot | undefined;
  /** The hub's first-paint snapshot; taken once by the shell. Not reactive. */
  let inline = opts.inline;
  /** The hub vouched for this session when it served the page: the first refresh() has nothing to ask. */
  let vouched = false;
  if (inline) {
    state.user = inline.user;
    state.phase = 'ready';
    vouched = true;
  }

  /** Open the front page on the last-known snapshot, if this device has one. */
  async function tryOffline(): Promise<boolean> {
    try {
      const { loadSnapshot } = await import('../lib/snapshot');
      const snap = await loadSnapshot();
      if (!snap) return false;
      restored = snap;
      state.user = snap.user;
      state.offline = true;
      state.phase = 'ready';
      return true;
    } catch {
      return false;
    }
  }

  async function refresh(): Promise<void> {
    if (vouched) {
      vouched = false;
      return;
    }
    try {
      const setup = await api<{ required: boolean; dev_login?: boolean }>('/api/setup');
      state.devLogin = import.meta.env.DEV && setup?.dev_login === true;
      if (setup?.required) {
        state.user = undefined;
        state.phase = 'setup';
        return;
      }
      const s = await api<{ user: User }>('/api/auth/session');
      state.user = s.user;
      state.phase = 'ready';
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        state.user = undefined;
        state.phase = 'login';
        return;
      }
      const down = err instanceof ApiError && (err.status === 0 || err.status === 502 || err.status === 503 || err.status === 504);
      if (down && (await tryOffline())) return;
      state.error = err instanceof ApiError && err.status === 0 ? "Can't reach the Cha Dash hub." : 'The hub answered with an error.';
      state.phase = 'unreachable';
    }
  }

  async function setup(token: string, username: string, password: string): Promise<void> {
    try {
      const res = await api<{ user: User }>('/api/setup', { method: 'POST', body: { token: token.trim(), username, password } });
      state.user = res.user;
      state.offerPasskey = true;
      state.phase = 'ready';
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) state.phase = 'login';
      throw err;
    }
  }

  async function login(username: string, password: string): Promise<void> {
    const res = await api<{ user: User }>('/api/auth/login', { method: 'POST', body: { username, password } });
    state.user = res.user;
    state.phase = 'ready';
  }

  /** After a passkey sign-in: the hub set the cookie; it may or may not have sent the user. */
  async function signedIn(user?: User): Promise<void> {
    if (user) {
      state.user = user;
      state.phase = 'ready';
      return;
    }
    await refresh();
  }

  async function logout(): Promise<void> {
    try {
      await api('/api/auth/logout', { method: 'POST' });
    } finally {
      state.user = undefined;
      state.offline = false;
      state.offerPasskey = false;
      restored = undefined;
      inline = undefined;
      state.phase = 'login';
      try {
        const { clearSnapshot } = await import('../lib/snapshot');
        await clearSnapshot();
      } catch {
        /* nothing stored, or storage blocked */
      }
    }
  }

  /** The hub is back after an offline start: confirm the session (or go to sign-in). */
  async function confirm(): Promise<void> {
    if (!state.offline) return;
    try {
      const s = await api<{ user: User }>('/api/auth/session');
      state.user = s.user;
      state.offline = false;
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        state.user = undefined;
        state.offline = false;
        state.phase = 'login';
      }
    }
  }

  /** The snapshot an offline start opened on, handed over once. */
  function takeRestored(): Snapshot | undefined {
    const s = restored;
    restored = undefined;
    return s;
  }

  /** The hub's first-paint snapshot, handed over once. */
  function takeInline(): InlineSnapshot | undefined {
    const s = inline;
    inline = undefined;
    return s;
  }

  /** Is the session still good? Used when the realtime socket keeps failing. */
  async function check(): Promise<boolean> {
    try {
      await api('/api/auth/session');
      return true;
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        state.user = undefined;
        state.phase = 'login';
        return false;
      }
      return true; // unreachable is the banner's job, not a sign-out
    }
  }

  return { state, refresh, setup, login, signedIn, logout, check, confirm, takeRestored, takeInline };
}

export type Session = ReturnType<typeof createSession>;

const KEY: InjectionKey<Session> = Symbol('session');
export const provideSession = (s: Session) => provide(KEY, s);
export function useSession(): Session {
  const s = inject(KEY);
  if (!s) throw new Error('useSession() outside the app');
  return s;
}
