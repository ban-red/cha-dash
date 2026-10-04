/**
 * The Inbox and "Needs attention", derived from the `inbox` topic.
 *
 * - Problems (class `problem`, severity attention or down, open) feed Needs
 *   attention and the Briefing.
 * - Repairs (class `repair`) are integration issues: Inbox only, never
 *   mirrored as outages (02 · Experience → Voice and copy).
 * - Info items are open but settled (e.g. qBittorrent recovered, fix pending),
 *   or **hints**: standing advice to tighten something (a socket proxy that's
 *   too broad, Frigate's anonymous admin). Hints have their own Inbox group;
 *   they never feed Needs attention, the Briefing or the Incidents tile.
 * - Security findings (`category: security`) arrive at the severity the
 *   hub's security level gives them: at Relaxed (the default) every one is
 *   info, so a hint. An info security finding stays in Hints whatever its
 *   phase, so it never reaches the Incidents tile as "settling".
 */
import type { GraphDoc, Incident, InboxDoc, Resource } from './types';
import { label } from './graph';

const SEV_RANK: Record<string, number> = { down: 0, attention: 1, info: 2 };

const byPriority = (a: Incident, b: Incident) =>
  (SEV_RANK[a.severity] ?? 3) - (SEV_RANK[b.severity] ?? 3) || Date.parse(a.opened_at) - Date.parse(b.opened_at) || a.id.localeCompare(b.id);

export const incidents = (inbox: InboxDoc | undefined): Incident[] => (inbox ? Object.values(inbox.incidents ?? {}) : []);

export const isOpen = (i: Incident) => i.status === 'open';
export const needsYou = (i: Incident) => isOpen(i) && (i.severity === 'attention' || i.severity === 'down');

export function openProblems(inbox: InboxDoc | undefined): Incident[] {
  return incidents(inbox)
    .filter((i) => i.class === 'problem' && needsYou(i))
    .sort(byPriority);
}

export function openRepairs(inbox: InboxDoc | undefined): Incident[] {
  return incidents(inbox)
    .filter((i) => i.class === 'repair' && isOpen(i))
    .sort(byPriority);
}

/** Open, but settling by itself (severity info, in a fixing phase). Hints aren't settling. */
export function settling(inbox: InboxDoc | undefined): Incident[] {
  return incidents(inbox)
    .filter((i) => i.class === 'problem' && isOpen(i) && !needsYou(i) && !isHint(i))
    .sort(byPriority);
}

export function resolved(inbox: InboxDoc | undefined): Incident[] {
  return incidents(inbox)
    .filter((i) => i.status === 'resolved')
    .sort((a, b) => Date.parse(b.resolved_at ?? b.updated_at ?? b.opened_at) - Date.parse(a.resolved_at ?? a.updated_at ?? a.opened_at));
}

/**
 * Phases in which something is already working on the incident: a fix that
 * was accepted, a retry, a restart, or the hub applying an update. The Demo
 * Home and real instances use the same words.
 */
export const FIXING_PHASES: ReadonlySet<string> = new Set([
  'retrying',
  'pruning',
  'collecting',
  'restarting',
  'recovering',
  'recovered',
  'applying',
  'updating',
  'reconnecting',
  'fixing',
  'in_progress',
]);

/** Open, but in hand: a fix is running, or it's recovering by itself. */
export function isBeingFixed(i: Incident, fixState?: string): boolean {
  if (i.status !== 'open') return false;
  return fixState === 'sending' || fixState === 'accepted' || FIXING_PHASES.has(i.phase ?? '');
}

export const isSecurity = (i: Incident) => i.category === 'security';

/** An open info security finding: a hint, whatever its phase. */
const isSecurityHint = (i: Incident) => i.class === 'problem' && i.status === 'open' && i.severity === 'info' && isSecurity(i);

/** A hint: an open info problem nothing is working on, i.e. standing advice, or an info security finding. */
export function isHint(i: Incident): boolean {
  return isSecurityHint(i) || (i.class === 'problem' && i.status === 'open' && i.severity === 'info' && !FIXING_PHASES.has(i.phase ?? ''));
}

/** The Hints group's heading: "Security hints · 3" when every hint is a security finding. */
export function hintsHeading(hints: readonly Incident[]): string {
  return `${hints.length && hints.every(isSecurity) ? 'Security hints' : 'Hints'} · ${hints.length}`;
}

export interface InboxGroups {
  /** Needs you: problems (down first), then repairs. */
  open: Incident[];
  /** The correlator's questions: are these two things one? Oldest first. */
  suggestions: Incident[];
  /** In hand, not resolved yet. */
  fixing: Incident[];
  /** Standing advice: worth tightening, nothing broken. Oldest first. */
  hints: Incident[];
  /** Newest first. */
  resolved: Incident[];
}

const CLASS_RANK: Record<string, number> = { problem: 0, repair: 1 };

export const isSuggestion = (i: Incident) => i.class === 'suggestion';

/** The Inbox by status: open, suggestions, being fixed, hints, resolved. */
export function groupInbox(inbox: InboxDoc | undefined, fixes: Readonly<Record<string, { state: string } | undefined>> = {}): InboxGroups {
  const open: Incident[] = [];
  const suggestions: Incident[] = [];
  const fixing: Incident[] = [];
  const hints: Incident[] = [];
  for (const i of incidents(inbox)) {
    if (i.status !== 'open') continue;
    if (isSuggestion(i)) suggestions.push(i);
    else if (isSecurityHint(i)) hints.push(i);
    else if (isBeingFixed(i, fixes[i.id]?.state)) fixing.push(i);
    else if (isHint(i)) hints.push(i);
    else open.push(i);
  }
  const order = (a: Incident, b: Incident) =>
    (SEV_RANK[a.severity] ?? 3) - (SEV_RANK[b.severity] ?? 3) || (CLASS_RANK[a.class] ?? 2) - (CLASS_RANK[b.class] ?? 2) || byPriority(a, b);
  return { open: open.sort(order), suggestions: suggestions.sort(byPriority), fixing: fixing.sort(order), hints: hints.sort(byPriority), resolved: resolved(inbox) };
}

/** Open incidents whose target (or blast radius) touches a resource. Suggestions are questions, not incidents. */
export function incidentsFor(inbox: InboxDoc | undefined, resourceIds: readonly string[]): Incident[] {
  const set = new Set(resourceIds);
  return incidents(inbox).filter((i) => isOpen(i) && !isSuggestion(i) && set.has(i.target));
}

const KIND_WORDS: Record<string, string> = {
  backup_target: 'Backups',
  backup_job: 'Backups',
  host: 'Host',
  vm: 'VM',
  lxc: 'Container (LXC)',
  container: 'Container',
  app: 'Service',
  integration: 'Integration',
  engine: 'Docker',
  router: 'Network',
  camera: 'Camera',
  array: 'Storage',
  pool: 'Storage',
};

/** The small-caps kind line of a strip: "Backups", "Host · jupiter‑nas". */
export function incidentKicker(i: Incident, g: GraphDoc | undefined): string {
  const r: Resource | undefined = g?.resources[i.target];
  const kind = r ? (KIND_WORDS[r.kind] ?? r.kind) : 'Home';
  if (!r || r.kind === 'backup_target' || r.kind === 'backup_job') return kind;
  return `${kind} · ${nbHyphen(label(r))}`;
}

/** Long host names must not break at their hyphens in prose. */
export const nbHyphen = (s: string) => s.replace(/-/g, '‑');

/** The incident's target, as a name. */
export function targetName(i: Incident, g: GraphDoc | undefined): string {
  return label(g?.resources[i.target]) || i.target;
}
