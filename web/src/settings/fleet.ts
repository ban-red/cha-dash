/**
 * Fleet stacks (v0.3, wave 14): moving a running project into a node's stacks
 * root, and Cha Dash updating itself. The hub's routes and the words the
 * Stacks pane uses for them.
 *
 *   POST /api/stacks/{node}/{stack}/move/preview  {from_path} → rewrites, plan, refusal (saves nothing)
 *   GET  /api/nodes/{id}/self-update              the last update, and whether this node may run one
 *
 * Moving is the `stack.move` action (T1, undo un-adopts), un-adopting is
 * `stack.unadopt` (T1), and updating is `chadash.update` (T3: typed "Cha Dash"
 * and a step-up) on `node:<id>`. Only the Stacks chunk imports this file.
 */
import { api } from '../lib/api';
import type { GlyphKind } from '../ui/marks/Glyph.vue';
import type { PlanAction } from './stacks';

export interface Rewrite {
  line: number;
  from: string;
  to: string;
}

export interface MovePlan {
  services: { name: string; action: PlanAction; image?: string; reason?: string; pull?: boolean }[];
  downtime_s: number;
  recreate_all?: boolean;
  root_equivalent?: string[];
}

export interface MovePreview {
  ok: boolean;
  rewrites: Rewrite[];
  plan: MovePlan | null;
  /** Where the stack would live, in the root. */
  path: string;
  refusal: { code: string; message: string } | null;
}

export type SelfUpdateOutcome = 'deployed' | 'unchanged' | 'blocked' | 'rolled_back' | 'stuck' | 'running';

export interface SelfUpdateResult {
  outcome: SelfUpdateOutcome;
  previous: string;
  current: string;
  ref?: string;
  image?: string;
  reason?: string;
  commit?: string;
  started_at?: string;
  finished_at?: string;
}

export interface SelfUpdateStatus {
  node: string;
  /** The node's policy grants stacks.selfupdate. */
  allowed: boolean;
  /** …and it runs beside the hub, so it can update it. */
  available: boolean;
  reason?: string;
  /** The image the hub runs now. */
  running?: string;
  last: SelfUpdateResult | null;
}

const seg = (node: string, name: string) => `/api/stacks/${encodeURIComponent(node)}/${encodeURIComponent(name)}`;

export async function previewMove(node: string, name: string, fromPath: string, signal?: AbortSignal): Promise<MovePreview> {
  const p = await api<MovePreview>(`${seg(node, name)}/move/preview`, { method: 'POST', body: { from_path: fromPath }, ...(signal ? { signal } : {}) });
  return { ok: !!p?.ok, rewrites: p?.rewrites ?? [], plan: p?.plan ?? null, path: p?.path ?? '', refusal: p?.refusal ?? null };
}

export async function selfUpdateStatus(node: string): Promise<SelfUpdateStatus> {
  const s = await api<SelfUpdateStatus>(`/api/nodes/${encodeURIComponent(node)}/self-update`);
  return { ...s, node, allowed: !!s?.allowed, available: !!s?.available, last: s?.last ?? null };
}

export const SELF_TARGET = (node: string) => `node:${node}`;
export const HUB_IMAGE = 'ghcr.io/ban-red/chadash';
export const TAG_RE = /^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$/;

/** A tag, or the hub's image with one; anything else is a sentence saying why not. */
export function tagProblem(input: string): string {
  const v = input.trim();
  if (!v) return 'Give the tag to update to, such as 0.3.0.';
  const tag = v.startsWith(`${HUB_IMAGE}:`) ? v.slice(HUB_IMAGE.length + 1) : v;
  if (v.includes('@')) return 'Give a tag, not a digest: the node pins the digest the tag points at.';
  if (tag.includes('/') || tag.includes(':')) return `Only ${HUB_IMAGE} can be updated here. Give one of its tags.`;
  if (!TAG_RE.test(tag)) return 'That isn’t an image tag.';
  return '';
}

/** "ghcr.io/ban-red/chadash:0.3.0@sha256:abcd…" → "0.3.0 · sha256:abcd1234". */
export function shortImage(ref: string | undefined): string {
  if (!ref) return 'unknown';
  const [name = '', digest] = ref.split('@');
  const tag = name.startsWith(`${HUB_IMAGE}:`) ? name.slice(HUB_IMAGE.length + 1) : name === HUB_IMAGE ? '' : name;
  const d = digest ? digest.replace(/^(sha256:[0-9a-f]{8})[0-9a-f]*$/, '$1') : '';
  return [tag, d].filter(Boolean).join(' · ') || ref;
}

export const OUTCOME_WORDS: Record<SelfUpdateOutcome, { words: string; glyph: GlyphKind }> = {
  deployed: { words: 'Updated and healthy', glyph: 'ok' },
  unchanged: { words: 'Already on that version', glyph: 'ok' },
  blocked: { words: 'Nothing changed', glyph: 'attn' },
  rolled_back: { words: 'Rolled back on its own', glyph: 'attn' },
  stuck: { words: 'Stuck: the rollback failed too', glyph: 'down' },
  running: { words: 'Updating now', glyph: 'prog' },
};
