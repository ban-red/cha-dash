/**
 * Settings → Updates: the Update Center's shapes, as internal/updates
 * answers them, and the helpers its pane and tests share.
 *
 *   GET    /api/updates                         → {updates: UpdateRow[]}
 *   GET    /api/updates/{resource}/notes        → ReleaseNotes; 409 release_notes_off, 404 no_source
 *   GET    /api/updates/settings                → {release_notes}
 *   PUT    /api/updates/settings                {release_notes} (admin)
 *   POST   /api/updates/rollouts/preview        {targets} → RolloutPlan
 *   GET    /api/updates/rollouts                → {rollouts: Rollout[]}
 *   DELETE /api/updates/{resource}/rejected     Try again → the row; 404 not_rejected
 *   POST   /api/updates/rollouts/{id}/cancel    → the rollout; a running one: 202, it stops before its next step
 *   GET    /api/windows                         → {windows: WindowView[]}
 *   POST   /api/windows, PUT /api/windows/{id}  WindowInput → the window (admin)
 *   DELETE /api/windows/{id}                    (admin)
 *
 * A rollout is the `updates.rollout` action on `chadash:updates`, with
 * `{targets, hash, window?}` or `{rollout}`: the usual hold-to-confirm and
 * task sheet.
 */
import type { MaintenanceWindow, Rollout, RolloutStep, Tier } from '../model/types';

export type Risk = 'patch' | 'minor' | 'major' | 'unknown';
export type RowKind = 'image' | 'apt' | 'os';

export interface UpdateRow {
  resource: string;
  name: string;
  /** Absent from a hub before 0.4: an image. */
  kind?: RowKind;
  risk?: Risk;
  engine: string;
  host: string;
  stack?: string;
  image?: string;
  /** rolled_back: its update rolled back from the digest the registry still offers (`rejected`). */
  state: 'current' | 'available' | 'cant_check' | 'held' | 'rolled_back';
  current?: string;
  latest?: string;
  checked_at?: string;
  reason?: string;
  hold?: { kind: 'hold' | 'snooze'; until?: string; created_by?: string; created_at: string };
  /** The digest its last update rolled back from, and why; no rollout takes it until a newer one or Try again. */
  rejected?: { digest: string; reason?: string; rollout?: string; created_at: string };
  /** The action that updates it. `stack.update` for a container of a stack Cha Dash owns: the stack updates as one. */
  update?: { id: string; target: string };
  why?: string;
  link?: { title: string; url: string };
  packages?: { name: string; from?: string; to?: string; risk: Risk }[];
  /** The GitHub repository the image says it's built from. */
  source?: string;
  version?: string;
}

export interface RolloutPlan {
  steps: RolloutStep[];
  hash: string;
  tier: Tier;
  ordered: boolean;
}

export interface Release {
  tag: string;
  name?: string;
  published_at?: string;
  url?: string;
  /** Untrusted plain text: no HTML; links only into the same repository. */
  notes: string;
  truncated?: boolean;
}

export interface ReleaseNotes {
  repo: string;
  from?: string;
  releases: Release[];
  newer: boolean;
  fetched_at: string;
}

export interface WindowView extends MaintenanceWindow {
  open: boolean;
  next_open?: string;
  next_end?: string;
}

export interface WindowInput {
  name: string;
  hosts: string[];
  starts_at: string;
  ends_at: string;
  weekly: boolean;
}

/** The rollout action and its target. */
export const ROLLOUT_ACTION = 'updates.rollout';
export const ROLLOUT_TARGET = 'chadash:updates';

export const kindOf = (r: UpdateRow): RowKind => r.kind ?? 'image';

/** A row a rollout can take: an image with an update and an action. */
export const selectable = (r: UpdateRow): boolean => kindOf(r) === 'image' && r.state === 'available' && !!r.update;

const RISK_WORDS: Record<Risk, string> = {
  patch: 'patch',
  minor: 'minor',
  major: 'major',
  unknown: 'risk unknown',
};

/** The chip's words for a risk; '' for none. */
export function riskWords(r: Risk | undefined): string {
  return r ? RISK_WORDS[r] : '';
}

/** A piece of release notes: plain text, or a link into the repository. */
export interface NotePart {
  text: string;
  href?: string;
}

/**
 * Splits release notes into text and links. Only https links into `repo`
 * (`https://github.com/<owner>/<repo>` or below it) become links; the hub
 * already removed every other, and this checks again, since the text came
 * from someone else.
 */
