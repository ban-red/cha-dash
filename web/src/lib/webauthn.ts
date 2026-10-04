/**
 * The WebAuthn browser calls, without a library. The hub (go-webauthn) sends
 * options as JSON with every binary field unpadded base64url, and reads the
 * credential back the same way: the shape of `PublicKeyCredential.toJSON()`.
 *
 *   begin → {publicKey: {challenge, user: {id}, excludeCredentials: [{id}], …}}
 *   finish ← {id, rawId, type, response: {clientDataJSON, attestationObject | authenticatorData, signature, userHandle}}
 *
 * Browsers that have `PublicKeyCredential.parseCreationOptionsFromJSON` /
 * `parseRequestOptionsFromJSON` and `credential.toJSON()` use them; the
 * conversions here are the fallback for the ones that don't (and what the
 * tests pin down), producing the same shapes.
 *
 * Passkeys need a secure context: HTTPS, or http://localhost. On a plain
 * http://<lan-ip> the browser has no `navigator.credentials.create` at all,
 * and the UI says so instead of offering a button that can't work.
 */

/** base64url (padded or not, or plain base64) → bytes. */
export function fromB64url(s: string): ArrayBuffer {
  const b64 = s.replace(/-/g, '+').replace(/_/g, '/').replace(/=+$/, '');
  const pad = b64.length % 4 === 2 ? '==' : b64.length % 4 === 3 ? '=' : '';
  if (b64.length % 4 === 1) throw new PasskeyError('invalid', 'The hub sent a malformed passkey request.');
  const bin = atob(b64 + pad);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out.buffer;
}

/** bytes → base64url without padding (what go-webauthn's URLEncodedBase64 reads). */
export function toB64url(data: ArrayBuffer | ArrayBufferView): string {
  const bytes = data instanceof ArrayBuffer ? new Uint8Array(data) : new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
  let bin = '';
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]!);
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

type Json = Record<string, unknown>;

interface CredentialDescriptorJSON {
  id: string;
  type?: string;
  transports?: string[];
}

const descriptors = (list: unknown): PublicKeyCredentialDescriptor[] | undefined =>
  Array.isArray(list)
    ? (list as CredentialDescriptorJSON[]).map((d) => {
        const out: PublicKeyCredentialDescriptor = { type: 'public-key', id: fromB64url(d.id) };
        if (d.transports) out.transports = d.transports as AuthenticatorTransport[];
        return out;
      })
    : undefined;

/** Accept `{publicKey: {...}}` (go-webauthn's CredentialCreation) or the inner object. */
const unwrap = (json: unknown): Json => {
  const j = (json ?? {}) as Json;
  return (typeof j['publicKey'] === 'object' && j['publicKey'] !== null ? j['publicKey'] : j) as Json;
};

export function creationOptions(json: unknown): PublicKeyCredentialCreationOptions {
  const o = unwrap(json);
  if (typeof o['challenge'] !== 'string' || typeof o['user'] !== 'object' || o['user'] === null) {
    throw new PasskeyError('invalid', 'The hub sent a malformed passkey request.');
  }
  const user = o['user'] as Json;
  const out = {
    ...o,
    challenge: fromB64url(o['challenge']),
    user: { ...user, id: fromB64url(String(user['id'])) },
  } as unknown as PublicKeyCredentialCreationOptions;
  const exclude = descriptors(o['excludeCredentials']);
  if (exclude) out.excludeCredentials = exclude;
  return out;
}

export function requestOptions(json: unknown): PublicKeyCredentialRequestOptions {
  const o = unwrap(json);
  if (typeof o['challenge'] !== 'string') throw new PasskeyError('invalid', 'The hub sent a malformed passkey request.');
  const out = { ...o, challenge: fromB64url(o['challenge']) } as unknown as PublicKeyCredentialRequestOptions;
  const allow = descriptors(o['allowCredentials']);
  if (allow) out.allowCredentials = allow;
  return out;
}

interface AttestationLike {
  clientDataJSON: ArrayBuffer;
  attestationObject: ArrayBuffer;
  getTransports?: () => string[];
}

interface AssertionLike {
  clientDataJSON: ArrayBuffer;
  authenticatorData: ArrayBuffer;
  signature: ArrayBuffer;
  userHandle: ArrayBuffer | null;
}

