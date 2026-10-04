/**
 * The action engine's HTTP side (08 · v0.2 → API shapes):
 *
 *   POST /api/actions/plan        {action, target, params?} → 200 Plan
 *   POST /api/plans/{id}/approve  {hash, typed?}            → 202 {task}
 *   GET  /api/tasks/{id}                                    → Task
 *   POST /api/auth/step-up/begin                            → WebAuthn request options, or {method: "password"}
 *   POST /api/auth/step-up        assertion | {password}    → {until}
 *
 * Refusals use the hub's envelope, `{"error": {"code", "message"}}`, and the
 * message is a sentence the page shows as is. `actionError()` turns any
 * failure into one of the codes below plus that sentence.
 */
import { ApiError, api } from './api';
import type { Impact, Task, Tier, UndoSpec } from '../model/types';

export type Confirm = 'none' | 'undo' | 'hold' | 'type';
export type StepUp = 'none' | 'required' | 'fresh';

export interface Plan {
  id: string;
  /** Approve must echo it, so an approval only ever runs the plan that was shown. */
  hash: string;
  action: string;
  label: string;
  target: string;
  params?: Record<string, unknown>;
  tier: Tier;
  confirm: Confirm;
  /** Only for `type`: what must be typed. */
  confirm_text?: string;
  step_up: StepUp;
  steps?: { label: string }[];
  impact?: Impact[];
  undo?: UndoSpec | null;
  expires_at?: string;
}

export type ActionErrorCode =
  | 'forbidden'
  | 'not_found'
  | 'precondition'
  | 'unavailable'
  | 'invalid'
  | 'step_up_required'
  | 'plan_expired'
  | 'plan_changed'
  | 'confirm_mismatch'
  | 'locked'
  | 'network'
  | 'rate_limited'
  | 'failed';

export interface ActionFailure {
  code: ActionErrorCode;
  /** A sentence to show as is. */
  message: string;
  status: number;
  /** `unavailable`: the instance whose recipe grants the action. */
  recipe?: string;
  /** `plan_changed`: the new plan, when the hub sends one. */
  plan?: Plan;
}

const FALLBACK: Record<ActionErrorCode, string> = {
  forbidden: 'Your role can’t run this. Ask an admin or the owner.',
  not_found: 'Cha Dash doesn’t know that action or what it’s for any more.',
  precondition: 'That doesn’t apply right now.',
  unavailable: 'Cha Dash can’t run this here yet.',
  invalid: 'The hub didn’t accept those settings for this action.',
  step_up_required: 'Confirm it’s you first.',
  plan_expired: 'That plan expired. Plan it again.',
  plan_changed: 'Something changed since this was planned. Here’s the new plan.',
  confirm_mismatch: 'That doesn’t match. Type it exactly as shown.',
  locked: 'Another task is working on this right now. Try again when it’s done.',
  network: 'Can’t reach the hub.',
  rate_limited: 'Too many actions at once. Try again shortly.',
  failed: 'The hub couldn’t do that. Try again.',
};

const KNOWN = new Set<string>(Object.keys(FALLBACK));

/** Any failure from the engine's routes, as a code and a sentence. */
export function actionError(err: unknown): ActionFailure {
  if (!(err instanceof ApiError)) return { code: 'failed', message: FALLBACK.failed, status: 0 };
  if (err.status === 0) return { code: 'network', message: FALLBACK.network, status: 0 };
  if (err.status === 429) {
    return { code: 'rate_limited', message: err.retryAfter ? `Too many actions at once. Try again in ${err.retryAfter} s.` : FALLBACK.rate_limited, status: 429 };
  }
  let code: ActionErrorCode = KNOWN.has(err.code) ? (err.code as ActionErrorCode) : 'failed';
  if (code === 'failed') {
    if (err.status === 403) code = 'forbidden';
    else if (err.status === 404) code = 'not_found';
    else if (err.status === 410) code = 'plan_expired';
    else if (err.status === 422) code = 'invalid';
  }
  // The hub's message is a sentence; the fallback is only for a hub that sent none.
  const body = err.body as { recipe?: unknown; plan?: unknown; error?: { message?: unknown; recipe?: unknown; plan?: unknown } } | undefined;
  const said = body?.error?.message;
  const fromHub = typeof said === 'string' && said.trim() ? said.trim() : '';
  const out: ActionFailure = { code, message: fromHub || FALLBACK[code], status: err.status };
  const recipe = body?.error?.recipe ?? body?.recipe;
  if (typeof recipe === 'string' && recipe) out.recipe = recipe;
  const plan = body?.error?.plan ?? body?.plan;
  if (isPlan(plan)) out.plan = plan;
  return out;
}

export function isPlan(p: unknown): p is Plan {
  const x = p as Partial<Plan> | null | undefined;
  return !!x && typeof x.id === 'string' && typeof x.hash === 'string' && typeof x.confirm === 'string';
}

const garbled = () => new ApiError(502, 'failed', 'The hub answered in a way Cha Dash doesn’t understand.', undefined, { error: { message: 'The hub answered in a way Cha Dash doesn’t understand.' } });

export async function plan(action: string, target: string, params?: Record<string, unknown>): Promise<Plan> {
  const body: Record<string, unknown> = { action, target };
  if (params && Object.keys(params).length) body['params'] = params;
  const p = await api<Plan>('/api/actions/plan', { method: 'POST', body });
  if (!isPlan(p)) throw garbled();
  return p;
}

export async function approve(p: Plan, typed?: string): Promise<Task> {
  const body: Record<string, unknown> = { hash: p.hash };
  if (typed !== undefined) body['typed'] = typed;
  const res = await api<{ task?: Task }>(`/api/plans/${encodeURIComponent(p.id)}/approve`, { method: 'POST', body });
  if (!res?.task || typeof res.task.id !== 'string') throw garbled();
  return res.task;
}

export async function getTask(id: string): Promise<Task> {
  return api<Task>(`/api/tasks/${encodeURIComponent(id)}`);
}

export const isDone = (t: Pick<Task, 'state'>) => t.state === 'succeeded' || t.state === 'failed' || t.state === 'cancelled';

/**
 * A sentence for a refusal from the v0.2 routes (users, audit, settings): the
 * hub's own message when it sent one, else words for the status.
 */
export function hubWords(err: unknown, fallback = 'The hub couldn’t do that. Try again.'): string {
  if (!(err instanceof ApiError)) return fallback;
  if (err.status === 0) return 'Can’t reach the Cha Dash hub.';
  const said = (err.body as { error?: { message?: unknown } } | undefined)?.error?.message;
  if (typeof said === 'string' && said.trim()) return said.trim();
  if (err.status === 401) return 'Your session ended. Sign in again.';
  if (err.status === 403) return 'Your role can’t do that.';
  if (err.status === 404) return 'This hub doesn’t have that yet.';
  if (err.status === 429) return 'Too many requests. Try again shortly.';
  return fallback;
}

/** A step-up refusal from any route (an action's approve, a settings save, a user change). */
export const needsStepUp = (err: unknown) => err instanceof ApiError && err.code === 'step_up_required';
