/**
 * Who is signed in, and which door the app shows: first-run setup, sign-in,
 * or the front page. Plain Vue reactivity; no store library.
 */
import { inject, provide, reactive, type InjectionKey } from 'vue';
import { ApiError, api } from '../lib/api';

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
}

/** Mirrors internal/auth: 1–64 of letters, digits, . _ @ -, starting with a letter or digit. */
export const USERNAME_RE = /^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$/;
export const MIN_PASSWORD = 12;

/** Password length the way the hub counts it: characters, not bytes. */
export const passwordLength = (p: string) => [...p].length;

/** A sentence for the person at the form. Never raw server text for auth failures. */
export function authMessage(err: unknown, context: 'setup' | 'login'): string {
  if (!(err instanceof ApiError)) return 'Something went wrong. Try again.';
  if (err.status === 0) return "Can't reach the Cha Dash hub. Check that it's running, then try again.";
  if (err.status === 429) {
    return err.retryAfter ? `Too many attempts. Try again in ${err.retryAfter} s.` : 'Too many attempts. Try again in a minute.';
  }
  if (context === 'setup') {
    if (err.status === 403) return "That setup token doesn't match. Copy it again from the hub's log.";
    if (err.status === 409) return 'Setup is already done. Sign in instead.';
    if (err.status === 422) return sentence(err.message);
  } else if (err.status === 401) {
    return 'That username and password don’t match.';
  }
  return 'The hub couldn’t do that. Try again.';
}

const sentence = (s: string) => (s ? s[0]!.toUpperCase() + s.slice(1) + (/[.!?]$/.test(s) ? '' : '.') : s);

export function createSession() {
  const state = reactive<SessionState>({ phase: 'loading', user: undefined, error: '' });

  async function refresh(): Promise<void> {
    try {
      const setup = await api<{ required: boolean }>('/api/setup');
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
      state.error = err instanceof ApiError && err.status === 0 ? "Can't reach the Cha Dash hub." : 'The hub answered with an error.';
      state.phase = 'unreachable';
    }
  }

  async function setup(token: string, username: string, password: string): Promise<void> {
    try {
      const res = await api<{ user: User }>('/api/setup', { method: 'POST', body: { token: token.trim(), username, password } });
      state.user = res.user;
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

  async function logout(): Promise<void> {
    try {
      await api('/api/auth/logout', { method: 'POST' });
    } finally {
      state.user = undefined;
      state.phase = 'login';
    }
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

  return { state, refresh, setup, login, logout, check };
}

export type Session = ReturnType<typeof createSession>;

const KEY: InjectionKey<Session> = Symbol('session');
export const provideSession = (s: Session) => provide(KEY, s);
export function useSession(): Session {
  const s = inject(KEY);
  if (!s) throw new Error('useSession() outside the app');
  return s;
}
