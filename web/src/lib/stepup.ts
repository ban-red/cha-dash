/**
 * Step-up: re-verify who you are before a T3+ action or a change to the
 * hub's security (08 · v0.2 → Step-up). A grant lasts 5 minutes on the
 * session; T4 always asks again.
 *
 *   POST /api/auth/step-up/begin  → WebAuthn request options ({publicKey}),
 *                                   or {"method": "password"} when the fallback applies
 *   POST /api/auth/step-up        the assertion, or {"password"} → {"until"}
 *
 * Loaded with the step-up dialog, so the WebAuthn code stays out of the
 * front page until someone needs it.
 */
import { ApiError, api } from './api';
import { getPasskey, PasskeyError, type CeremonyOptions } from './webauthn';

export type StepUpBegin = { method: 'passkey'; options: unknown } | { method: 'password' } | { method: 'none'; message: string; addPasskey: boolean };

/** Codes a hub may use when no method is left (Passkey only, and no passkey yet). */
const NO_METHOD = new Set(['passkey_required', 'no_passkey', 'step_up_unavailable', 'passkeys_unavailable']);

export const PASSKEY_ONLY_MESSAGE =
  'This hub asks for a passkey before destructive actions, and your account doesn’t have one yet. Add one in Settings → Security, then try again.';

/** Ask the hub how to step up. A refusal that leaves no way (Passkey only, no passkey) comes back as `none`. */
export async function beginStepUp(): Promise<StepUpBegin> {
  try {
    const res = await api<{ method?: string; publicKey?: unknown }>('/api/auth/step-up/begin', { method: 'POST' });
    if (res?.method === 'password') return { method: 'password' };
    return { method: 'passkey', options: res };
  } catch (err) {
    if (err instanceof ApiError && (NO_METHOD.has(err.code) || err.status === 409 || err.status === 412)) {
      const said = (err.body as { error?: { message?: unknown } } | undefined)?.error?.message;
      const addPasskey = err.code !== 'passkeys_unavailable';
      return { method: 'none', message: typeof said === 'string' && said ? said : PASSKEY_ONLY_MESSAGE, addPasskey };
    }
    throw stepUpError(err);
  }
}

/** Use a passkey: the browser's prompt, then the assertion to the hub. Resolves the grant's end. */
export async function stepUpWithPasskey(options: unknown, opts: CeremonyOptions = {}): Promise<string | undefined> {
  const assertion = await getPasskey(options, opts);
  try {
    return (await api<{ until?: string }>('/api/auth/step-up', { method: 'POST', body: assertion }))?.until;
  } catch (err) {
    throw stepUpError(err);
  }
}

export async function stepUpWithPassword(password: string): Promise<string | undefined> {
  try {
    return (await api<{ until?: string }>('/api/auth/step-up', { method: 'POST', body: { password } }))?.until;
  } catch (err) {
    throw stepUpError(err, 'password');
  }
}

/** A sentence for a failed step-up. The hub's own message wins when it sent one. */
export function stepUpError(err: unknown, how: 'passkey' | 'password' = 'passkey'): Error {
  if (err instanceof PasskeyError || !(err instanceof ApiError)) return err as Error;
  if (err.status === 0) return new Error('Can’t reach the Cha Dash hub.');
  if (err.status === 429) return new Error(err.retryAfter ? `Too many attempts. Try again in ${err.retryAfter} s.` : 'Too many attempts. Try again in a minute.');
  const said = (err.body as { error?: { message?: unknown } } | undefined)?.error?.message;
  if (typeof said === 'string' && said) return new Error(said);
  if (err.status === 401 || err.status === 403) return new Error(how === 'password' ? 'That password doesn’t match.' : 'The hub couldn’t verify that passkey. Try again.');
  if (err.code === 'ceremony_expired' || err.status === 410) return new Error('That took too long, so the request lapsed. Try again.');
  return new Error('The hub couldn’t confirm it’s you. Try again.');
}
