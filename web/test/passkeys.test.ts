import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import { nextTick } from 'vue';
import LoginView from '../src/views/LoginView.vue';
import PasskeyOffer from '../src/views/PasskeyOffer.vue';
import SecurityPane from '../src/settings/SecurityPane.vue';
import { registerPasskey, signInWithPasskey } from '../src/lib/passkeys';
import {
  assertionJSON,
  creationOptions,
  createPasskey,
  fromB64url,
  getPasskey,
  passkeyError,
  passkeySupport,
  PasskeyError,
  registrationJSON,
  requestOptions,
  toB64url,
} from '../src/lib/webauthn';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

const bytes = (...b: number[]) => new Uint8Array(b).buffer;
const view = (b: ArrayBuffer | ArrayBufferView | BufferSource | undefined) => [...new Uint8Array(b instanceof ArrayBuffer ? b : (b as ArrayBufferView).buffer)];

/** What go-webauthn sends from register/begin. */
const CREATION = {
  publicKey: {
    rp: { name: 'Cha Dash', id: 'localhost' },
    user: { name: 'owner', displayName: 'owner', id: 'AQID' },
    challenge: 'q83vEjRWeJA',
    pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
    timeout: 300000,
    excludeCredentials: [{ type: 'public-key', id: '-_8', transports: ['internal'] }],
    authenticatorSelection: { residentKey: 'required', userVerification: 'preferred' },
    attestation: 'none',
  },
};
const REQUEST = { publicKey: { challenge: 'AAEC', timeout: 300000, rpId: 'localhost', userVerification: 'preferred' } };

function fakeCredential(kind: 'create' | 'get') {
  const response =
    kind === 'create'
      ? { clientDataJSON: bytes(123, 125), attestationObject: bytes(1, 2, 3), getTransports: () => ['internal', 'hybrid'] }
      : { clientDataJSON: bytes(123, 125), authenticatorData: bytes(9, 9), signature: bytes(0xfb, 0xff), userHandle: bytes(1, 2, 3) };
  return { id: 'cred-1', rawId: bytes(0xfb, 0xef, 0xff), type: 'public-key', authenticatorAttachment: 'platform', response, getClientExtensionResults: () => ({ credProps: { rk: true } }) };
}

function installWebAuthn(creds: Partial<CredentialsContainer>, statics: Record<string, unknown> = {}) {
  const PKC = Object.assign(function PublicKeyCredential() {}, statics);
  vi.stubGlobal('PublicKeyCredential', PKC);
  Object.defineProperty(navigator, 'credentials', { value: creds, configurable: true });
  Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true });
}

afterEach(() => {
  vi.unstubAllGlobals();
  Object.defineProperty(window, 'isSecureContext', { value: undefined, configurable: true });
  document.body.innerHTML = '';
});

describe('base64url', () => {
  it('round-trips bytes and reads padded or plain base64 too', () => {
    const all = new Uint8Array(256).map((_, i) => i);
    expect(view(fromB64url(toB64url(all)))).toEqual([...all]);
    expect(toB64url(new TextEncoder().encode('hello'))).toBe('aGVsbG8');
    expect(toB64url(bytes(0xfb, 0xff))).toBe('-_8');
    expect(view(fromB64url('-_8'))).toEqual([0xfb, 0xff]);
    expect(view(fromB64url('+/8='))).toEqual([0xfb, 0xff]);
    expect(view(fromB64url(''))).toEqual([]);
    expect(() => fromB64url('abcde')).toThrow(PasskeyError);
  });
});

