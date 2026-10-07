/**
 * The safety ladder on the action engine (08 · v0.2; 02 · Experience → The
 * action safety ladder). Every action is planned first, and the plan's
 * `confirm` picks the rung:
 *
 * - `none` (T0) runs: the plan is approved straight away.
 * - `undo` (T1) runs too, then offers the plan's `undo` in a toast for 10 s.
 *   Undo plans and approves that action like any other.
 * - `hold` (T2) opens the hold-to-confirm ring with the plan's impact as the
 *   blast radius and its steps.
 * - `type` (T3, T4) opens the typed confirmation: type `confirm_text`.
 *
 * When the plan says `step_up` is `required` (no grant on this session) or
 * `fresh` (T4: always), the step-up dialog runs after confirming and before
 * approving. Refusals come back as the hub's sentence, shown as is:
 * `plan_expired` re-plans once without asking, `plan_changed` shows the new
 * plan, `confirm_mismatch` stays in the typed dialog, `step_up_required`
 * steps up and retries once, `unavailable` links to the instance's settings.
 *
 * Tasks: approving returns a Task; the `tasks` realtime topic carries its
 * progress (or, on a hub without the topic, `GET /api/tasks/{id}` is polled).
 * Tasks started here end in a toast: done, or the hub's error sentence.
 *
 * Owned by the UI store (stores/ui.ts), which hands it the toasts and the
 * dialog state; components reach it through `useUi()`.
 */
import { computed, reactive, watch } from 'vue';
import {
  actionError,
  approve as approvePlan,
  cancelTask as cancelRequest,
  getTask,
  hubWords,
  isDone,
  needsStepUp,
  plan as planAction,
  type ActionFailure,
  type Plan,
} from '../lib/actions';
import type { Action, Impact, Incident, Task, Tier } from '../model/types';
import type { Live } from './live';

export interface ActionRequest {
  action: string;
  target: string;
  params?: Record<string, unknown>;
  label: string;
  tier?: Tier;
  detail?: string;
  /** The incident this works on: its progress shows there. */
  incident?: Incident;
  /** Fallback blast radius when no plan carries one. */
  impact?: Impact[];
  /** Running a plan's undo: no second undo toast. */
  isUndo?: boolean;
}

export type ConfirmKind = 'hold' | 'type' | 'review';

export interface ConfirmRequest {
  kind: ConfirmKind;
  req: ActionRequest;
  plan: Plan;
  /** The action as the dialog shows it: label, tier, detail, impact. */
  action: Action;
  incident?: Incident;
  /** Said above the plan ("Something changed since this was planned…"). */
  note?: string;
  /** Said at the typed field (confirm_mismatch). */
  error?: string;
  busy?: boolean;
}

export interface StepUpRequest {
  /** What it's for ("Delete snapshot pre-upgrade"). */
  reason: string;
  /** T4: always asks, even with a grant. */
  fresh: boolean;
  resolve: (ok: boolean) => void;
}

export interface ToastAction {
  label: string;
  run: () => void;
}

export interface ActionsDeps {
  live: Live;
  state: { confirm: ConfirmRequest | undefined; stepUp: StepUpRequest | undefined };
  toast: (text: string, tone?: 'ok' | 'error', ms?: number, action?: ToastAction) => void;
  openRecipe: (instance: string) => void;
  /** The viewer may open an integration's settings (where its recipe is). */
  canOpenRecipe?: () => boolean;
  /** Poll interval for a hub without the `tasks` topic. */
  pollMs?: number;
}

/** A step-up the person closed: the change didn't happen, and nothing needs saying. */
export class StepUpCancelled extends Error {
  constructor() {
    super('Cancelled.');
    this.name = 'StepUpCancelled';
  }
}

/** A step-up grant lasts 5 minutes on the hub; trust it here a little less long. */
const GRANT_MS = 4.5 * 60_000;
const UNDO_MS = 10_000;

