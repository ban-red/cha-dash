/**
 * Owned stacks (wave 9): the hub's routes and the words the Stacks pane uses.
 *
 *   GET    /api/stacks                         nodes, their stacks and detected projects (viewer+)
 *   GET    /api/stacks/{node}/{stack}          one stack: file, draft, services, history, secrets (admin+)
 *   POST   /api/stacks/{node}/{stack}/validate {content} → messages, secrets, plan, diff (saves nothing)
 *   PUT    /api/stacks/{node}/{stack}          {content, revision} → {draft}; 409 conflict / protected
 *   DELETE /api/stacks/{node}/{stack}/draft    discard the draft
 *   GET    /api/stacks/secrets                 names and whether each is set; values never leave the hub
 *   PUT    /api/stacks/secrets/{name}          {value}
 *   DELETE /api/stacks/secrets/{name}
 *
 * Deploy, roll back and adopt are actions on the engine (`stack.deploy`,
 * `stack.rollback` {commit}, `stack.adopt`), planned and confirmed through the
 * safety ladder like any other. Only the Stacks chunk imports this file.
 */
import { ApiError, api } from '../lib/api';
import type { GlyphKind } from '../components/marks/Glyph.vue';

export type StackStatus = 'running' | 'partial' | 'stopped' | 'down' | 'unknown';

export interface Commit {
  commit: string;
  short: string;
  subject: string;
  at: string;
}

export interface StackSummary {
  name: string;
  status: StackStatus;
  services: number;
  running: number;
  location: 'root' | 'external';
  path: string;
  protected: boolean;
  drift: boolean;
  draft: boolean;
  head: Commit | null;
}

export interface Detected {
  name: string;
  status: StackStatus;
  services: number;
  running: number;
  path: string;
  config_files?: string[];
  adoptable: boolean;
  /** Cha Dash's own project: listed, never adopted or drafted. */
  protected?: boolean;
  reason: string | null;
}

export interface StackNode {
  node: string;
  name: string;
  online: boolean;
  error: { code: string; message: string } | null;
  root: string;
  stacks: StackSummary[];
  detected: Detected[];
}

export type Health = 'healthy' | 'unhealthy' | 'starting' | 'none';
export type HistoryKind = 'deploy' | 'rollback' | 'adopt' | 'unadopt' | 'accept' | 'other';

export interface StackService {
  name: string;
  image: string;
  state: string;
  health: Health;
  restarts: number;
}

export interface HistoryEntry extends Commit {
  author: string;
  kind: HistoryKind;
}

export interface Draft {
  content: string;
  revision: number;
  updated_at: string;
  updated_by: string;
}

export interface SecretRef {
  name: string;
  set: boolean;
}

export interface StackDetail {
  node: string;
  name: string;
  path: string;
  file: string;
  location: 'root' | 'external';
  managed: boolean;
  protected: boolean;
  protected_reason: string | null;
  drift: boolean;
  status: StackStatus;
  content: string;
  env: { present: boolean; keys: string[] };
  meta: Record<string, unknown>;
  services: StackService[];
  history: HistoryEntry[];
  draft: Draft | null;
  secrets: SecretRef[];
}

export type Severity = 'error' | 'warning' | 'info';
export type PlanAction = 'create' | 'recreate' | 'remove' | 'unchanged';

export interface Validation {
  ok: boolean;
  messages: { severity: Severity; message: string; line?: number; service?: string }[];
  secrets: SecretRef[];
  plan: { services: { name: string; action: PlanAction; image?: string; reason?: string; pull?: boolean }[]; downtime_s: number } | null;
  diff: { op: ' ' | '-' | '+' | '…'; text: string }[];
}

export interface StackSecret {
  name: string;
  set: boolean;
  updated_at?: string;
}

// ── routes ──

const seg = (node: string, name: string) => `/api/stacks/${encodeURIComponent(node)}/${encodeURIComponent(name)}`;

export async function listStacks(): Promise<StackNode[]> {
  const res = await api<{ nodes?: StackNode[] }>('/api/stacks');
  return (res?.nodes ?? []).map((n) => ({ ...n, stacks: n.stacks ?? [], detected: n.detected ?? [] }));
}

export async function getStack(node: string, name: string): Promise<StackDetail> {
  const d = await api<StackDetail>(seg(node, name));
  return { ...d, services: d.services ?? [], history: d.history ?? [], secrets: d.secrets ?? [], env: d.env ?? { present: false, keys: [] } };
}