describe('options and credentials as JSON', () => {
  it('turns creation options into buffers where WebAuthn wants them', () => {
    const o = creationOptions(CREATION);
    expect(view(o.challenge)).toEqual([0xab, 0xcd, 0xef, 0x12, 0x34, 0x56, 0x78, 0x90]);
    expect(view(o.user.id)).toEqual([1, 2, 3]);
    expect(view(o.excludeCredentials![0]!.id)).toEqual([0xfb, 0xff]);
    expect(o.excludeCredentials![0]!.transports).toEqual(['internal']);
    expect(o.rp).toEqual({ name: 'Cha Dash', id: 'localhost' });
    expect(o.authenticatorSelection?.residentKey).toBe('required');
    expect(() => creationOptions({ publicKey: { challenge: 1 } })).toThrow('malformed');
  });

  it('keeps sign-in usernameless (no allowCredentials)', () => {
    const o = requestOptions(REQUEST);
    expect(view(o.challenge)).toEqual([0, 1, 2]);
    expect(o.allowCredentials).toBeUndefined();
    expect(o.rpId).toBe('localhost');
  });

  it('writes the credential the way toJSON() does', () => {
    expect(registrationJSON(fakeCredential('create'))).toEqual({
      id: 'cred-1',
      rawId: '--__',
      type: 'public-key',
      response: { clientDataJSON: 'e30', attestationObject: 'AQID', transports: ['internal', 'hybrid'] },
      clientExtensionResults: { credProps: { rk: true } },
      authenticatorAttachment: 'platform',
    });
    const a = assertionJSON(fakeCredential('get'));
    expect(a['response']).toEqual({ clientDataJSON: 'e30', authenticatorData: 'CQk', signature: '-_8', userHandle: 'AQID' });
    const noHandle = fakeCredential('get');
    (noHandle.response as { userHandle: ArrayBuffer | null }).userHandle = null;
    expect((assertionJSON(noHandle)['response'] as Record<string, unknown>)['userHandle']).toBeUndefined();
  });

  it('prefers the browser’s own parsers and toJSON when it has them', async () => {
    const parsed = { challenge: new ArrayBuffer(1) };
    const parseRequestOptionsFromJSON = vi.fn(() => parsed);
    const native = { ...fakeCredential('get'), toJSON: () => ({ id: 'native' }) };
    const get = vi.fn().mockResolvedValue(native);
    installWebAuthn({ get }, { parseRequestOptionsFromJSON });
    expect(await getPasskey(REQUEST, { mediation: 'conditional' })).toEqual({ id: 'native' });
    expect(parseRequestOptionsFromJSON).toHaveBeenCalledWith(REQUEST.publicKey);
    expect(get).toHaveBeenCalledWith({ publicKey: parsed, mediation: 'conditional' });
  });
});

describe('errors', () => {
  const dom = (name: string) => Object.assign(new Error(name), { name });

  it('says what happened in words', () => {
    expect(passkeyError(dom('NotAllowedError'), 'get').kind).toBe('not-allowed');
    expect(passkeyError(dom('NotAllowedError'), 'get').message).toContain('closed, timed out, or this device didn’t allow it');
    expect(passkeyError(dom('AbortError'), 'get').kind).toBe('cancelled');
    expect(passkeyError(dom('InvalidStateError'), 'create').kind).toBe('exists');
    expect(passkeyError(dom('SecurityError'), 'create').message).toContain('CHADASH_PUBLIC_URL');
    expect(passkeyError(dom('Weird'), 'create').kind).toBe('failed');
  });

  it('knows when passkeys can’t work here', () => {
    expect(passkeySupport({ isSecureContext: false, PublicKeyCredential: {}, navigator: { credentials: {} } })).toBe('insecure');
    expect(passkeySupport({ isSecureContext: true, navigator: {} })).toBe('unsupported');
    expect(passkeySupport({ isSecureContext: true, PublicKeyCredential: {}, navigator: { credentials: {} } })).toBe('ok');
  });

  it('refuses on an insecure page without calling the browser', async () => {
    Object.defineProperty(window, 'isSecureContext', { value: false, configurable: true });
    await expect(createPasskey(CREATION)).rejects.toMatchObject({ kind: 'insecure', message: expect.stringContaining('https://') });
  });

  it('maps a user cancel from create', async () => {
    installWebAuthn({ create: vi.fn().mockRejectedValue(dom('NotAllowedError')) });
    await expect(createPasskey(CREATION)).rejects.toMatchObject({ kind: 'not-allowed' });
  });
});