export function createActions(deps: ActionsDeps) {
  const { live, state, toast } = deps;
  /** Tasks this tab started, as the approve call returned them (the topic supersedes). */
  const known = reactive<Record<string, Task>>({});
  /** Tasks started here, announced when they finish. */
  const mine = new Set<string>();
  const announced = new Set<string>();
  /** Incident id → the task working on it. */
  const byIncident = reactive<Record<string, string>>({});
  let grantUntil = 0;

  const fromTopic = computed<Record<string, Task>>(() => live.doc.tasks?.tasks ?? {});
  const tasks = computed<Record<string, Task>>(() => ({ ...known, ...fromTopic.value }));
  /** Running tasks, oldest first. */
  const running = computed(() =>
    Object.values(tasks.value)
      .filter((t) => !isDone(t))
      .sort((a, b) => (a.started_at ?? '').localeCompare(b.started_at ?? '')),
  );

  /** The task working on this incident: the one started from it here, else a running one on its action. */
  function taskFor(incident: Incident): Task | undefined {
    const id = byIncident[incident.id];
    if (id && tasks.value[id]) return tasks.value[id];
    const acts = [incident.suggested, ...(incident.alternatives ?? [])].filter((a): a is Action => !!a);
    return running.value.find((t) => acts.some((a) => a.id === t.action && a.target === t.target));
  }

  // ── step-up ──

  /** Ask the person to confirm it's them. Resolves false if they close the dialog. */
  function stepUp(reason: string, fresh = false): Promise<boolean> {
    if (!fresh && grantUntil > Date.now()) return Promise.resolve(true);
    state.stepUp?.resolve(false);
    return new Promise((resolve) => {
      state.stepUp = { reason, fresh, resolve };
    });
  }

  /** The step-up dialog's answer. */
  function stepUpDone(ok: boolean): void {
    const s = state.stepUp;
    state.stepUp = undefined;
    if (ok) grantUntil = Date.now() + GRANT_MS;
    s?.resolve(ok);
  }

  /**
   * Run a request (a settings save, a user change) that the hub may refuse
   * with `step_up_required`: step up, then try once more. Throws
   * StepUpCancelled if the person closes the dialog.
   */
  async function withStepUp<T>(reason: string, fn: () => Promise<T>): Promise<T> {
    try {
      return await fn();
    } catch (err) {
      if (!needsStepUp(err)) throw err;
      grantUntil = 0;
      if (!(await stepUp(reason))) throw new StepUpCancelled();
      return fn();
    }
  }

  // ── the ladder ──

  const keyOf = (req: ActionRequest) => req.incident?.id;

  function setFix(req: ActionRequest, fix: { state: 'sending' | 'accepted' | 'failed'; error?: string; recipe?: string; task?: string }): void {
    const id = keyOf(req);
    if (!id) return;
    const phase = req.incident?.phase;
    live.fixes[id] = { ...fix, ...(phase !== undefined && fix.state === 'accepted' ? { phase } : {}) };
  }

  function clearSending(req: ActionRequest): void {
    const id = keyOf(req);
    if (id && live.fixes[id]?.state === 'sending') delete live.fixes[id];
  }

  function fail(req: ActionRequest, f: ActionFailure): void {
    const canRecipe = deps.canOpenRecipe?.() ?? true;
    const link = f.code === 'unavailable' && f.recipe && canRecipe ? { label: 'Open its settings', run: () => deps.openRecipe(f.recipe!) } : undefined;
    toast(f.message, 'error', link ? 10_000 : 6000, link);
    setFix(req, { state: 'failed', error: f.message, ...(f.recipe ? { recipe: f.recipe } : {}) });
  }

  /** The action as a dialog shows it, from the plan. */
  function shown(req: ActionRequest, p: Plan): Action {
    const a: Action = { id: p.action, label: p.label, tier: p.tier, target: p.target };
    if (req.detail) a.detail = req.detail;
    const impact = p.impact ?? req.impact;
    if (impact) a.impact = impact;
    return a;
  }

  function open(kind: ConfirmKind, req: ActionRequest, p: Plan, note?: string): void {
    clearSending(req);
    const c: ConfirmRequest = { kind, req, plan: p, action: shown(req, p) };
    if (req.incident) c.incident = req.incident;
    if (note) c.note = note;
    state.confirm = c;
  }

  /** Is something for this request already in flight? */
  function busy(req: ActionRequest): boolean {
    const id = keyOf(req);
    return !!id && live.fixes[id]?.state === 'sending';
  }

  /** Plan an action and take it up the ladder. */
  async function run(req: ActionRequest): Promise<void> {
    if (busy(req)) return;
    setFix(req, { state: 'sending' });
    let p: Plan;
    try {
      p = await planAction(req.action, req.target, req.params);
    } catch (err) {
      fail(req, actionError(err));
      return;
    }
    climb(req, p);
  }

  function climb(req: ActionRequest, p: Plan, note?: string): void {
    if (p.confirm === 'hold') open('hold', req, p, note);
    else if (p.confirm === 'type') open('type', req, p, note);
    else if (note) open('review', req, p, note);
    else void execute(req, p);
  }

  /** Show a plan that changed: the dialog for its rung, or a plain review for T0 and T1. */
  function changed(req: ActionRequest, p: Plan, note: string): void {
    const kind: ConfirmKind = p.confirm === 'type' ? 'type' : p.confirm === 'hold' ? 'hold' : 'review';
    open(kind, req, p, note);
  }

  async function replan(req: ActionRequest): Promise<Plan | ActionFailure> {
    try {
      return await planAction(req.action, req.target, req.params);
    } catch (err) {
      return actionError(err);
    }
  }

  /** A dialog's confirm (the ring filled, the name was typed, or Run on a review). */
  async function confirmed(typed?: string): Promise<void> {
    const c = state.confirm;
    if (!c) return;
    if (c.kind === 'type') {
      // The typed dialog stays open while it runs, so a mismatch is said at the field.
      c.busy = true;
      c.error = '';
      await execute(c.req, c.plan, typed, c);
    } else {
      state.confirm = undefined;
      await execute(c.req, c.plan, typed);
    }
  }

  function cancel(): void {
    const c = state.confirm;
    state.confirm = undefined;
    if (c) clearSending(c.req);
  }

  function closeDialog(dialog: ConfirmRequest | undefined): void {
    if (dialog && state.confirm === dialog) state.confirm = undefined;
  }

  async function execute(req: ActionRequest, p: Plan, typed?: string, dialog?: ConfirmRequest, tried = { expired: false, stepUp: false, refused: false }): Promise<void> {
    if (p.step_up !== 'none' && !tried.stepUp) {
      if (!(await stepUp(p.label, p.step_up === 'fresh'))) {
        if (dialog) dialog.busy = false;
        closeDialog(dialog);
        clearSending(req);
        return;
      }
      tried = { ...tried, stepUp: true };
    }
    setFix(req, { state: 'sending' });
    let task: Task;
    try {
      task = await approvePlan(p, typed);
    } catch (err) {
      const f = actionError(err);
      if (f.code === 'step_up_required' && !tried.refused) {
        grantUntil = 0;
        if (await stepUp(p.label, p.step_up === 'fresh')) return execute(req, p, typed, dialog, { ...tried, stepUp: true, refused: true });
        if (dialog) dialog.busy = false;
        closeDialog(dialog);
        clearSending(req);
        return;
      }
      if (f.code === 'plan_expired' && !tried.expired) {
        const next = await replan(req);
        if (!('hash' in next)) {
          closeDialog(dialog);
          return fail(req, next);
        }
        const np = next;
        // Silently, only if the person confirmed the same thing; otherwise show them.
        if (np.confirm === p.confirm && np.confirm_text === p.confirm_text) return execute(req, np, typed, dialog, { ...tried, expired: true });
        closeDialog(dialog);
        return changed(req, np, 'That plan expired, and the new one is different. Check it, then confirm again.');
      }
      if (f.code === 'plan_changed') {
        const next = f.plan ?? (await replan(req));
        closeDialog(dialog);
        if ('hash' in next) return changed(req, next, f.message);
        return fail(req, next);
      }
      if (f.code === 'confirm_mismatch' && dialog && state.confirm === dialog) {
        dialog.busy = false;
        dialog.error = f.message;
        clearSending(req);
        return;
      }
      closeDialog(dialog);
      return fail(req, f);
    }
    closeDialog(dialog);
    started(req, p, task);
  }

  function started(req: ActionRequest, p: Plan, task: Task): void {
    known[task.id] = task;
    mine.add(task.id);
    if (req.incident) byIncident[req.incident.id] = task.id;
    setFix(req, { state: 'accepted', task: task.id });
    const undo = p.undo;
    if (p.confirm === 'undo' && undo && !req.isUndo) {
      toast(`Started: ${p.label}.`, 'ok', UNDO_MS, {
        label: 'Undo',
        run: () =>
          void run({ action: undo.action, target: p.target, label: undo.label, isUndo: true, ...(undo.params ? { params: undo.params } : {}) }),
      });
    } else {
      toast(`Started: ${p.label}.`);
    }
    if (isDone(task)) finished(task);
    else poll(task.id);
  }

  /** Say how a task started here ended: done, or the hub's sentence for what went wrong. */
  function finished(t: Task): void {
    if (!mine.has(t.id) || announced.has(t.id)) return;
    announced.add(t.id);
    const incident = Object.entries(byIncident).find(([, id]) => id === t.id)?.[0];
    if (t.state === 'succeeded') {
      toast(`Done: ${t.label}.`);
    } else if (t.state === 'cancelled') {
      toast(`Cancelled: ${t.label}.`, 'error');
      if (incident) delete live.fixes[incident];
    } else {
      const message = t.error?.message ? t.error.message : `${t.label} didn’t work.`;
      toast(message, 'error', 8000);
      if (incident) live.fixes[incident] = { state: 'failed', error: message };
    }
  }

  watch(
    () => Object.values(tasks.value).map((t) => `${t.id}:${t.state}`).join('|'),
    () => {
      for (const id of mine) {
        const t = tasks.value[id];
        if (t && isDone(t)) finished(t);
      }
    },
  );

  /**
   * Stop a running task (POST /api/tasks/{id}/cancel). The hub allows it for
   * a task the viewer may see whose action is cancellable; the task then ends
   * as cancelled on the topic, and a task started here says so in a toast.
   * Refusals are the hub's sentence.
   */
  const cancelling = reactive<Record<string, boolean>>({});
  async function cancelTask(t: Task): Promise<void> {
    if (cancelling[t.id] || isDone(t)) return;
    cancelling[t.id] = true;
    try {
      const after = await cancelRequest(t.id);
      if (after && !fromTopic.value[t.id]) known[t.id] = after;
      if (!mine.has(t.id)) toast(`Cancelling: ${t.label}.`);
    } catch (err) {
      toast(hubWords(err, `${t.label} couldn’t be cancelled.`), 'error');
    } finally {
      delete cancelling[t.id];
    }
  }

  /** A hub without the `tasks` topic: ask for the task until it ends (at most 10 minutes). */
  function poll(id: string, deadline = Date.now() + 10 * 60_000): void {
    const every = deps.pollMs ?? 1500;
    setTimeout(async () => {
      if (live.conn.topics['tasks']?.status !== 'unavailable') return; // the topic carries it
      if (fromTopic.value[id] || announced.has(id) || Date.now() > deadline) return;
      try {
        const t = await getTask(id);
        known[id] = t;
        if (isDone(t)) return finished(t);
      } catch {
        /* try again next time */
      }
      poll(id, deadline);
    }, every);
  }

  /** This tab started the task, so a toast says how it ended. */
  const startedHere = (id: string) => mine.has(id);

  return { tasks, running, taskFor, startedHere, run, confirmed, cancel, cancelTask, cancelling, stepUp, stepUpDone, withStepUp, busy };
}

export type Actions = ReturnType<typeof createActions>;
