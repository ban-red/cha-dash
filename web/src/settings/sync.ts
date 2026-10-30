/**
 * Git sync and deploy webhooks (v0.3, ADR-0021): the hub's routes and the
 * words the Follow git form and the webhooks list use.
 *
 *   GET    /api/stacks/{node}/{stack}/sync          {sync: SyncView | null}
 *   PUT    /api/stacks/{node}/{stack}/sync          follow, or change (step-up when mode becomes deploy, the URL changes, or taking over)
 *   DELETE /api/stacks/{node}/{stack}/sync          unfollow; files and history stay
 *   POST   /api/stacks/{node}/{stack}/sync/check    {url?, branch?, auth?} → {sha, host_key}: reach the remote, fetch nothing
 *   POST   /api/stacks/{node}/{stack}/sync/now      check now → 202
 *   POST   /api/stacks/{node}/{stack}/sync/resume   resume a held or paused sync
 *   GET    /api/stacks/hooks                        hooks, never their secret
 *   POST   /api/stacks/hooks                        {url, branch, sender, deploy_on_push?} → {hook, secret, switched}: the secret, once
 *   POST   /api/stacks/hooks/{id}/rotate            → {hook, secret}: the old secret stops working at once
 *   DELETE /api/stacks/hooks/{id}
 *
 * Credentials are write-only: an omitted secret keeps the saved one. Only the
 * Stacks chunk imports this file.
 */
import { api } from '../lib/api';
import type { GlyphKind } from '../ui/marks/Glyph.vue';

export type AuthKind = 'none' | 'https' | 'ssh';
export type SyncMode = 'ask' | 'deploy';
export type SyncState = 'active' | 'held' | 'paused';
export type HookSender = 'github' | 'gitea' | 'forgejo' | 'gitlab' | 'chadash';

export interface SyncView {
  id: string;
  node: string;
  stack: string;
  url: string;
  branch: string;
  path: string;
  auth: { kind: AuthKind; username: string; secret_set: boolean };
  host_key: string | null;
  mode: SyncMode;
  poll_seconds: number;
  secrets_allow: string[];
  owner: { id: string; username: string } | null;
  state: SyncState;
  state_reason: string;
  last_seen_sha: string;
  last_deployed_sha: string;
  rejected_sha: string;
  last_error: string;
  running: boolean;
  hook: { id: string; path: string; sender: HookSender; last_used_at: string | null } | null;
  created_at: string;
  updated_at: string;
}

export interface SyncRequest {
  url: string;
  branch: string;
  path?: string;
  auth: { kind: AuthKind; username?: string; secret?: string };
  host_key?: string;
  mode: SyncMode;
  poll_seconds?: number;
  secrets_allow?: string[];
}

export interface CheckRequest {
  url?: string;
  branch?: string;
  auth?: { kind: AuthKind; username?: string; secret?: string };
}

export interface CheckResult {
  sha: string;
  host_key: string | null;
}

export interface HookView {
  id: string;
  url: string;
  branch: string;
  sender: HookSender;
  path: string;
  created_at: string;
  created_by: string | null;
  last_used_at: string | null;
  failures: number;
  syncs: { node: string; stack: string; mode: SyncMode; state: SyncState }[];
}

export interface HookCreated {
  hook: HookView;
  secret: string;
  switched?: number;
}

// ── routes ──

const seg = (node: string, stack: string) => `/api/stacks/${encodeURIComponent(node)}/${encodeURIComponent(stack)}/sync`;

export async function getSync(node: string, stack: string): Promise<SyncView | null> {
  const res = await api<{ sync?: SyncView | null }>(seg(node, stack));
  return res?.sync ?? null;
}

export async function putSync(node: string, stack: string, req: SyncRequest): Promise<SyncView> {
  const res = await api<{ sync: SyncView }>(seg(node, stack), { method: 'PUT', body: req });
  return res.sync;
}

export const deleteSync = (node: string, stack: string) => api(seg(node, stack), { method: 'DELETE' });