describe('against the hub', () => {
  it('registers: begin, create with buffers, finish with the JSON', async () => {
    const f = fakeFetch({
      'POST /api/auth/passkeys/register/begin': { status: 200, body: CREATION },
      'POST /api/auth/passkeys/register/finish': { status: 201, body: { id: 'pk1', name: 'iCloud Keychain' } },
    });
    const create = vi.fn().mockResolvedValue(fakeCredential('create'));
    installWebAuthn({ create });
    expect(await registerPasskey()).toEqual({ id: 'pk1', name: 'iCloud Keychain' });
    const arg = create.mock.calls[0]![0] as CredentialCreationOptions;
    expect(arg.publicKey!.challenge).toBeInstanceOf(ArrayBuffer);
    expect(f.last('POST', '/api/auth/passkeys/register/finish')!.body).toMatchObject({ id: 'cred-1', rawId: '--__', response: { attestationObject: 'AQID' } });
  });

  it('signs in and passes the hub’s words through', async () => {
    fakeFetch({
      'POST /api/auth/passkeys/login/begin': { status: 200, body: REQUEST },
      'POST /api/auth/passkeys/login/finish': { status: 200, body: { user: { id: 'u1', username: 'owner', role: 'owner' } } },
    });
    installWebAuthn({ get: vi.fn().mockResolvedValue(fakeCredential('get')) });
    expect(await signInWithPasskey()).toEqual({ user: { id: 'u1', username: 'owner', role: 'owner' } });
  });

  it('shows passkeys_unavailable as is, and signals an unknown passkey to the password manager', async () => {
    fakeFetch({
      'POST /api/auth/passkeys/login/begin': { status: 409, body: { error: { code: 'passkeys_unavailable', message: 'Open Cha Dash at http://localhost:5173 instead' } } },
    });
    installWebAuthn({ get: vi.fn() });
    await expect(signInWithPasskey()).rejects.toMatchObject({ kind: 'unavailable', message: 'Open Cha Dash at http://localhost:5173 instead.' });

    const signalUnknownCredential = vi.fn().mockResolvedValue(undefined);
    fakeFetch({
      'POST /api/auth/passkeys/login/begin': { status: 200, body: REQUEST },
      'POST /api/auth/passkeys/login/finish': {
        status: 401,
        body: { error: { code: 'unknown_passkey', message: 'unknown passkey' }, signal: { rpId: 'localhost', credentialId: '--__' } },
      },
    });
    installWebAuthn({ get: vi.fn().mockResolvedValue(fakeCredential('get')) }, { signalUnknownCredential });
    await expect(signInWithPasskey()).rejects.toMatchObject({ kind: 'unknown' });
    expect(signalUnknownCredential).toHaveBeenCalledWith({ rpId: 'localhost', credentialId: '--__' });
  });

  it('explains an expired ceremony and an existing passkey', async () => {
    fakeFetch({
      'POST /api/auth/passkeys/register/begin': { status: 200, body: CREATION },
      'POST /api/auth/passkeys/register/finish': { status: 400, body: { error: { code: 'ceremony_expired', message: 'ceremony expired' } } },
    });
    installWebAuthn({ create: vi.fn().mockResolvedValue(fakeCredential('create')) });
    await expect(registerPasskey()).rejects.toMatchObject({ kind: 'expired' });
    fakeFetch({
      'POST /api/auth/passkeys/register/begin': { status: 200, body: CREATION },
      'POST /api/auth/passkeys/register/finish': { status: 409, body: { error: { code: 'passkey_exists', message: 'exists' } } },
    });
    await expect(registerPasskey()).rejects.toMatchObject({ kind: 'exists' });
  });
});

