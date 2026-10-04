/**
 * The hub's action engine for tests, to the shapes in 08 · v0.2 → API shapes:
 * `POST /api/actions/plan` answers a plan whose tier and label come from the
 * Demo Home's incidents (confirm by tier: T0 none, T1 undo, T2 hold, T3/T4
 * type), and `POST /api/plans/{id}/approve` answers 202 with a running task.
 * Override either per test; `plans` and `approvals` record what was asked.
 */
import type { Plan } from '../src/lib/actions';
import type { Action, Task, Tier } from '../src/model/types';
import type { Handler } from './fake-fetch';
import { withIncidents } from './fixtures';

const CONFIRM: Record<Tier, Plan['confirm']> = { T0: 'none', T1: 'undo', T2: 'hold', T3: 'type', T4: 'type' };

/** Every action the Demo Home's incidents offer, by `id target`. */
const KNOWN = new Map<string, Action>();
for (const i of withIncidents.incidents) {
  for (const a of [i.suggested, ...(i.alternatives ?? [])]) if (a) KNOWN.set(`${a.id} ${a.target}`, a);
}

export function planFor(action: string, target: string, over: Partial<Plan> = {}): Plan {
  const a = KNOWN.get(`${action} ${target}`);
  const tier = over.tier ?? a?.tier ?? 'T1';
  const p: Plan = {
    id: `p_${action}_${target}`.replace(/[^\w]/g, '_'),
    hash: `sha256:${action}:${target}`,
    action,
    label: a?.label ?? action,
    target,
    params: {},
    tier,
    confirm: CONFIRM[tier],
    step_up: tier === 'T4' ? 'fresh' : 'none',
    steps: [{ label: `Ask the hub to ${a?.label.toLowerCase() ?? action}` }, { label: 'Check it worked' }],
    impact: a?.impact ?? [],
    undo: null,
    expires_at: '2026-10-03T14:07:10Z',
    ...over,
  };
  if (p.confirm === 'type' && p.confirm_text === undefined) p.confirm_text = target.split('/').pop() ?? target;
  return p;
}

export function taskFor(p: Plan, over: Partial<Task> = {}): Task {
  return {
    id: `t_${p.id}`,
    plan: p.id,
    action: p.action,
    label: p.label,
    target: p.target,
    actor: { kind: 'user', name: 'owner' },
    state: 'running',
    step: p.steps?.[0]?.label ?? null,
    progress: 0.1,
    started_at: '2026-10-03T14:05:11Z',
    finished_at: null,
    error: null,
    undo: p.undo ?? null,
    audit: `a_${p.id}`,
    ...over,
  };
}

export interface EngineOptions {
  /** Change the plan for an action (by id), or refuse it: return {status, body}. */
  plan?: (action: string, target: string) => Partial<Plan> | { status: number; body?: unknown } | undefined;
  /** Answer an approve differently: return {status, body} to refuse, or a Task override. */
  approve?: (planId: string, body: { hash: string; typed?: string }, n: number) => { status: number; body?: unknown } | Partial<Task> | undefined;
}

const isResponse = (x: unknown): x is { status: number; body?: unknown } => typeof (x as { status?: unknown })?.status === 'number' && !('state' in (x as object));

export function engine(opts: EngineOptions = {}) {
  const plans: { action: string; target: string; params?: unknown }[] = [];
  const approvals: { plan: string; hash: string; typed?: string }[] = [];
  const issued = new Map<string, Plan>();
  const routes: Record<string, Handler> = {
    'POST /api/actions/plan': (body) => {
      const b = body as { action: string; target: string; params?: unknown };
      plans.push(b);
      const over = opts.plan?.(b.action, b.target);
      if (isResponse(over)) return over;
      const p = planFor(b.action, b.target, over ?? {});
      issued.set(p.id, p);
      return { status: 200, body: p };
    },
    'POST /api/plans/*': (body, url) => {
      const id = decodeURIComponent(url.pathname.split('/')[3] ?? '');
      const b = body as { hash: string; typed?: string };
      approvals.push({ plan: id, ...b });
      const over = opts.approve?.(id, b, approvals.length);
      if (isResponse(over)) return over;
      const p = issued.get(id) ?? planFor('unknown', 'unknown', { id });
      return { status: 202, body: { task: taskFor(p, over ?? {}) } };
    },
  };
  return { routes, plans, approvals };
}

/** The hub's error envelope. */
export const refuse = (status: number, code: string, message: string, extra: Record<string, unknown> = {}) => ({ status, body: { error: { code, message, ...extra } } });
