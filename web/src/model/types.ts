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
}

export interface Cause {
  text: string;
  resource?: string;
  at?: string;
}

export type IncidentClass = 'problem' | 'repair';
export type Severity = 'info' | 'attention' | 'down';

export interface Incident {
  id: string;
  scenario?: string;
  class: IncidentClass;
  severity: Severity;
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
}

export interface InboxDoc {
  now: string;
  incidents: Record<string, Incident>;
}

/** The subscription documents, one member per topic. */
export interface LiveDoc {
  graph?: GraphDoc;
  metrics?: MetricsDoc;
  inbox?: InboxDoc;
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

export function resourceLinks(r: Resource | undefined): Link[] {
  if (!r) return [];
  if (Array.isArray(r.links)) return r.links;
  const fromAttrs = r.attrs?.['links'];
  return Array.isArray(fromAttrs) ? (fromAttrs as Link[]) : [];
}

export const seriesKey = (resource: string, metric: string): string => `${resource}#${metric}`;