export async function validateStack(node: string, name: string, content: string, signal?: AbortSignal): Promise<Validation> {
  const v = await api<Validation>(`${seg(node, name)}/validate`, { method: 'POST', body: { content }, ...(signal ? { signal } : {}) });
  return { ok: !!v?.ok, messages: v?.messages ?? [], secrets: v?.secrets ?? [], plan: v?.plan ?? null, diff: v?.diff ?? [] };
}

export async function saveDraft(node: string, name: string, content: string, revision: number): Promise<Draft> {
  const res = await api<{ draft: Draft }>(seg(node, name), { method: 'PUT', body: { content, revision } });
  return res.draft;
}

export const discardDraft = (node: string, name: string) => api(`${seg(node, name)}/draft`, { method: 'DELETE' });

export async function listSecrets(): Promise<StackSecret[]> {
  const res = await api<{ secrets?: StackSecret[] }>('/api/stacks/secrets');
  return res?.secrets ?? [];
}

export const putSecret = (name: string, value: string) => api(`/api/stacks/secrets/${encodeURIComponent(name)}`, { method: 'PUT', body: { value } });
export const deleteSecret = (name: string) => api(`/api/stacks/secrets/${encodeURIComponent(name)}`, { method: 'DELETE' });

/** The current revision a `409 conflict` carries, at the top level or in `error`. */
export function conflictRevision(err: unknown): number | undefined {
  if (!(err instanceof ApiError) || err.code !== 'conflict') return undefined;
  const b = err.body as { revision?: unknown; error?: { revision?: unknown } } | undefined;
  const r = b?.revision ?? b?.error?.revision;
  return typeof r === 'number' ? r : undefined;
}

// ── names and words ──

export const STACK_NAME_RE = /^[a-z0-9][a-z0-9_-]*$/;
export const SECRET_NAME_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

export const targetOf = (node: string, name: string) => `stack:${node}/${name}`;

export const PROTECTED_WORDS = 'Cha Dash’s own stack · view only until self-update';

export const STATUS_GLYPH: Record<StackStatus, GlyphKind> = { running: 'ok', partial: 'attn', stopped: 'off', down: 'down', unknown: 'repair' };

export function statusWords(s: { status: StackStatus; services: number; running: number }): string {
  switch (s.status) {
    case 'running':
      return `running · ${s.running}/${s.services}`;
    case 'partial':
      return `partly running · ${s.running}/${s.services}`;
    case 'stopped':
      return 'stopped';
    case 'down':
      return 'down · no containers';
    default:
      return 'unknown';
  }
}

/** A service's state in words. No healthcheck is never "healthy". */
export function serviceWords(s: StackService): { words: string; glyph: GlyphKind } {
  const running = s.state === 'running';
  if (!running) return { words: s.state || 'unknown', glyph: s.state === 'exited' || s.state === 'dead' ? 'down' : 'off' };
  if (s.health === 'healthy') return { words: 'running · healthy', glyph: 'ok' };
  if (s.health === 'unhealthy') return { words: 'running · unhealthy', glyph: 'down' };
  if (s.health === 'starting') return { words: 'running · health check starting', glyph: 'prog' };
  return { words: 'running · no healthcheck', glyph: 'off' };
}

export const KIND_WORDS: Record<HistoryKind, { label: string; glyph: GlyphKind }> = {
  deploy: { label: 'deploy', glyph: 'check' },
  rollback: { label: 'rollback', glyph: 'attn' },
  adopt: { label: 'adopted', glyph: 'link' },
  unadopt: { label: 'released', glyph: 'cross' },
  accept: { label: 'accepted edits', glyph: 'ok' },
  other: { label: 'change', glyph: 'off' },
};

export const PLAN_WORDS: Record<PlanAction, string> = { create: 'create', recreate: 'recreate', remove: 'remove', unchanged: 'unchanged' };

export const SEVERITY_GLYPH: Record<Severity, GlyphKind> = { error: 'down', warning: 'attn', info: 'ok' };

/** A task's failure in words: a headline for the codes stacks use, then the hub's sentence as is. */
export function outcomeHeadline(code: string | undefined): string {
  switch (code) {
    case 'rolled_back':
      return 'The new version failed its health check and Cha Dash rolled back on its own.';
    case 'stuck':
      return 'The deploy failed, and so did the rollback. The stack needs you.';
    case 'blocked':
      return 'Nothing was changed: the dry run failed.';
    case 'drift':
      return 'The file on disk changed outside Cha Dash.';
    case 'conflict':
      return 'Something changed since this was planned.';
    default:
      return 'That didn’t work.';
  }
}

export const STARTER = `services:
  app:
    image: nginx:1.28
    restart: unless-stopped
    ports:
      - "8080:80"
`;