describe('sign-in door', () => {
  beforeEach(() => vi.stubGlobal('fetch', vi.fn()));

  it('signs in with a passkey', async () => {
    fakeFetch({
      'POST /api/auth/passkeys/login/begin': { status: 200, body: REQUEST },
      'POST /api/auth/passkeys/login/finish': { status: 200, body: { user: { id: 'u1', username: 'owner', role: 'owner' } } },
    });
    installWebAuthn({ get: vi.fn().mockResolvedValue(fakeCredential('get')) });
    const { mountIn, session } = harness();
    session.state.phase = 'login';
    const w = mountIn(LoginView);
    expect(w.find('#login-user').attributes('autocomplete')).toBe('username webauthn');
    await w.find('.pk-btn').trigger('click');
    await flushPromises();
    expect(session.state.phase).toBe('ready');
    expect(session.state.user?.username).toBe('owner');
  });

  it('stays quiet when the person cancels, and explains other failures', async () => {
    const get = vi.fn().mockRejectedValue(Object.assign(new Error('x'), { name: 'AbortError' }));
    fakeFetch({ 'POST /api/auth/passkeys/login/begin': { status: 200, body: REQUEST } });
    installWebAuthn({ get });
    const { mountIn, session } = harness();
    session.state.phase = 'login';
    const w = mountIn(LoginView);
    await w.find('.pk-btn').trigger('click');
    await flushPromises();
    expect(w.find('.form-error').exists()).toBe(false);
    get.mockRejectedValue(Object.assign(new Error('x'), { name: 'NotAllowedError' }));
    await w.find('.pk-btn').trigger('click');
    await flushPromises();
    expect(w.find('.form-error').text()).toContain('No passkey was used');
    expect(session.state.phase).toBe('login');
  });

  it('explains inline that passkeys need HTTPS or localhost', () => {
    Object.defineProperty(window, 'isSecureContext', { value: false, configurable: true });
    const { mountIn } = harness();
    const w = mountIn(LoginView);
    expect(w.find('.pk-btn').exists()).toBe(false);
    expect(w.find('.pk-off').text()).toContain('https://');
    expect(w.find('.pk-off').text()).toContain('localhost');
  });
});

describe('after first-run setup', () => {
  it('offers a passkey, skippably', async () => {
    installWebAuthn({ create: vi.fn() });
    const { mountIn, session } = harness();
    session.state.offerPasskey = true;
    const w = mountIn(PasskeyOffer);
    expect(w.find('.caps').text()).toBe('First run · step 2 of 2');
    await w.findAll('button').find((b) => b.text() === 'Skip for now')!.trigger('click');
    expect(session.state.offerPasskey).toBe(false);
  });

  it('adds one and says how to use it', async () => {
    fakeFetch({
      'POST /api/auth/passkeys/register/begin': { status: 200, body: CREATION },
      'POST /api/auth/passkeys/register/finish': { status: 201, body: { id: 'pk1', name: 'iCloud Keychain' } },
    });
    installWebAuthn({ create: vi.fn().mockResolvedValue(fakeCredential('create')) });
    const { mountIn, session } = harness();
    session.state.offerPasskey = true;
    const w = mountIn(PasskeyOffer);
    await w.find('.btn.primary').trigger('click');
    await flushPromises();
    expect(w.find('h1').text()).toBe('You’re set.');
    expect(w.find('.lede').text()).toContain('Added “iCloud Keychain”.');
    await w.find('.btn.primary').trigger('click');
    expect(session.state.offerPasskey).toBe(false);
  });

  it('lets you continue when this connection can’t do passkeys', () => {
    Object.defineProperty(window, 'isSecureContext', { value: false, configurable: true });
    const { mountIn } = harness();
    const w = mountIn(PasskeyOffer);
    expect(w.find('.door-note').text()).toContain('secure connection');
    expect(w.find('.btn.primary').text()).toBe('Continue');
  });
});

