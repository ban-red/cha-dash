/**
 * Sign-in through an identity provider or a proxy (ADR-0022): the sign-in
 * page's provider button and hidden password form, Settings → Security →
 * Sign-in, linked sign-ins, "from groups" on the Users page, and step-up at
 * the provider.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { enableAutoUnmount, flushPromises } from '@vue/test-utils';
import StepUpDialog from '../src/components/StepUpDialog.vue';
import LinkedIdentities from '../src/settings/LinkedIdentities.vue';
import SignInPane from '../src/settings/SignInPane.vue';
import UsersPane from '../src/settings/UsersPane.vue';
import { browser, identityPath, oidcStartURL, takeOidcReturn, type SignInView } from '../src/settings/signin';
import LoginView from '../src/views/LoginView.vue';
import { axeViolations } from './axe';
import { fakeFetch } from './fake-fetch';
import { harness } from './harness';

enableAutoUnmount(afterEach);

let went: string[] = [];
beforeEach(() => {
  went = [];
  vi.spyOn(browser, 'go').mockImplementation((url: string) => void went.push(url));
  history.replaceState(null, '', '/');
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

const VIEW: SignInView = {
  oidc: { enabled: true, name: 'Pocket ID', issuer: 'https://id.home.example', client_id: 'dash', scopes: ['openid', 'profile', 'email', 'groups'], group_claim: 'groups' },
  forward_auth: { enabled: false, user_header: 'Remote-User', groups_header: 'Remote-Groups', email_header: 'Remote-Email' },
  mappings: [{ group: 'lab-ops', role: 'operator' }],
  provision: false,
  hide_password_form: false,
  client_secret_set: true,
  redirect_uri: 'https://dash.home.example/api/auth/oidc/callback',
  trusted_proxies: false,
  last_test: null,
};

describe('lib: sign-in helpers', () => {
  it('builds start URLs, escapes identity paths, and takes the provider’s answer from the address bar once', () => {
    expect(oidcStartURL({ link: true, returnTo: '/settings/security' })).toBe('/api/auth/oidc/start?link=1&return=%2Fsettings%2Fsecurity');
    expect(identityPath({ issuer: 'https://id.home.example/realms/lab', subject: 'a b' })).toBe(
      '/api/auth/identities/https%3A%2F%2Fid.home.example%2Frealms%2Flab/a%20b',
    );
    history.replaceState(null, '', '/settings/security?x=1&oidc=linked');
    expect(takeOidcReturn()).toEqual({ result: 'linked' });
    expect(location.pathname + location.search).toBe('/settings/security?x=1');
    expect(takeOidcReturn()).toEqual({});
  });
});

describe('sign-in page', () => {
  it('offers the provider beside the password form, and goes there', async () => {
    fakeFetch({});
    const { mountIn, session } = harness();
    session.state.phase = 'login';
    session.state.signIn = { oidc: { name: 'Pocket ID' }, password_form: true };
    history.replaceState(null, '', '/boards/b1');
    const w = mountIn(LoginView);
    await flushPromises();
    const btn = w.findAll('button').find((b) => b.text() === 'Sign in with Pocket ID');
    expect(btn).toBeTruthy();
    expect(w.find('#login-user').exists()).toBe(true);
    await btn!.trigger('click');
    expect(went).toEqual(['/api/auth/oidc/start?return=%2Fboards%2Fb1']);
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
  });

  it('hides the password form behind a link when the owner says so, and still signs in with it', async () => {
    fakeFetch({ 'POST /api/auth/login': { status: 200, body: { user: { id: 'u1', username: 'owner', role: 'owner' } } } });
    const { mountIn, session } = harness();
    session.state.phase = 'login';
    session.state.signIn = { oidc: { name: 'Pocket ID' }, password_form: false };
    const w = mountIn(LoginView);
    await flushPromises();
    expect(w.find('#login-user').exists()).toBe(false);
    expect(document.activeElement?.textContent?.trim()).toBe('Sign in with Pocket ID');
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
    await w.findAll('button').find((b) => b.text() === 'Use a password instead')!.trigger('click');
    await flushPromises();
    expect(document.activeElement?.id).toBe('login-user');
    await w.find('#login-user').setValue('owner');
    await w.find('#login-pass').setValue('correct horse battery');
    await w.find('form').trigger('submit');
    await flushPromises();
    expect(session.state.phase).toBe('ready');
  });

  it('says why the provider’s sign-in was refused', async () => {
    fakeFetch({});
    const { mountIn, session } = harness();
    session.state.phase = 'login';
    session.state.signIn = { oidc: { name: 'Pocket ID' }, password_form: true };
    history.replaceState(null, '', '/?oidc_error=unlinked');
    const w = mountIn(LoginView);
    await flushPromises();
    expect(w.find('.form-error').text()).toContain('isn’t linked to a Cha Dash user');
    expect(location.search).toBe('');
  });

  it('shows only the password form on a hub without a provider', async () => {
    fakeFetch({});
    const { mountIn, session } = harness();
    session.state.phase = 'login';
    const w = mountIn(LoginView);
    await flushPromises();
    expect(w.text()).not.toContain('Sign in with Pocket');
    expect(w.find('#login-user').exists()).toBe(true);
  });
});

describe('Settings → Security → Sign-in', () => {
  it('loads, saves with a step-up, and never echoes the secret', async () => {
    let puts = 0;
    const f = fakeFetch({
      'GET /api/settings/sign-in': { status: 200, body: VIEW },
      'PUT /api/settings/sign-in': (body) => {
        puts++;
        if (puts === 1) return { status: 403, body: { error: { code: 'step_up_required', message: 'Confirm it’s you first.' } } };
        const b = body as Record<string, unknown>;
        return { status: 200, body: { ...VIEW, ...b, client_secret_set: true } };
      },
    });
    const { mountIn, ui } = harness();
    const w = mountIn(SignInPane);
    await flushPromises();
    expect(w.find('legend').text()).toBe('Identity provider (OIDC)');
    expect(w.text()).toContain(VIEW.redirect_uri);
    expect(w.text()).toContain('A secret is saved in the vault');
    // Forward-auth on without trusted proxies: say so.
    const fa = w.findAll('input[type="checkbox"]').find((c) => c.element.closest('label')?.textContent?.includes('Trust the user headers'))!;
    await fa.setValue(true);
    expect(w.text()).toContain('CHADASH_TRUSTED_PROXIES lists no proxy');
    // A mapping, and a new secret.
    await w.findAll('button').find((b) => b.text() === 'Add a mapping')!.trigger('click');
    const groups = w.findAll('input[placeholder="Group name"]');
    await groups[1]!.setValue('family');
    await w.findAll('select')[1]!.setValue('household');
    await w.find('input[type="password"]').setValue('FAKE-new-secret');
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);

    await w.find('form').trigger('submit');
    await flushPromises();
    expect(ui.state.stepUp?.reason).toBe('Change how people sign in');
    ui.stepUpDone(true);
    await flushPromises();
    const sent = f.last('PUT', '/api/settings/sign-in')!.body as Record<string, unknown>;
    expect(sent.client_secret).toBe('FAKE-new-secret');
    expect(sent.mappings).toEqual([
      { group: 'lab-ops', role: 'operator' },
      { group: 'family', role: 'household' },
    ]);
    expect((sent.forward_auth as { enabled: boolean }).enabled).toBe(true);
    expect((w.find('input[type="password"]').element as HTMLInputElement).value).toBe('');
    expect(ui.state.toasts.at(-1)?.text).toBe('Sign-in settings saved.');
  });

  it('tests the provider and offers a test sign-in', async () => {
    fakeFetch({
      'GET /api/settings/sign-in': { status: 200, body: VIEW },
      'POST /api/settings/sign-in/test': {
        status: 200,
        body: { ok: true, issuer: 'https://id.home.example', auth_time: 'not_listed', login_url: 'https://id.home.example/authorize?state=x' },
      },
    });
    const { mountIn } = harness();
    const w = mountIn(SignInPane);
    await flushPromises();
    await w.findAll('button').find((b) => b.text() === 'Test')!.trigger('click');
    await flushPromises();
    expect(w.find('.si-test').text()).toContain('doesn’t list auth_time');
    await w.findAll('button').find((b) => b.text() === 'Try a test sign-in')!.trigger('click');
    expect(went).toEqual(['https://id.home.example/authorize?state=x']);
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
  });

  it('shows the last test sign-in', async () => {
    fakeFetch({
      'GET /api/settings/sign-in': {
        status: 200,
        body: { ...VIEW, last_test: { at: '2026-10-30T10:00:00Z', ok: true, auth_time: true, auth_time_fresh: true, groups: ['lab-ops'], mapped_role: 'operator', username: 'sam' } },
      },
    });
    const { mountIn } = harness();
    const w = mountIn(SignInPane);
    await flushPromises();
    const facts = w.find('.si-facts').text();
    expect(facts).toContain('sam');
    expect(facts).toContain('step-up through the provider works');
    expect(facts).toContain('Operator');
  });

  it('shows nothing on a hub without it', async () => {
    fakeFetch({});
    const { mountIn } = harness();
    const w = mountIn(SignInPane);
    await flushPromises();
    expect(w.find('section').exists()).toBe(false);
  });
});

describe('Settings → Security → Linked sign-ins', () => {
  const ID = { kind: 'oidc', issuer: 'https://id.home.example', subject: 'sub-1', email: 'sam@home.example', created_at: '2026-10-01T10:00:00Z', last_used_at: null };

  it('lists, unlinks with a step-up, and links the provider after one', async () => {
    const f = fakeFetch({
      'GET /api/auth/identities': { status: 200, body: { identities: [ID], provider: 'Pocket ID' } },
      'DELETE /api/auth/identities/*': { status: 204 },
    });
    const { mountIn, ui } = harness();
    const w = mountIn(LinkedIdentities);
    await flushPromises();
    expect(w.find('.st-list').text()).toContain('sam@home.example');
    expect(w.find('.st-list').text()).toContain('at id.home.example');
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);

    await w.findAll('button').find((b) => b.text() === 'Unlink…')!.trigger('click');
    expect(w.find('[role="alertdialog"]').text()).toContain('sam@home.example');
    await w.findAll('button').find((b) => b.text().startsWith('Unlink') && b.text().includes('T3'))!.trigger('click');
    await flushPromises();
    expect(f.calls.some((c) => c.method === 'DELETE' && c.path === identityPath(ID))).toBe(true);

    const link = w.findAll('button').find((b) => b.text() === 'Link Pocket ID')!;
    await link.trigger('click');
    await flushPromises();
    expect(ui.state.stepUp?.reason).toBe('Link a sign-in to your account');
    ui.stepUpDone(true);
    await flushPromises();
    expect(went).toEqual(['/api/auth/oidc/start?link=1&return=%2Fsettings%2Fsecurity']);
  });

  it('offers to link the name a trusted proxy sends', async () => {
    let linked = false;
    fakeFetch({
      'GET /api/auth/identities': () =>
        linked
          ? { status: 200, body: { identities: [{ ...ID, kind: 'forward_auth', issuer: 'forward-auth', subject: 'sam@proxy', email: '' }] } }
          : { status: 200, body: { identities: [], forward_auth_username: 'sam@proxy' } },
      'POST /api/auth/identities/forward-auth': () => {
        linked = true;
        return { status: 201, body: { username: 'sam@proxy' } };
      },
    });
    const { mountIn } = harness();
    const w = mountIn(LinkedIdentities);
    await flushPromises();
    expect(w.text()).toContain('Your proxy says you’re sam@proxy');
    await w.findAll('button').find((b) => b.text() === 'Link sam@proxy')!.trigger('click');
    await flushPromises();
    expect(w.find('.st-list').text()).toContain('Proxy username sam@proxy');
  });

  it('stays out of the way with nothing to show', async () => {
    fakeFetch({ 'GET /api/auth/identities': { status: 200, body: { identities: [] } } });
    const { mountIn } = harness();
    const w = mountIn(LinkedIdentities);
    await flushPromises();
    expect(w.find('section').exists()).toBe(false);
  });
});

describe('Users: roles from groups', () => {
  it('marks them and offers no role picker', async () => {
    fakeFetch({
      'GET /api/users': {
        status: 200,
        body: [
          { id: 'u1', username: 'owner', role: 'owner', disabled: false, has_passkey: true, last_seen_at: null },
          { id: 'u2', username: 'sam', role: 'operator', disabled: false, has_passkey: false, last_seen_at: null, role_from_groups: true },
          { id: 'u3', username: 'kim', role: 'viewer', disabled: false, has_passkey: false, last_seen_at: null },
        ],
      },
      'GET /api/bans': { status: 200, body: [] },
    });
    const { mountIn } = harness();
    const w = mountIn(UsersPane);
    await flushPromises();
    const rows = w.findAll('.us-row');
    const sam = rows.find((r) => r.text().includes('sam'))!;
    const kim = rows.find((r) => r.text().includes('kim'))!;
    expect(sam.text()).toContain('from groups');
    expect(sam.find('select').exists()).toBe(false);
    expect(kim.find('select').exists()).toBe(true);
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
  });
});

describe('step-up at the provider', () => {
  it('sends an account without a passkey or password to the provider, and back here', async () => {
    fakeFetch({ 'POST /api/auth/step-up/begin': { status: 200, body: { method: 'oidc', url: '/api/auth/oidc/start?step_up=1', provider: 'Pocket ID' } } });
    const { mountIn, ui } = harness();
    history.replaceState(null, '', '/settings/users');
    ui.state.stepUp = { reason: 'Disable sam', fresh: false, resolve: () => undefined };
    const w = mountIn(StepUpDialog);
    await flushPromises();
    expect(document.querySelector('dialog.stepup')?.textContent).toContain('signs in with Pocket ID');
    const go = [...document.querySelectorAll<HTMLButtonElement>('dialog.stepup button')].find((b) => b.textContent?.trim() === 'Continue with Pocket ID')!;
    go.click();
    await flushPromises();
    expect(went).toEqual(['/api/auth/oidc/start?step_up=1&return=%2Fsettings%2Fusers']);
    expect(await axeViolations(w.element.parentElement!)).toEqual([]);
  });
});
