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
import type { Tier } from '../model/types';
import type { GlyphKind } from '../ui/marks/Glyph.vue';
import type { SyncView } from './sync';

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
  /** Other managers listing a stack of this name: deploy from one only. */
  also_managed_by?: OtherManager[];
}

/**
 * Another manager of the stack: one whose spec lists a stack of the same
 * name (Dockhand, Komodo, Arcane: resource), or the tool that last brought
 * its containers up from another file on its host (Dockge, or "Another
 * tool": file).
 */
export interface OtherManager {
  manager: string;
  resource?: string;
  file?: string;
}

/** "Dockhand also manages media" or "Dockhand and Komodo also manage media", and the file another tool used. */
export function twoWriters(name: string, others: OtherManager[] | undefined): string {
  const names = [...new Set((others ?? []).map((o) => o.manager))];
  if (!names.length) return '';
  const who = names.length === 1 ? names[0] : `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`;
  const file = others?.find((o) => o.file)?.file;
  const from = file ? ` Its containers were last brought up from ${file}.` : '';
  return `${who} also manage${names.length === 1 ? 's' : ''} a stack named ${name}: deploy it from one of them only.${from}`;
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
  /** stack.move can bring it into the root (fleet.ts); move_refusal says why not. */
  moveable?: boolean;
  move_refusal?: string | null;
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
export type HistoryKind = 'deploy' | 'rollback' | 'adopt' | 'unadopt' | 'accept' | 'self-update' | 'other';

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
  /** web, or git:<sha> from a sync (sync.ts). */
  source?: string;
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
  /** Set while the stack follows git: the editor is read-only (sync.ts). */
  sync?: SyncView | null;
  /** Where stack.move copied the file from, while that copy is still there. */
  moved_from?: string | null;
  /** Other managers listing a stack of this name: deploy from one only. */
  also_managed_by?: OtherManager[];
  /** The project's named volumes now: what down keeps and remove may delete. */
  volumes?: string[];
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

/** A whole-stack lifecycle action the stack's page offers (plan 16, wave 32a). */
export interface LifecycleOffer {
  action: 'stack.update' | 'stack.stop' | 'stack.start' | 'stack.restart' | 'stack.down' | 'stack.remove';
  /** The button's word. */
  text: string;
  tier: Tier;
  /** The plan's label, and the button's accessible name. */
  label: string;
}

/**
 * The lifecycle buttons for a stack's state: Update (pull its tags and
 * recreate what moved, the file unchanged) while it has containers, Stop while anything runs,
 * Start when it's stopped or down (down deploys its last commit), Restart
 * while it has containers, Take down while it has any, and Remove always.
 * None for a stack Cha Dash doesn't manage, or its own.
 */
export function lifecycleOffers(d: Pick<StackDetail, 'name' | 'managed' | 'protected' | 'status'>): LifecycleOffer[] {
  if (!d.managed || d.protected) return [];
  const n = d.name;
  const out: LifecycleOffer[] = [];
  const has = d.status !== 'down';
  if (has) out.push({ action: 'stack.update', text: 'Update', tier: 'T2', label: `Update ${n} (pull its images, recreate what moved)` });
  if (d.status === 'running' || d.status === 'partial') out.push({ action: 'stack.stop', text: 'Stop', tier: 'T1', label: `Stop ${n}` });
  if (d.status !== 'running') {
    out.push({ action: 'stack.start', text: 'Start', tier: 'T1', label: d.status === 'down' ? `Start ${n} (deploys its last commit)` : `Start ${n}` });
  }
  if (has) out.push({ action: 'stack.restart', text: 'Restart', tier: 'T1', label: `Restart ${n}` });
  if (has) out.push({ action: 'stack.down', text: 'Take down', tier: 'T2', label: `Take ${n} down (its volumes stay)` });
  out.push({ action: 'stack.remove', text: 'Remove', tier: 'T3', label: `Remove ${n}` });
  return out;
}

/** stack.remove's params: the volumes go only with the name typed again. */
export function removeParams(name: string, volumes: boolean, typed: string): { volumes?: true; confirm_volumes?: string } | null {
  if (!volumes) return {};
  return typed.trim() === name ? { volumes: true, confirm_volumes: typed.trim() } : null;
}

export const PROTECTED_WORDS = 'Cha Dash’s own stack · view only';

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
  'self-update': { label: 'self-update', glyph: 'check' },
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