interface CredentialLike {
  id: string;
  rawId: ArrayBuffer;
  type: string;
  authenticatorAttachment?: string | null;
  response: AttestationLike | AssertionLike;
  getClientExtensionResults?: () => AuthenticationExtensionsClientOutputs;
}

/** A new credential, ready for /register/finish. */
export function registrationJSON(cred: CredentialLike): Json {
  const r = cred.response as AttestationLike;
  const response: Json = { clientDataJSON: toB64url(r.clientDataJSON), attestationObject: toB64url(r.attestationObject) };
  const transports = typeof r.getTransports === 'function' ? r.getTransports() : undefined;
  if (transports?.length) response['transports'] = transports;
  return {
    id: cred.id,
    rawId: toB64url(cred.rawId),
    type: cred.type,
    response,
    clientExtensionResults: cred.getClientExtensionResults?.() ?? {},
    ...(cred.authenticatorAttachment ? { authenticatorAttachment: cred.authenticatorAttachment } : {}),
  };
}

/** An assertion, ready for /login/finish. */
export function assertionJSON(cred: CredentialLike): Json {
  const r = cred.response as AssertionLike;
  const response: Json = {
    clientDataJSON: toB64url(r.clientDataJSON),
    authenticatorData: toB64url(r.authenticatorData),
    signature: toB64url(r.signature),
  };
  if (r.userHandle && r.userHandle.byteLength) response['userHandle'] = toB64url(r.userHandle);
  return {
    id: cred.id,
    rawId: toB64url(cred.rawId),
    type: cred.type,
    response,
    clientExtensionResults: cred.getClientExtensionResults?.() ?? {},
    ...(cred.authenticatorAttachment ? { authenticatorAttachment: cred.authenticatorAttachment } : {}),
  };
}

export type PasskeyErrorKind = 'cancelled' | 'not-allowed' | 'insecure' | 'unavailable' | 'unsupported' | 'exists' | 'expired' | 'unknown' | 'invalid' | 'failed';

export class PasskeyError extends Error {
  readonly kind: PasskeyErrorKind;
  constructor(kind: PasskeyErrorKind, message: string) {
    super(message);
    this.name = 'PasskeyError';
    this.kind = kind;
  }
}

export type PasskeySupport = 'ok' | 'insecure' | 'unsupported';

/** Can this page use passkeys? Insecure context is the common case on a LAN address. */
export function passkeySupport(g: { isSecureContext?: boolean; PublicKeyCredential?: unknown; navigator?: { credentials?: unknown } } = globalThis): PasskeySupport {
  if (g.isSecureContext === false) return 'insecure';
  if (typeof g.PublicKeyCredential === 'undefined' || !g.navigator?.credentials) return 'unsupported';
  return 'ok';
}

export const INSECURE_MESSAGE =
  'Passkeys need a secure connection. Open Cha Dash at its https:// address (or on localhost) to use one; the password works everywhere.';
export const UNSUPPORTED_MESSAGE = 'This browser doesn’t support passkeys. The password works everywhere.';

/** A sentence for whatever the browser threw. */
export function passkeyError(err: unknown, op: 'create' | 'get'): PasskeyError {
  if (err instanceof PasskeyError) return err;
  const name = (err as { name?: string } | null)?.name;
  switch (name) {
    case 'AbortError':
      return new PasskeyError('cancelled', 'Cancelled.');
    case 'NotAllowedError':
      return new PasskeyError(
        'not-allowed',
        op === 'create'
          ? 'The passkey wasn’t created: the prompt was closed, timed out, or this device didn’t allow it.'
          : 'No passkey was used: the prompt was closed, timed out, or this device didn’t allow it.',
      );
    case 'InvalidStateError':
      return new PasskeyError('exists', 'This device already has a passkey for Cha Dash. Use it to sign in, or remove it in Settings → Security.');
    case 'SecurityError':
      return new PasskeyError(
        'insecure',
        'The browser refused: this address doesn’t match the hub’s public URL (CHADASH_PUBLIC_URL), or the page isn’t on HTTPS.',
      );
    case 'NotSupportedError':
      return new PasskeyError('unsupported', 'This device can’t make a passkey Cha Dash accepts.');
    default:
      return new PasskeyError('failed', 'The passkey didn’t work. Try again.');
  }
}

type Credentials = Pick<CredentialsContainer, 'create' | 'get'>;

