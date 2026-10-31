/**
 * Sign-in through an identity provider (OIDC) or a trusted proxy
 * (forward-auth), ADR-0022. The shapes match internal/auth and
 * internal/security:
 *
 *   GET  /api/settings/sign-in        → SignInView (owner)
 *   PUT  /api/settings/sign-in        SignInSettings & {client_secret?} → SignInView; needs a step-up
 *   POST /api/settings/sign-in/test   → SignInTest
 *   GET  /api/auth/identities         → IdentitiesView (yours)
 *   DELETE /api/auth/identities/{issuer}/{subject}   needs a step-up
 *   POST /api/auth/identities/forward-auth           link the name your proxy sends; needs a step-up
 *   GET  /api/auth/oidc/start?return=…[&link=1|&step_up=1]   a navigation, not a fetch
 *
 * The provider's redirects come back with ?oidc=linked|step_up|tested, or
 * ?oidc_error=<code>; takeOidcReturn() reads and removes them.
 */

export type GroupRole = 'admin' | 'operator' | 'viewer' | 'household';

export interface GroupMapping {
  group: string;
  role: GroupRole;
}

export interface SignInSettings {
  oidc: {
    enabled: boolean;
    name: string;
    issuer: string;
    client_id: string;
    scopes: string[];
    group_claim: string;
  };
  forward_auth: {
    enabled: boolean;
    user_header: string;
    groups_header: string;
    email_header: string;
  };
  mappings: GroupMapping[];
  provision: boolean;
  hide_password_form: boolean;
}

export interface OidcTestResult {
  at: string;
  ok: boolean;
  error?: string;
  auth_time: boolean;
  auth_time_fresh: boolean;
  groups: string[];
  mapped_role: string;
  email?: string;
  username?: string;
  linked?: string;
}

export interface SignInView extends SignInSettings {
  client_secret_set: boolean;
  redirect_uri: string;
  trusted_proxies: boolean;
  last_test: OidcTestResult | null;
  /** The groups header a trusted proxy last sent, since the hub started; null when none has. */
  forward_groups?: ForwardGroupsSeen | null;
}

export interface ForwardGroupsSeen {
  header: string;
  value: string;
  at: string;
}

export interface SignInTest {
  ok: boolean;
  error?: string;
  issuer?: string;
  authorization_endpoint?: string;
  token_endpoint?: string;
  auth_time?: 'listed' | 'not_listed' | 'unknown';
  login_url?: string;
}

export interface LinkedIdentity {
  kind: 'oidc' | 'forward_auth';
  issuer: string;
  subject: string;
  email?: string;
  created_at: string;
  last_used_at: string | null;
}

export interface IdentitiesView {
  identities: LinkedIdentity[];
  provider?: string;
  forward_auth_username?: string;
  forward_auth_linked_elsewhere?: boolean;
}

/** The public part GET /api/setup carries for the sign-in page. */
export interface PublicSignIn {
  oidc?: { name: string };
  password_form: boolean;
}

export const FORWARD_AUTH_ISSUER = 'forward-auth';

export const GROUP_ROLES: GroupRole[] = ['admin', 'operator', 'viewer', 'household'];

/** Where the browser goes to sign in, link or step up at the provider. */
export function oidcStartURL(opts: { link?: boolean; stepUp?: boolean; returnTo?: string } = {}): string {
  const q = new URLSearchParams();
  if (opts.link) q.set('link', '1');
  if (opts.stepUp) q.set('step_up', '1');
  q.set('return', opts.returnTo ?? '/');
  return `/api/auth/oidc/start?${q}`;
}

/** The hub's step-up URL (from step-up/begin) with where to come back to. */
export function withReturn(url: string, returnTo: string): string {
  return `${url}${url.includes('?') ? '&' : '?'}return=${encodeURIComponent(returnTo)}`;
}

/** Sends the browser to a hub URL that redirects to the provider. Tests replace it. */
export const browser = {
  go(url: string): void {
    location.assign(url);
  },
};

/** The current page as a return path: path, query and nothing else. */
export function herePath(): string {
  if (typeof location === 'undefined') return '/';
  return location.pathname + location.search;
}

