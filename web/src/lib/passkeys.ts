/**
 * Passkeys against the hub (internal/auth, go-webauthn; discoverable
 * credentials, usernameless sign-in). Loaded on demand: the doors and
 * Settings → Security import this when it's needed, so the WebAuthn code never
 * rides in the initial bundle.
 *
 *   POST /api/auth/passkeys/register/begin   → {publicKey}            (owner)
 *   POST /api/auth/passkeys/register/finish  credential.toJSON() → 201 {id, name}
 *   POST /api/auth/passkeys/login/begin      → {publicKey}
 *   POST /api/auth/passkeys/login/finish     assertion.toJSON() → 200 {user} + session cookie
 *   GET  /api/auth/passkeys                  → [{id, name, created_at, last_used_at, rp_id, backed_up, clone_warning}]
 *   PATCH  /api/auth/passkeys/{id}           {name} → 200 the passkey
 *   DELETE /api/auth/passkeys/{id}           → 204
 *
 * Each begin sets an HttpOnly ceremony cookie that the same-origin finish
 * sends back; nothing for the page to handle. Errors carry codes:
 * ceremony_expired, passkey_rejected, unknown_passkey (with a `signal` for
 * the password manager), passkey_exists, passkeys_unavailable (a message
 * that says what to change, shown as is), last_sign_in_method.
 */
import { ApiError, api, IP_BANNED_MESSAGE } from './api';
import { createPasskey, getPasskey, PasskeyError, signalUnknown, type CeremonyOptions } from './webauthn';

export interface Passkey {
  id: string;
  name: string;
  created_at?: string;
  last_used_at?: string | null;
  rp_id?: string;
  /** Synced by its provider (iCloud Keychain, 1Password, …). */
  backed_up?: boolean;
  /** Its sign counter went backwards once: two copies may be in use. */
  clone_warning?: boolean;
}

export interface SignedInUser {
  id: string;
  username: string;
  role: string;
}

interface Begin {
  publicKey?: unknown;
}

const sentence = (s: string) => (s ? s[0]!.toUpperCase() + s.slice(1) + (/[.!?]$/.test(s) ? '' : '.') : s);

/** A sentence for a hub error in a passkey flow. */
export function passkeyHubError(err: unknown, op: 'register' | 'login' | 'manage'): Error {
  if (err instanceof PasskeyError || !(err instanceof ApiError)) return err as Error;
  switch (err.code) {
    case 'passkeys_unavailable':
      // The hub's message says what to change ("open Cha Dash at …"); show it as is.
      return new PasskeyError('unavailable', err.message ? sentence(err.message) : 'Passkeys can’t work at this address.');
    case 'ceremony_expired':
      return new PasskeyError('expired', 'That took too long, so the request lapsed. Try again.');
    case 'passkey_rejected':
      return new PasskeyError(
        'failed',
        op === 'login' ? 'The hub couldn’t verify that passkey. Try again, or use your password.' : 'The hub couldn’t verify the new passkey. Try again.',
      );
    case 'unknown_passkey':
      signalUnknown((err.body as { signal?: unknown } | undefined)?.signal);
      return new PasskeyError('unknown', 'This hub doesn’t know that passkey; it may have been deleted. Sign in with your password.');
    case 'passkey_exists':
      return new PasskeyError('exists', 'This device already has a passkey for Cha Dash.');
    case 'ip_banned':
      return new PasskeyError('failed', IP_BANNED_MESSAGE);
    case 'last_sign_in_method':
      return new PasskeyError('failed', 'It’s the only way left to sign in to this account, so it stays. Add another passkey first.');
  }
  if (err.status === 0) return new PasskeyError('failed', 'Can’t reach the Cha Dash hub.');
  if (err.status === 404) return new PasskeyError('unsupported', op === 'manage' ? 'That passkey is already gone.' : 'This hub doesn’t offer passkeys yet.');
  if (err.status === 429) {
    return new PasskeyError('failed', err.retryAfter ? `Too many attempts. Try again in ${err.retryAfter} s.` : 'Too many attempts. Try again in a minute.');
  }
  if (err.status === 422) return new PasskeyError('invalid', err.message ? sentence(err.message) : 'The hub didn’t accept that.');
  if (op === 'login' && err.status === 401) return new PasskeyError('failed', 'The hub couldn’t verify that passkey. Try again, or use your password.');
  return new PasskeyError('failed', 'The hub couldn’t do that. Try again.');
}

/** Create a passkey for the signed-in owner. */
export async function registerPasskey(opts: CeremonyOptions = {}): Promise<Passkey> {
  try {
    const begin = await api<Begin>('/api/auth/passkeys/register/begin', { method: 'POST' });
    const credential = await createPasskey(begin, opts);
    return await api<Passkey>('/api/auth/passkeys/register/finish', { method: 'POST', body: credential });
  } catch (err) {
    throw passkeyHubError(err, 'register');
  }
}

/**
 * Usernameless sign-in. Resolves once the hub has set the session cookie.
 * With `mediation: 'conditional'` the browser offers passkeys in the username
 * field's autofill instead of a dialog, and waits until one is picked (or the
 * signal aborts).
 */
export async function signInWithPasskey(opts: CeremonyOptions = {}): Promise<{ user?: SignedInUser }> {
  try {
    const begin = await api<Begin>('/api/auth/passkeys/login/begin', { method: 'POST' });
    const assertion = await getPasskey(begin, opts);
    return (await api<{ user?: SignedInUser }>('/api/auth/passkeys/login/finish', { method: 'POST', body: assertion })) ?? {};
  } catch (err) {
    throw passkeyHubError(err, 'login');
  }
}

export async function listPasskeys(): Promise<Passkey[]> {
  const res = await api<Passkey[] | { passkeys?: Passkey[] }>('/api/auth/passkeys');
  return Array.isArray(res) ? res : (res?.passkeys ?? []);
}

/** The hub's rule: 1–64 characters after trimming, no control characters. */
export function passkeyNameProblem(name: string): string | undefined {
  const n = name.trim();
  if (!n) return 'Give it a name.';
  if ([...n].length > 64) return 'At most 64 characters.';
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f-\u009f]/.test(n)) return 'No control characters.';
  return undefined;
}

export async function renamePasskey(id: string, name: string): Promise<Passkey | undefined> {
  try {
    return await api<Passkey | undefined>(`/api/auth/passkeys/${encodeURIComponent(id)}`, { method: 'PATCH', body: { name: name.trim() } });
  } catch (err) {
    throw passkeyHubError(err, 'manage');
  }
}

export async function deletePasskey(id: string): Promise<void> {
  try {
    await api<void>(`/api/auth/passkeys/${encodeURIComponent(id)}`, { method: 'DELETE' });
  } catch (err) {
    throw passkeyHubError(err, 'manage');
  }
}