export function noteParts(text: string, repo: string): NotePart[] {
  const out: NotePart[] = [];
  const re = /https:\/\/github\.com\/[^\s<>()[\]"']+/g;
  let last = 0;
  for (const m of text.matchAll(re)) {
    const url = m[0].replace(/[.,;:!?]+$/, '');
    const start = m.index;
    if (!inRepo(url, repo)) continue;
    if (start > last) out.push({ text: text.slice(last, start) });
    out.push({ text: url, href: url });
    last = start + url.length;
  }
  if (last < text.length) out.push({ text: text.slice(last) });
  return out;
}

/** An https link into repo, without climbing out of it. */
export function inRepo(url: string, repo: string): boolean {
  if (!repo.startsWith('https://github.com/')) return false;
  let u: URL;
  try {
    u = new URL(url);
  } catch {
    return false;
  }
  if (u.protocol !== 'https:' || u.username || u.password || u.host.toLowerCase() !== 'github.com') return false;
  const want = repo.slice('https://github.com'.length).toLowerCase();
  const path = u.pathname.toLowerCase();
  if (/(^|\/)\.\.(\/|$)/.test(url.slice('https://github.com'.length))) return false;
  return path === want || path.startsWith(`${want}/`);
}

/** A digest as people read it: its first 12 hex digits. */
export const shortDigest = (d?: string): string => (d ? d.replace(/^sha256:/, '').slice(0, 12) : '');

/** What a rolled-back row says: the digest and why. */
export function rolledBackWords(r: UpdateRow): string {
  if (!r.rejected) return '';
  const why = r.rejected.reason ? `: ${r.rejected.reason}` : '.';
  return `Rolled back from ${shortDigest(r.rejected.digest)}${why} Update all leaves it out until a newer image appears.`;
}

/** A rollout that can be cancelled now: before it starts, or while it runs and isn't already stopping. */
export const cancellable = (r: Rollout): boolean =>
  r.state === 'scheduled' || r.state === 'waiting' || (r.state === 'running' && !r.cancelling);

/** What a step's state reads as. */
export const STEP_WORDS: Record<RolloutStep['state'], string> = {
  pending: 'waiting its turn',
  running: 'updating',
  succeeded: 'updated',
  rolled_back: 'rolled back',
  not_started: 'didn’t start',
  skipped: 'left out',
};

/** What a rollout's state reads as. */
export const ROLLOUT_WORDS: Record<Rollout['state'], string> = {
  scheduled: 'Scheduled',
  waiting: 'Waits for you',
  running: 'Running',
  succeeded: 'Done',
  failed: 'Stopped',
  cancelled: 'Cancelled',
};

/** The value of a datetime-local input for an ISO time, in the browser's zone. */
export function localInput(iso: string | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const p = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

/** An ISO time from a datetime-local input's value; '' when it isn't one. */
export function fromLocalInput(v: string): string {
  if (!v) return '';
  const d = new Date(v);
  return Number.isNaN(d.getTime()) ? '' : d.toISOString();
}

/** The window form's problem, or '' when it can be sent. */
export function windowProblem(w: WindowInput): string {
  if (!w.name.trim()) return 'Give the window a name.';
  if (!w.hosts.length) return 'Pick at least one host.';
  if (!w.starts_at || !w.ends_at) return 'Set when it starts and ends.';
  const len = Date.parse(w.ends_at) - Date.parse(w.starts_at);
  if (!(len > 0)) return 'It has to end after it starts.';
  if (w.weekly && len > 7 * 24 * 3600_000) return 'A weekly window can’t be longer than a week.';
  return '';
}

/** The stack an update row updates with (its `stack.update` target), if any. */
export function stackOf(r: Pick<UpdateRow, 'update'>): string | undefined {
  return r.update?.id === 'stack.update' ? r.update.target : undefined;
}

/**
 * A host's rows in list order, with an owned stack's rows together after a
 * heading for the stack: it updates as one, so its rows have no Update of
 * their own.
 */
export type UpdateItem = { kind: 'row'; row: UpdateRow } | { kind: 'stack'; target: string; name: string; rows: UpdateRow[] };

export function updateItems(rows: UpdateRow[]): UpdateItem[] {
  const out: UpdateItem[] = [];
  const at = new Map<string, Extract<UpdateItem, { kind: 'stack' }>>();
  for (const r of rows) {
    const t = stackOf(r);
    if (!t) {
      out.push({ kind: 'row', row: r });
      continue;
    }
    let item = at.get(t);
    if (!item) {
      item = { kind: 'stack', target: t, name: r.stack || t.slice(t.lastIndexOf('/') + 1), rows: [] };
      at.set(t, item);
      out.push(item);
    }
    item.rows.push(r);
  }
  return out;
}
