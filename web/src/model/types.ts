/**
 * Wire shapes of the realtime topics.
 *
 * - `graph`: internal/graph/graph.go `Doc` (`resources` by id, `relations` by key).
 * - `metrics`: `{ series: { "<resource id>#<metric key>": Series } }`, each
 *   series shaped like an entry of `sim/snapshots/*.json` `series`.
 * - `inbox`: `{ now, incidents: { id: Incident } }`, each incident shaped like
 *   an entry of `sim/snapshots/*.json` `incidents`.
 * - `causes`: `{ causes: { "<resource id>": CauseHint[] } }` (internal/topics/causes.go).
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
  /** Where the upstream shows why (its status page), when it has one. */
  url?: string;
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

/**
 * One entry of an app's `uses` attr (internal/integration `Use`): a service
 * it is set up to use, by address. The correlator joins it and publishes
 * `depends_on` with rule `uses`. Never credentials.
 */
export interface Use {
  /** A word for the copy: "download client", "indexer", "media server". */
  role: string;
  url?: string;
  /** What the app calls it, for the copy when nothing joins. */
  name?: string;
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
  /** Each history point's highest sample; rates only. */
  peak?: (number | null)[];
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

/** One relation on a cause hint's path, as the graph has it. */
export interface CauseStep {
  from: string;
  type: string;
  to: string;
}

/**
 * A cause hint (internal/topics `Cause`): why a resource that isn't ok may
 * not be. A hint, never a verdict. `path` comes only from `GET /api/causes`.
 */
export interface CauseHint extends Cause {
  resource: string;
  /** What found it: the relation walked, or the event read. */
  rule: string;
  /** The sentence's message ID and arguments, for translation. */
  message?: string;
  args?: Record<string, unknown>;
  path?: CauseStep[];
}

/** The `causes` topic: `{"causes": {resource id: [CauseHint]}}`, the first cause first. */
export interface CausesDoc {
  causes: Record<string, CauseHint[]>;
}

/** `GET /api/causes?resource=<id>`: one resource's hints, with their paths. */
export interface CausesResponse {
  resource: string;
  causes: CauseHint[];
}

/**
 * `suggestion`: the hub's correlator asking whether two things are one (Merge / Keep separate).
 * `node`: a node that joined with a token and waits for an admin to approve it.
 */
export type IncidentClass = 'problem' | 'repair' | 'suggestion' | 'approval' | 'node';
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
  /**
   * An open problem someone said they're on (operator and up): who, when, and
   * their note (household gets who and when). Notifications stop repeating it
   * while it stays; un-acknowledging or resolving clears it.
   */
  acknowledged?: Acknowledgement | null;
}

/** An incident's acknowledgement (`POST /api/inbox/{id}/acknowledge`). */
export interface Acknowledgement {
  by: string;
  at: string;
  note?: string;
}

/** Who made a plan that waits for a person: an API token, a git sync or an agent, and the user it acts for. */
export interface Originator {
  kind: 'token' | 'sync' | 'agent' | 'user' | string;
  id: string;
  /** "backup script (sam)" for a token, "git sync for sam" for a sync. */
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
    /** A line for whoever approves it: "Commit 1761175: Quiet the logs" from a git sync. */
    note?: string;
    /** An agent's reason for proposing it: its words, untrusted, shown as a quote. */
    rationale?: string;
    params?: Record<string, unknown>;
  };
  originator: Originator;
  requested_at: string;
  expires_at: string;
  decided_at?: string | null;
  /** `step_up`: how an approver confirmed it was them (passkey, password, oidc), when the tier asked. */
  decided_by?: { kind: string; name: string; step_up?: 'passkey' | 'password' | 'oidc' | (string & {}) } | null;
  reason?: string;
  task?: string | null;
  /** An agent's proposal: how many it made in the 24 hours up to this one. */
  nth_today?: number;
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
  /** The `causes` topic: cause hints for the resources that have any. */
  causes?: CausesDoc;
  /** The `backups` topic: backup coverage per protectable resource. */
  backups?: BackupsDoc;
  [topic: string]: unknown;
}

const STATUSES: readonly HealthStatus[] = ['ok', 'attention', 'down', 'unknown', 'maintenance', 'repair'];

function asStatus(v: unknown): HealthStatus {
  return typeof v === 'string' && (STATUSES as readonly string[]).includes(v) ? (v as HealthStatus) : 'unknown';
}

/**
 * Health from either wire shape. With a string `health`, the reason and since
 * may ride in attrs (`health.reason`, `health.since`). The upstream's page
 * for it is always an attr (`health.url`), http(s) only.
 */