/** The DELETE path for one identity; the issuer is a URL, so it's escaped. */
export function identityPath(i: Pick<LinkedIdentity, 'issuer' | 'subject'>): string {
  return `/api/auth/identities/${encodeURIComponent(i.issuer)}/${encodeURIComponent(i.subject)}`;
}

/** A sentence for each ?oidc_error= code the hub sends back. */
export const OIDC_ERRORS: Record<string, string> = {
  oidc_off: 'Signing in with a provider isn’t set up on this hub.',
  expired: 'That sign-in took too long or was already used. Try again.',
  state_mismatch: 'That sign-in was started in another browser or tab. Start it again here.',
  provider_error: 'The provider didn’t complete the sign-in, or couldn’t be reached. Try again.',
  bad_token: 'The provider’s answer didn’t check out, so Cha Dash refused it. The owner can run Test in Settings → Security.',
  unlinked: 'This account isn’t linked to a Cha Dash user. Sign in another way and link it from your settings.',
  no_group: 'Your account at the provider isn’t in a group that has a role here. Ask the owner.',
  disabled: 'This Cha Dash account is disabled.',
  already_linked: 'That provider account is already linked to another Cha Dash user.',
  wrong_user: 'You signed in as someone else while that was going on. Try again.',
  stale_auth: 'The provider didn’t confirm a fresh sign-in, so that didn’t count as confirming it’s you.',
  not_linked: 'That provider account isn’t the one linked to you.',
  step_up_required: 'Confirm it’s you first, then link the account.',
  step_up_unavailable: 'Your account confirms it’s you here in Cha Dash, not at the provider.',
  cross_site: 'That was started from another site, so Cha Dash ignored it. Start it again from Cha Dash.',
  not_signed_in: 'Your session ended. Sign in again.',
  ip_banned: 'This device’s address is banned after five wrong passwords. An admin can clear it in Settings → Users.',
  rate_limited: 'Too many attempts. Try again in a minute.',
  internal: 'The hub couldn’t finish that. Try again.',
};

export function oidcErrorWords(code: string): string {
  return OIDC_ERRORS[code] ?? 'That sign-in didn’t work. Try again.';
}

export interface OidcReturn {
  /** linked, step_up or tested. */
  result?: string;
  error?: string;
}

/**
 * Reads ?oidc= and ?oidc_error= from the address bar once and removes them,
 * so a reload doesn't repeat the message.
 */
export function takeOidcReturn(): OidcReturn {
  if (typeof location === 'undefined' || typeof history === 'undefined') return {};
  const url = new URL(location.href);
  const result = url.searchParams.get('oidc') ?? undefined;
  const error = url.searchParams.get('oidc_error') ?? undefined;
  if (result === undefined && error === undefined) return {};
  url.searchParams.delete('oidc');
  url.searchParams.delete('oidc_error');
  history.replaceState(history.state, '', url.pathname + url.search + url.hash);
  const out: OidcReturn = {};
  if (result) out.result = result;
  if (error) out.error = error;
  return out;
}

/** Normalises what the hub sent, so a form can bind to every field. */
export function asSettings(v: Partial<SignInSettings> | undefined): SignInSettings {
  return {
    oidc: {
      enabled: v?.oidc?.enabled === true,
      name: v?.oidc?.name ?? '',
      issuer: v?.oidc?.issuer ?? '',
      client_id: v?.oidc?.client_id ?? '',
      scopes: Array.isArray(v?.oidc?.scopes) ? v.oidc.scopes : ['openid', 'profile', 'email', 'groups'],
      group_claim: v?.oidc?.group_claim || 'groups',
    },
    forward_auth: {
      enabled: v?.forward_auth?.enabled === true,
      user_header: v?.forward_auth?.user_header || 'Remote-User',
      groups_header: v?.forward_auth?.groups_header || 'Remote-Groups',
      email_header: v?.forward_auth?.email_header || 'Remote-Email',
    },
    mappings: Array.isArray(v?.mappings) ? v.mappings.map((m) => ({ group: m.group, role: m.role })) : [],
    provision: v?.provision === true,
    hide_password_form: v?.hide_password_form === true,
  };
}