interface PublicKeyCredentialStatics {
  parseCreationOptionsFromJSON?: (json: unknown) => PublicKeyCredentialCreationOptions;
  parseRequestOptionsFromJSON?: (json: unknown) => PublicKeyCredentialRequestOptions;
  isConditionalMediationAvailable?: () => Promise<boolean>;
  signalUnknownCredential?: (o: { rpId: string; credentialId: string }) => Promise<void>;
}

const statics = (): PublicKeyCredentialStatics | undefined =>
  typeof PublicKeyCredential === 'undefined' ? undefined : (PublicKeyCredential as unknown as PublicKeyCredentialStatics);

/** The browser's own JSON parsing when it has it, else ours. */
function parseCreation(json: unknown): PublicKeyCredentialCreationOptions {
  const native = statics()?.parseCreationOptionsFromJSON;
  if (native) {
    try {
      return native.call(PublicKeyCredential, unwrap(json));
    } catch {
      /* fall through to ours, which says what's wrong in words */
    }
  }
  return creationOptions(json);
}

function parseRequest(json: unknown): PublicKeyCredentialRequestOptions {
  const native = statics()?.parseRequestOptionsFromJSON;
  if (native) {
    try {
      return native.call(PublicKeyCredential, unwrap(json));
    } catch {
      /* fall through */
    }
  }
  return requestOptions(json);
}

/** `credential.toJSON()` when the browser has it (what the hub reads as is), else ours. */
function credentialJSON(cred: CredentialLike & { toJSON?: () => unknown }, kind: 'create' | 'get'): Json {
  if (typeof cred.toJSON === 'function') {
    try {
      return cred.toJSON() as Json;
    } catch {
      /* fall through */
    }
  }
  return kind === 'create' ? registrationJSON(cred) : assertionJSON(cred);
}

/** Can the browser offer passkeys in the username field's autofill? */
export async function conditionalMediationAvailable(): Promise<boolean> {
  try {
    return (await statics()?.isConditionalMediationAvailable?.()) === true;
  } catch {
    return false;
  }
}

/** Tell the password manager to stop offering a passkey the hub doesn't know. */
export function signalUnknown(signal: unknown): void {
  const s = signal as { rpId?: unknown; credentialId?: unknown } | null;
  const fn = statics()?.signalUnknownCredential;
  if (!fn || typeof s?.rpId !== 'string' || typeof s.credentialId !== 'string') return;
  fn.call(PublicKeyCredential, { rpId: s.rpId, credentialId: s.credentialId }).catch(() => undefined);
}

export interface CeremonyOptions {
  /** A credentials container (tests pass a fake). */
  creds?: Credentials;
  signal?: AbortSignal;
  /** `conditional`: offer passkeys in the username field's autofill. */
  mediation?: CredentialMediationRequirement;
}

function container(c?: Credentials): Credentials {
  const support = passkeySupport();
  if (!c && support === 'insecure') throw new PasskeyError('insecure', INSECURE_MESSAGE);
  const found = c ?? (typeof navigator !== 'undefined' ? navigator.credentials : undefined);
  if (!found) throw new PasskeyError('unsupported', UNSUPPORTED_MESSAGE);
  return found;
}

/** Ask the authenticator for a new passkey. Returns the JSON for /register/finish. */
export async function createPasskey(optionsJSON: unknown, opts: CeremonyOptions = {}): Promise<Json> {
  const c = container(opts.creds);
  const publicKey = parseCreation(optionsJSON);
  let cred: Credential | null;
  try {
    cred = await c.create(opts.signal ? { publicKey, signal: opts.signal } : { publicKey });
  } catch (err) {
    throw passkeyError(err, 'create');
  }
  if (!cred) throw new PasskeyError('cancelled', 'Cancelled.');
  return credentialJSON(cred as unknown as CredentialLike, 'create');
}

/** Ask for an assertion (usernameless: no allowCredentials). Returns the JSON for /login/finish. */
export async function getPasskey(optionsJSON: unknown, opts: CeremonyOptions = {}): Promise<Json> {
  const c = container(opts.creds);
  const publicKey = parseRequest(optionsJSON);
  const req: CredentialRequestOptions = { publicKey };
  if (opts.signal) req.signal = opts.signal;
  if (opts.mediation) req.mediation = opts.mediation;
  let cred: Credential | null;
  try {
    cred = await c.get(req);
  } catch (err) {
    throw passkeyError(err, 'get');
  }
  if (!cred) throw new PasskeyError('cancelled', 'Cancelled.');
  return credentialJSON(cred as unknown as CredentialLike, 'get');
}