export function normalizeHealth(r: Resource | undefined): Health {
  if (!r || r.health === undefined || r.health === null) return { status: 'unknown' };
  if (typeof r.health === 'string') {
    const h: Health = { status: asStatus(r.health) };
    const reason = r.attrs?.['health.reason'];
    const since = r.attrs?.['health.since'];
    if (typeof reason === 'string') h.reason = reason;
    if (typeof since === 'string') h.since = since;
    const url = safeUrl(r.attrs?.['health.url']);
    if (url) h.url = url;
    return h;
  }
  const h: Health = { status: asStatus(r.health.status) };
  if (r.health.reason) h.reason = r.health.reason;
  if (r.health.since) h.since = r.health.since;
  const url = safeUrl(r.attrs?.['health.url']);
  if (url) h.url = url;
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

/** Whether a container's image tag has moved in its registry (integration.ImageUpdate). */
export type UpdateState = 'current' | 'available' | 'cant_check' | 'held';

export interface ImageUpdate {
  state: UpdateState;
  /** Registry digests, "sha256:…". */
  current?: string;
  latest?: string;
  checked_at?: string;
  /** Why it's cant_check, as a sentence. */
  reason?: string;
}

const UPDATE_STATES: readonly string[] = ['current', 'available', 'cant_check', 'held'];

/** A resource's `update` attr, or undefined when it has none or it doesn't parse. */
export function imageUpdate(r: Resource | undefined): ImageUpdate | undefined {
  const u = r?.attrs?.['update'];
  if (!u || typeof u !== 'object' || Array.isArray(u)) return undefined;
  const v = u as Record<string, unknown>;
  if (typeof v.state !== 'string' || !UPDATE_STATES.includes(v.state)) return undefined;
  const out: ImageUpdate = { state: v.state as UpdateState };
  for (const k of ['current', 'latest', 'checked_at', 'reason'] as const) {
    if (typeof v[k] === 'string') out[k] = v[k];
  }
  return out;
}

/*
 * REST shapes of v0.4's contracts (wave 25), mirroring the Go JSON. Waves
 * 26–28 add their routes; backup coverage (wave 27) is below.
 */

/** What an API token is for, fixed at minting (internal/auth `TokenKind`). */
export type TokenKind = 'api' | 'agent';

/** `GET`/`PUT /api/settings/agents` (internal/security `AgentSettings`). Off by default. */
export interface AgentSettings {
  enabled: boolean;
  /** Proposals one agent token may have waiting at once (3 by default, 1–20). */
  max_pending: number;
  /** Proposals one agent token may make an hour (20 by default, 1–200). */
  max_per_hour: number;
}

/** internal/updates `RolloutState`. */
export type RolloutState = 'scheduled' | 'waiting' | 'running' | 'succeeded' | 'failed' | 'cancelled';

/** internal/updates `StepState`. */
export type RolloutStepState = 'pending' | 'running' | 'succeeded' | 'rolled_back' | 'not_started' | 'skipped';

/** One container or stack in a rollout (internal/updates `RolloutStep`). */
export interface RolloutStep {
  /** The container's or stack's resource ID. */
  target: string;
  /** The action that updates it. */
  action: string;
  /** What it snapshots first; absent when nothing can be. */
  snapshot?: 'git' | 'pve';
  state: RolloutStepState;
  /** Why it was skipped or failed, a sentence. */
  reason?: string;
  /** What the list calls the container, and its host. */
  name?: string;
  host?: string;
  /** The PVE guest a `pve` snapshot is of. */
  guest?: string;
  /** The image digest it updates to, as planned. */
  to?: string;
}

/** internal/updates `Rollout`. Times are RFC 3339. */
export interface Rollout {
  id: string;
  created_by?: string;
  plan_hash: string;
  state: RolloutState;
  window_id?: string;
  /** Absent: at once, or when its window opens. */
  scheduled_for?: string;
  steps: RolloutStep[];
  created_at: string;
  started_at?: string;
  finished_at?: string;
  /** Running, and someone asked it to stop: it stops before its next step. */
  cancelling?: boolean;
}

/** internal/updates `MaintenanceWindow`: a weekly one repeats `starts_at`..`ends_at` every 7 days. */
export interface MaintenanceWindow {
  id: string;
  name: string;
  /** Host resource IDs. */
  hosts: string[];
  starts_at: string;
  ends_at: string;
  weekly: boolean;
  created_by?: string;
  created_at: string;
}

/** internal/backups `Objective`: how old the newest good backup may be before it's stale. */
export interface BackupObjective {
  resource: string;
  max_age_ms: number;
  set_by?: string;
  set_at: string;
}

/** internal/backups `State`: where a protectable resource stands. */
export type BackupState = 'fresh' | 'stale' | 'uncovered' | 'unknown';

/** internal/backups `Cover`: one thing that backs a resource up, with its newest good run. */
export interface BackupCover {
  /** A backup job (`backup_job`) or a target a backup tool reports (`backup_target`). */
  id: string;
  kind: string;
  name: string;
  schedule?: string;
  last_ok?: string;
  last_at?: string;
  last_status?: string;
  last_error?: string;
  disabled?: boolean;
  /** Where its backups land: a PBS datastore, when the graph knows it. */
  target?: string;
  target_name?: string;
}

/** internal/backups `Hint`: why it's stale, when something on the way isn't ok. */
export interface BackupHint {
  text: string;
  resource: string;
  message: string;
  args?: Record<string, unknown>;
  /** The open problem it names, when it names one (the datastore's or the job's). */
  incident?: string;
}

/** internal/backups `Item`: one entry of the coverage map. */
export interface BackupItem {
  resource: string;
  kind: string;
  name: string;
  state: BackupState;
  by: BackupCover[];
  /** The guest whose backup a container inherits. */
  through?: string;
  through_name?: string;
  last_ok?: string;
  max_age_ms: number;
  objective: 'override' | 'schedule' | 'default';
  text: string;
  message: string;
  args?: Record<string, unknown>;
  cause?: BackupHint;
}

/** The `backups` topic, and `GET /api/backups`. */
export interface BackupsDoc {
  items: Record<string, BackupItem>;
}

/** internal/incidents `EventKind`. */
export type IncidentEventKind = 'opened' | 'changed' | 'caused' | 'acknowledged' | 'expected' | 'resolved';

/** One step in an incident's lifecycle (internal/incidents `Event`). */
export interface IncidentEvent {
  id: string;
  incident_id: string;
  at: string;
  kind: IncidentEventKind;
  /** What the kind needs; `{}` when nothing. */
  data: Record<string, unknown>;
}
