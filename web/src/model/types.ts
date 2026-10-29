/**
 * Wire shapes of the three realtime topics.
 *
 * - `graph`: internal/graph/graph.go `Doc` (`resources` by id, `relations` by key).
 * - `metrics`: `{ series: { "<resource id>#<metric key>": Series } }`, each
 *   series shaped like an entry of `sim/snapshots/*.json` `series`.
 * - `inbox`: `{ now, incidents: { id: Incident } }`, each incident shaped like
 *   an entry of `sim/snapshots/*.json` `incidents`.
 *
 * The graph's `health` is a plain string in internal/graph, and an object
 * (`{status, reason, since}`) in the simulator. `normalizeHealth` accepts both,
 * so the UI doesn't care which one the hub sends.
 */

export type HealthStatus = 'ok' | 'attention' | 'down' | 'unknown' | 'maintenance' | 'repair';

export interface Health {
  status: HealthStatus;
  reason?: string;
  since?: string;
}

export interface Link {
  title?: string;
  url: string;
}

export interface Resource {
  id: string;
  kind: string;
  name?: string;
  source?: string;
  labels?: Record<string, string>;
  attrs?: Record<string, unknown>;
  state?: string;
  desired?: string;
  health?: HealthStatus | string | Health;
  links?: Link[];
  caps?: string[];
}

export interface Relation {
  from: string;
  to: string;
  type: string;
  source?: string;
}

export interface GraphDoc {
  resources: Record<string, Resource>;
  relations: Record<string, Relation>;
}

export interface Series {
  resource?: string;
  metric?: string;
  unit?: string;
  min?: number;
  max?: number;
  /** Normal range, drawn as a gray band. */
  band?: [number, number];
  warn?: number;
  crit?: number;
  /** Unknown is null, never 0. */
  value: number | null;
  as_of?: string;
  /** `value` is last-known. */
  stale?: boolean;
  /** Oldest first; null where unknown. */
  history?: (number | null)[];
}

export interface HistoryAxis {
  start: string;
  end: string;
  step_s: number;
  points: number;
}

export interface MetricsDoc {
  series: Record<string, Series>;
  /** Optional shared axis for every history, as in the simulator snapshot. */
  history?: HistoryAxis;
}

export type Tier = 'T0' | 'T1' | 'T2' | 'T3' | 'T4';

export interface Impact {
  resource: string;
  via: string;
  depth: number;
}

export interface Action {
  id: string;
  label: string;
  tier: Tier;
  target: string;
  detail?: string;
  /** T2 and up: what the action affects, for the hold-to-confirm list. */
  impact?: Impact[];
  /** Params for the plan (`POST /api/actions/plan`). */
  params?: Record<string, unknown>;
  /** The engine can plan this now (the hub decides availability; v0.2). */
  plan_ready?: boolean;
  /** Why it can't run, as a sentence ("Add a control token to let Cha Dash reboot guests on io"). */
  unavailable?: string;
  /** With `unavailable`: the instance whose recipe grants it. */
  recipe?: string;
  /** Household members may run it too (pausing Pi-hole). */
  household?: boolean;
}

export interface Cause {
  text: string;
  resource?: string;
  at?: string;
}

/** `suggestion`: the hub's correlator asking whether two things are one (Merge / Keep separate). */
export type IncidentClass = 'problem' | 'repair' | 'suggestion' | 'approval';
export type Severity = 'info' | 'attention' | 'down';

export interface Incident {
  id: string;
  scenario?: string;
  class: IncidentClass;
  severity: Severity;
  /** `security` for a security finding, whose severity the hub's security level decides. */
  category?: 'security' | (string & {});
  status: 'open' | 'resolved';
  phase?: string;
  title: string;
  summary: string;
  target: string;
  opened_at: string;
  updated_at?: string;
  resolved_at?: string;
  facts?: Record<string, unknown>;
  causes?: Cause[];
  blast_radius?: Impact[];
  suggested?: Action;
  alternatives?: Action[];
  resolves_when?: string;
  resolved_by?: 'action' | 'auto' | string;
  resolution?: string;
  /** Class `approval` only: the plan waiting for a person, as the hub shows it. */
  approval?: Approval;
}

/** Who made a plan that waits for a person: an API token (or, later, an agent) and its user. */
export interface Originator {
  kind: 'token' | 'agent' | string;
  id: string;
  /** "backup script (sam)": the token and its user. */
  name: string;
  user?: string;
  user_name?: string;
}