export async function checkSync(node: string, stack: string, req: CheckRequest): Promise<CheckResult> {
  const res = await api<CheckResult>(`${seg(node, stack)}/check`, { method: 'POST', body: req });
  return { sha: res?.sha ?? '', host_key: res?.host_key ?? null };
}

export const syncNow = (node: string, stack: string) => api(`${seg(node, stack)}/now`, { method: 'POST', body: {} });

export async function resumeSync(node: string, stack: string): Promise<SyncView> {
  const res = await api<{ sync: SyncView }>(`${seg(node, stack)}/resume`, { method: 'POST', body: {} });
  return res.sync;
}

export async function listHooks(): Promise<HookView[]> {
  const res = await api<{ hooks?: HookView[] }>('/api/stacks/hooks');
  return (res?.hooks ?? []).map((h) => ({ ...h, syncs: h.syncs ?? [] }));
}

export async function createHook(req: { url: string; branch: string; sender: HookSender; deploy_on_push?: boolean }): Promise<HookCreated> {
  return api<HookCreated>('/api/stacks/hooks', { method: 'POST', body: req });
}

export async function rotateHook(id: string): Promise<HookCreated> {
  return api<HookCreated>(`/api/stacks/hooks/${encodeURIComponent(id)}/rotate`, { method: 'POST', body: {} });
}

export const deleteHook = (id: string) => api(`/api/stacks/hooks/${encodeURIComponent(id)}`, { method: 'DELETE' });

// ── names and words ──

export const DEFAULT_POLL_MINUTES = 5;

/** The full address a forge posts to. */
export const hookURL = (path: string) => (typeof location === 'undefined' ? path : `${location.origin}${path}`);

export const short = (sha: string | null | undefined) => (sha ? sha.slice(0, 7) : '');

/** Where the read-only editor sends people. */
export const editWhere = (s: Pick<SyncView, 'path' | 'branch' | 'url'>) => `Edit ${s.path} on ${s.branch} in ${s.url}.`;

/** A draft's source, in words: "From git, commit abc1234", or "" for the editor's. */
export function sourceWords(source: string | undefined): string {
  if (!source?.startsWith('git:')) return '';
  return `From git, commit ${short(source.slice(4))}`;
}

export const STATE_WORDS: Record<SyncState, { label: string; glyph: GlyphKind }> = {
  active: { label: 'following', glyph: 'ok' },
  held: { label: 'held', glyph: 'maint' },
  paused: { label: 'paused', glyph: 'attn' },
};

export const MODE_WORDS: Record<SyncMode, string> = { ask: 'draft and ask', deploy: 'deploy on push' };

export const DEPLOY_ON_PUSH_WORDS = 'A push deploys without anyone approving it; a bad commit still rolls back.';

export const SENDERS: { id: HookSender; label: string; where: string }[] = [
  { id: 'github', label: 'GitHub', where: 'In the repository’s Settings → Webhooks, paste the URL, pick content type application/json, and paste the secret in Secret.' },
  { id: 'gitea', label: 'Gitea', where: 'In the repository’s Settings → Webhooks → Add webhook → Gitea, paste the URL and paste the secret in Secret.' },
  { id: 'forgejo', label: 'Forgejo', where: 'In the repository’s Settings → Webhooks → Add webhook → Forgejo, paste the URL and paste the secret in Secret.' },
  {
    id: 'gitlab',
    label: 'GitLab',
    where:
      'In the project’s Settings → Webhooks, paste the URL and paste the secret in Secret token. GitLab sends the token itself rather than a signature, so it proves less, and a reverse proxy that logs request headers would log it.',
  },
  {
    id: 'chadash',
    label: 'Cha Dash (CI)',
    where:
      'From CI, POST a JSON body with X-Chadash-Timestamp (Unix seconds) and X-Chadash-Signature: sha256=<hex>, the HMAC-SHA256 of timestamp + "." + body with this secret.',
  },
];

export const senderLabel = (id: HookSender) => SENDERS.find((s) => s.id === id)?.label ?? id;
