/**
 * Settings → Nodes: the hub's nodes and join tokens, as `GET /api/nodes`
 * and `/api/nodes/tokens` answer (internal/nodes).
 *
 *   GET    /api/nodes                → {nodes, hub_pin, pairing}
 *   POST   /api/nodes/{id}/approve   → the node, approved; step-up; 409 not pending
 *   POST   /api/nodes/{id}/revoke    → the node, revoked (on a pending node: Reject)
 *   GET    /api/nodes/tokens         → {tokens}: never a secret
 *   POST   /api/nodes/tokens         {name?, ttl_minutes?, add_docker?} → CreatedToken, once
 *   DELETE /api/nodes/tokens/{id}    → the token, revoked; 409 used, expired or revoked
 */

export interface Verb {
  name: string;
  policy: string;
  allowed: boolean;
}

export type TokenState = 'active' | 'used' | 'expired' | 'revoked';

/** A join token as the hub lists it: never its secret. */
export interface JoinToken {
  id: string;
  /** '' lets the node pick its name. */
  name: string;
  add_docker: boolean;
  created_by?: string;
  created_by_name?: string;
  created_at: string;
  expires_at: string;
  used_at?: string;
  revoked_at?: string;
  node_id?: string;
  node_name?: string;
  state: TokenState;
}

export interface HubNode {
  id: string;
  name: string;
  fingerprint: string;
  state: 'pending' | 'approved' | 'revoked' | string;
  enrolled_via?: string;
  approved_by?: string;
  approved_at?: string;
  revoked_by?: string;
  revoked_at?: string;
  last_seen_at?: string;
  address?: string;
  version?: string;
  verbs?: Verb[];
  connected?: boolean;
  hub_key_current?: boolean;
  created_at?: string;
  /** The token a node joined with: who made it, and whether approving adds Docker. */
  join_token?: JoinToken;
}

/** `POST /api/nodes/tokens`: the only time the token itself is shown. */
export interface CreatedToken {
  token: JoinToken;
  /** cdj_<secret>.<hub pin> */
  secret: string;
  /** The hub's suggestion for CHADASH_HUB; the admin can edit it. */
  hub_url: string;
  env: string;
  command: string;
}

/** What each policy grant lets Cha Dash do, in words (internal/node/policy.go). */
export const GRANTS: Record<string, string> = {
  'docker.read': 'Read containers and the engine',
  'docker.logs': 'Read container logs',
  'docker.lifecycle': 'Start, stop and restart containers',
  'docker.update': 'Pull images and recreate containers',
  'docker.remove': 'Remove containers',
  'docker.exec': 'Open a shell in containers',
  'stacks.read': 'Read stacks and their history',
  'stacks.deploy': 'Deploy and roll back stacks',
  'stacks.adopt': 'Adopt running compose projects',
  'stacks.sync': 'Follow stacks from git',
  'stacks.selfupdate': 'Update Cha Dash itself',
  'host.metrics': 'Read host metrics',
  'host.smart': 'Read disk health (SMART)',
};

export interface Grant {
  policy: string;
  words: string;
  allowed: boolean;
}

/** A node's verbs as grants: each policy once, allowed when the node allows its verbs. */
export function grantsOf(n: Pick<HubNode, 'verbs'>): Grant[] {
  const by = new Map<string, boolean>();
  for (const v of n.verbs ?? []) by.set(v.policy, (by.get(v.policy) ?? false) || v.allowed);
  return [...by.entries()].map(([policy, allowed]) => ({ policy, words: GRANTS[policy] ?? policy, allowed })).sort((a, b) => a.policy.localeCompare(b.policy));
}

/** How long a new token lives, in minutes: 15 minutes by default, a day at most. */
export const TTL_CHOICES: readonly { minutes: number; words: string }[] = [
  { minutes: 15, words: '15 minutes' },
  { minutes: 60, words: '1 hour' },
  { minutes: 240, words: '4 hours' },
  { minutes: 1440, words: '24 hours' },
];

/** The placeholder the hub puts in CHADASH_NODE_NAME when the token names no node. */
export const NAME_PLACEHOLDER = '<this host>';

/** The .env block compose.node.yaml reads, with the hub address the admin settled on. */
export function envBlock(hubUrl: string, name: string, token: string): string {
  return `CHADASH_HUB=${hubUrl}\nCHADASH_NODE_NAME=${name || NAME_PLACEHOLDER}\nCHADASH_NODE_TOKEN=${token}\n`;
}

/** A hub address a node can dial: ws:// or wss://, a host, any path. */
export function validHubUrl(s: string): boolean {
  try {
    const u = new URL(s.trim());
    return (u.protocol === 'ws:' || u.protocol === 'wss:') && u.host !== '';
  } catch {
    return false;
  }
}

/** A node name the hub accepts (internal/nodes nameRE, minus the reserved ones). */
export function validNodeName(s: string): boolean {
  return /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$/.test(s) && !['hooks', 'secrets'].includes(s.toLowerCase());
}