/**
 * A plan waiting for a person (08 · v0.2 → Wave 10 status, API tokens): an API
 * token's T2 plan. Admins and the owner approve it in a session, with a
 * step-up, bound to the plan's hash; or deny it. It expires after an hour.
 */
export interface Approval {
  id: string;
  state: 'awaiting_approval' | 'approved' | 'denied' | 'expired';
  plan: {
    id: string;
    hash: string;
    action: string;
    label: string;
    target: string;
    tier: Tier;
    confirm?: string;
    confirm_text?: string;
    steps?: { label: string }[];
    impact?: Impact[];
    undo?: UndoSpec | null;
    expires_at?: string;
  };
  originator: Originator;
  requested_at: string;
  expires_at: string;
  decided_at?: string | null;
  decided_by?: { kind: string; name: string } | null;
  reason?: string;
  task?: string | null;
}

export interface InboxDoc {
  now: string;
  incidents: Record<string, Incident>;
}

/** What an action's undo runs: an action ID plus params, on the same target. */
export interface UndoSpec {
  action: string;
  params?: Record<string, unknown>;
  label: string;
}

export type TaskState = 'queued' | 'running' | 'verifying' | 'succeeded' | 'failed' | 'cancelled';

/** A running or finished action (08 · v0.2 → API shapes → Task). */
export interface Task {
  id: string;
  plan?: string;
  action: string;
  label: string;
  target: string;
  actor?: { kind: string; name?: string };
  /** Who approved it for its actor, when it waited for a person (an API token's T2 plan). */
  approved_by?: { kind: string; name: string };
  state: TaskState;
  /** The current step, and progress 0–1; both optional. */
  step?: string | null;
  progress?: number | null;
  /** The last lines of its log. */
  log?: string[];
  started_at?: string | null;
  finished_at?: string | null;
  error?: { code: string; message: string } | null;
  undo?: UndoSpec | null;
  audit?: string;
  /** Its action allows POST /api/tasks/{id}/cancel while it runs. */
  cancellable?: boolean;
  /** Its log is the result (a container's logs): the page shows it when the task ends. */
  reads_data?: boolean;
}

/** The `tasks` topic: `{"tasks": {id: Task}}`, running plus the last 24 hours, at most 50. */
export interface TasksDoc {
  tasks: Record<string, Task>;
}

/** The subscription documents, one member per topic. */
export interface LiveDoc {
  graph?: GraphDoc;
  metrics?: MetricsDoc;
  inbox?: InboxDoc;
  /** The `tasks` topic: running tasks plus the last 24 hours. */
  tasks?: TasksDoc;
  [topic: string]: unknown;
}

const STATUSES: readonly HealthStatus[] = ['ok', 'attention', 'down', 'unknown', 'maintenance', 'repair'];

function asStatus(v: unknown): HealthStatus {
  return typeof v === 'string' && (STATUSES as readonly string[]).includes(v) ? (v as HealthStatus) : 'unknown';
}

/**
 * Health from either wire shape. With a string `health`, the reason and since
 * may ride in attrs (`health.reason`, `health.since`).
 */
export function normalizeHealth(r: Resource | undefined): Health {
  if (!r || r.health === undefined || r.health === null) return { status: 'unknown' };
  if (typeof r.health === 'string') {
    const h: Health = { status: asStatus(r.health) };
    const reason = r.attrs?.['health.reason'];
    const since = r.attrs?.['health.since'];
    if (typeof reason === 'string') h.reason = reason;
    if (typeof since === 'string') h.since = since;
    return h;
  }
  const h: Health = { status: asStatus(r.health.status) };
  if (r.health.reason) h.reason = r.health.reason;
  if (r.health.since) h.since = r.health.since;
  return h;
}

/**
 * A link the app may render: http or https only. A tile's config, an
 * integration or an import can carry any string; `javascript:` and `data:`
 * never reach an href.
 */
export const safeUrl = (u: unknown): string | undefined => (typeof u === 'string' && /^https?:\/\//i.test(u) ? u : undefined);

/** A resource's links, only those with an http(s) URL (safeUrl). */
export function resourceLinks(r: Resource | undefined): Link[] {
  if (!r) return [];
  const all = Array.isArray(r.links) ? r.links : r.attrs?.['links'];
  return Array.isArray(all) ? (all as Link[]).filter((l) => !!l && safeUrl(l.url) !== undefined) : [];
}

export const seriesKey = (resource: string, metric: string): string => `${resource}#${metric}`;