describe('Settings → Security', () => {
  const LIST = [
    { id: 'pk1', name: 'iCloud Keychain', created_at: '2026-10-01T10:00:00Z', last_used_at: null, rp_id: 'localhost', backed_up: true, clone_warning: false },
    { id: 'pk2', name: 'YubiKey', created_at: '2026-10-02T10:00:00Z', last_used_at: '2026-10-03T09:00:00Z', rp_id: 'dash.lab.example', backed_up: false, clone_warning: true },
  ];

  it('lists, renames and deletes passkeys', async () => {
    const f = fakeFetch({
      'GET /api/auth/passkeys': { status: 200, body: LIST },
      'PATCH /api/auth/passkeys/pk1': (body) => ({ status: 200, body: { ...LIST[0], ...(body as object) } }),
      'DELETE /api/auth/passkeys/pk2': { status: 204 },
    });
    installWebAuthn({ create: vi.fn() });
    const { mountIn } = harness();
    const w = mountIn(SecurityPane);
    await flushPromises();
    const rows = w.findAll('.st-list > li');
    expect(rows).toHaveLength(2);
    expect(rows[0]!.text()).toContain('synced');
    expect(rows[0]!.text()).toContain('never used');
    expect(rows[1]!.text()).toContain('for dash.lab.example');
    expect(rows[1]!.find('.pk-warn').text()).toContain('two copies');

    await rows[0]!.findAll('button').find((b) => b.text() === 'Rename')!.trigger('click');
    await nextTick();
    const input = w.find<HTMLInputElement>('#pk-name-pk1');
    await input.setValue('   ');
    await w.find('.pk-rename').trigger('submit');
    await flushPromises();
    expect(w.text()).toContain('Give it a name.');
    await input.setValue('MacBook Touch ID');
    await w.find('.pk-rename').trigger('submit');
    await flushPromises();
    expect(f.last('PATCH', '/api/auth/passkeys/pk1')!.body).toEqual({ name: 'MacBook Touch ID' });
    expect(w.findAll('.st-list > li')[0]!.text()).toContain('MacBook Touch ID');

    await w.findAll('.st-list > li')[1]!.findAll('button').find((b) => b.text() === 'Delete…')!.trigger('click');
    expect(f.calls.some((c) => c.method === 'DELETE')).toBe(false);
    await w.find('.pk-confirm .btn.danger').trigger('click');
    await flushPromises();
    expect(f.last('DELETE', '/api/auth/passkeys/pk2')).toBeDefined();
    expect(w.findAll('.st-list > li')).toHaveLength(1);
  });

  it('keeps the last way in, and says why', async () => {
    fakeFetch({
      'GET /api/auth/passkeys': { status: 200, body: [LIST[0]] },
      'DELETE /api/auth/passkeys/pk1': { status: 409, body: { error: { code: 'last_sign_in_method', message: 'last sign-in method' } } },
    });
    installWebAuthn({ create: vi.fn() });
    const { mountIn } = harness();
    const w = mountIn(SecurityPane);
    await flushPromises();
    await w.findAll('button').find((b) => b.text() === 'Delete…')!.trigger('click');
    await w.find('.pk-confirm .btn.danger').trigger('click');
    await flushPromises();
    expect(w.find('.st-list .err').text()).toContain('only way left to sign in');
    expect(w.findAll('.st-list > li')).toHaveLength(1);
  });

  it('explains why it can’t add one on a plain http address', async () => {
    fakeFetch({ 'GET /api/auth/passkeys': { status: 200, body: [] } });
    Object.defineProperty(window, 'isSecureContext', { value: false, configurable: true });
    const { mountIn } = harness();
    const w = mountIn(SecurityPane);
    await flushPromises();
    expect(w.find('.st-note.hatched').text()).toContain('Passkeys need a secure connection');
    expect(w.findAll('button').some((b) => b.text() === 'Add a passkey')).toBe(false);
  });
});
