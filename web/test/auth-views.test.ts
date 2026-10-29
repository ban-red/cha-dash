import { afterEach, describe, expect, it, vi } from 'vitest';
import { flushPromises } from '@vue/test-utils';
import SetupView from '../src/views/SetupView.vue';
import LoginView from '../src/views/LoginView.vue';
import { harness } from './harness';

afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

describe('first-run setup', () => {
  it('asks for no setup token and names the owner admin', () => {
    const { mountIn } = harness();
    const w = mountIn(SetupView);
    expect(w.find('#setup-token').exists()).toBe(false);
    expect((w.find('#setup-user').element as HTMLInputElement).value).toBe('admin');
  });

  it('checks the form before asking the hub', async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    const { mountIn } = harness();
    const w = mountIn(SetupView);
    await w.find('#setup-pass').setValue('short');
    await w.find('#setup-confirm').setValue('short');
    await w.find('form').trigger('submit');
    expect(fetchMock).not.toHaveBeenCalled();
    expect(w.find('#setup-pass').attributes('aria-invalid')).toBe('true');
    expect(w.find('#setup-pass-help').text()).toContain('1 to go');

    await w.find('#setup-pass').setValue('correct horse battery');
    await w.find('#setup-confirm').setValue('correct horse batterY');
    await w.find('form').trigger('submit');
    expect(w.find('#setup-confirm-help').text()).toBe("The two passwords don't match.");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('creates the owner and signs in', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(201, { user: { id: 'u1', username: 'owner', role: 'owner' } }));
    vi.stubGlobal('fetch', fetchMock);
    const { mountIn, session } = harness();
    session.state.phase = 'setup';
    const w = mountIn(SetupView);
    await w.find('#setup-pass').setValue('correct horse battery');
    await w.find('#setup-confirm').setValue('correct horse battery');
    await w.find('form').trigger('submit');
    await flushPromises();
    expect(JSON.parse(fetchMock.mock.calls[0]![1].body)).toEqual({ username: 'admin', password: 'correct horse battery' });
    expect(session.state.phase).toBe('ready');
  });

  it('says so when someone else finished setup first', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(409, { error: { code: 'setup_complete', message: 'setup has already been completed' } })));
    const { mountIn } = harness();
    const w = mountIn(SetupView);
    await w.find('#setup-pass').setValue('correct horse battery');
    await w.find('#setup-confirm').setValue('correct horse battery');
    await w.find('form').trigger('submit');
    await flushPromises();
    expect(w.find('.form-error').text()).toBe('Setup is already done. Sign in instead.');
  });
});

describe('sign-in', () => {
  it('signs in', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(200, { user: { id: 'u1', username: 'owner', role: 'owner' } }));
    vi.stubGlobal('fetch', fetchMock);
    const { mountIn, session } = harness();
    session.state.phase = 'login';
    const w = mountIn(LoginView);
    await w.find('#login-user').setValue('owner');
    await w.find('#login-pass').setValue('correct horse battery');
    await w.find('form').trigger('submit');
    await flushPromises();
    expect(fetchMock.mock.calls[0]![0]).toBe('/api/auth/login');
    expect(session.state.phase).toBe('ready');
  });

  it('says so when the password is wrong, and clears it', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(401, { error: { code: 'bad_credentials', message: 'invalid username or password' } })));
    const { mountIn, session } = harness();
    session.state.phase = 'login';
    const w = mountIn(LoginView);
    await w.find('#login-user').setValue('owner');
    await w.find('#login-pass').setValue('nope nope nope');
    await w.find('form').trigger('submit');
    await flushPromises();
    expect(w.find('.form-error').text()).toBe('That username and password don’t match.');
    expect((w.find('#login-pass').element as HTMLInputElement).value).toBe('');
    expect(session.state.phase).toBe('login');
  });
});

describe('dev login', () => {
  const settle = async () => {
    await vi.dynamicImportSettled();
    await flushPromises();
  };

  it('stays hidden unless the hub offers it', async () => {
    vi.stubGlobal('fetch', vi.fn());
    const { mountIn, session } = harness();
    session.state.phase = 'login';
    const w = mountIn(LoginView);
    await settle();
    expect(w.text()).not.toContain('Login as Local Dev');
  });

  it.each([
    ['setup', SetupView],
    ['login', LoginView],
  ] as const)('gets straight in from the %s door', async (phase, view) => {
    const fetchMock = vi.fn().mockResolvedValue(json(200, { user: { id: 'u1', username: 'dev', role: 'owner' } }));
    vi.stubGlobal('fetch', fetchMock);
    const { mountIn, session } = harness();
    session.state.phase = phase;
    session.state.devLogin = true;
    const w = mountIn(view);
    await settle();
    const button = w.findAll('button').find((b) => b.text() === 'Login as Local Dev');
    expect(button).toBeDefined();
    await button!.trigger('click');
    await flushPromises();
    expect(fetchMock.mock.calls[0]![0]).toBe('/api/auth/dev-login');
    expect(fetchMock.mock.calls[0]![1].method).toBe('POST');
    expect(session.state.phase).toBe('ready');
    expect(session.state.user?.username).toBe('dev');
  });

  it('explains a hub built without it', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(404, { error: { code: 'not_found', message: 'no such endpoint' } })));
    const { mountIn, session } = harness();
    session.state.phase = 'login';
    session.state.devLogin = true;
    const w = mountIn(LoginView);
    await settle();
    await w.findAll('button').find((b) => b.text() === 'Login as Local Dev')!.trigger('click');
    await flushPromises();
    expect(w.find('.dev-login .form-error').text()).toBe('This hub wasn’t built with dev login. Start it with bun run dev.');
    expect(session.state.phase).toBe('login');
  });
});
