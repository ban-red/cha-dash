/**
 * What each role may see and do, from 08 · v0.2 → Roles. The page only uses
 * this to hide what a role can't use; the hub enforces every rule itself
 * (internal/authz), so a stale or wrong answer here can only hide too much or
 * show a button the hub then refuses with a sentence.
 *
 * | Role      | Actions                     | Settings                                   |
 * |-----------|-----------------------------|--------------------------------------------|
 * | owner     | every tier (T4 where on)    | everything                                 |
 * | admin     | T0–T3                       | all but security; users other than owner   |
 * | operator  | T0–T2                       | their own passkeys, devices and API tokens |
 * | viewer    | T0                          | their own passkeys, devices and API tokens |
 * | household | T0 + `household: true`      | own passkeys, devices, API tokens; boards  |
 * |           |                             | only, no Inbox detail                      |
 *
 * Every role may manage its own passkeys (the hub's passkey routes are open
 * to any signed-in role, and "Passkey only" needs one for every role), so
 * Settings opens for everyone, on Security below admin, plus Notifications
 * for "Notify me on this device" (your own push subscriptions). Everything
 * else in Settings (integrations, import, backup, users, audit, notification
 * channels and rules, the first-run wizard) answers 403 below admin, and is
 * never shown there.
 *
 * Every role also manages its own API tokens (a token never exceeds its
 * user's role), so API tokens is open below admin too. The owner sees and
 * revokes everyone's tokens, admins everyone's but the owner's; both approve the plans tokens leave
 * waiting (`approve`).
 */
import type { SettingsPane } from '../lib/router';
import type { Tier } from './types';

export type Role = 'owner' | 'admin' | 'operator' | 'viewer' | 'household';

export const ROLES: readonly Role[] = ['owner', 'admin', 'operator', 'viewer', 'household'];

export const ROLE_WORDS: Record<Role, { label: string; note: string }> = {
  owner: { label: 'Owner', note: 'everything, including users and security' },
  admin: { label: 'Admin', note: 'actions up to T3, settings, other users' },
  operator: { label: 'Operator', note: 'actions up to T2, no settings' },
  viewer: { label: 'Viewer', note: 'reads everything, changes nothing' },
  household: { label: 'Household', note: 'boards only, a few safe actions' },
};

/**
 * A role from the session. An unknown role is treated as a viewer, so the
 * page never offers more than it should. No role at all means no session to
 * ask (a preview, a component on its own), where nothing is hidden.
 */
export function asRole(r: string | undefined): Role {
  return (ROLES as readonly string[]).includes(r ?? '') ? (r as Role) : r === undefined ? 'owner' : 'viewer';
}

const MAX_TIER: Record<Role, number> = { owner: 4, admin: 3, operator: 2, viewer: 0, household: 0 };

const tierN = (t: Tier | string) => Number(String(t).replace(/^T/, '')) || 0;

export interface Permissions {
  role: Role;
  /** The Inbox (and Needs attention): everyone but household. */
  inbox: boolean;
  /** Settings at all (below admin, only Security: your passkeys). */
  settings: boolean;
  /** Integrations, the first-run wizard and anything that opens an integration's settings (a recipe, Reconnect). */
  integrations: boolean;
  /** Edit layout, and add or dismiss label-discovered services: boards are an admin setting. */
  editBoards: boolean;
  /** Answer the correlator's suggestions (Merge, Keep separate). */
  decide: boolean;
  /** Settings → Security's hub-wide settings (security level, step-up). Owner only. */
  securitySettings: boolean;
  /** Settings → Users. */
  users: boolean;
  /** Settings → Audit. */
  audit: boolean;
  /** Approve or deny plans an API token left waiting for a person; see and revoke everyone's tokens. */
  approve: boolean;
  /** Run actions that read data back, such as a container's logs (`Def.ReadsData`): Operator and up. */
  readData: boolean;
  /** Can this role run an action of this tier (household: only actions marked for it)? */
  act(tier: Tier | string, household?: boolean): boolean;
  /** Which settings panes to list. */
  panes: readonly SettingsPane[];
}

export function permissions(role: string | undefined): Permissions {
  const r = asRole(role);
  const admin = r === 'owner' || r === 'admin';
  const panes: SettingsPane[] = admin ? ['integrations', 'stacks', 'nodes', 'import', 'backup', 'users', 'audit', 'notifications', 'tokens', 'security'] : ['security', 'notifications', 'tokens'];
  return {
    role: r,
    inbox: r !== 'household',
    settings: true,
    integrations: admin,
    editBoards: admin,
    decide: admin,
    securitySettings: r === 'owner',
    users: admin,
    audit: admin,
    approve: admin,
    readData: admin || r === 'operator',
    act: (tier, household) => {
      const n = tierN(tier);
      if (r === 'household') return n === 0 || household === true;
      return n <= MAX_TIER[r];
    },
    panes,
  };
}

/** Roles this viewer may give someone. Nobody becomes owner except by a transfer. */
export function assignableRoles(viewer: string | undefined): Role[] {
  const r = asRole(viewer);
  if (r === 'owner' || r === 'admin') return ['admin', 'operator', 'viewer', 'household'];
  return [];
}
