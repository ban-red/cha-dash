/**
 * The Inbox and "Needs attention", derived from the `inbox` topic.
 *
 * - Problems (class `problem`, severity attention or down, open) feed Needs
 *   attention and the Briefing.
 * - Repairs (class `repair`) are integration issues: Inbox only, never
 *   mirrored as outages (02 · Experience → Voice and copy).
 * - Info items are open but settled (e.g. qBittorrent recovered, fix pending).
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

/** Open, but nothing to do right now (severity info). */
export function settling(inbox: InboxDoc | undefined): Incident[] {
  return incidents(inbox)
    .filter((i) => i.class === 'problem' && isOpen(i) && !needsYou(i))
    .sort(byPriority);
}

export function resolved(inbox: InboxDoc | undefined): Incident[] {
  return incidents(inbox)
    .filter((i) => i.status === 'resolved')
    .sort((a, b) => Date.parse(b.resolved_at ?? b.updated_at ?? b.opened_at) - Date.parse(a.resolved_at ?? a.updated_at ?? a.opened_at));
}

/** Open incidents whose target (or blast radius) touches a resource. */
export function incidentsFor(inbox: InboxDoc | undefined, resourceIds: readonly string[]): Incident[] {
  const set = new Set(resourceIds);
  return incidents(inbox).filter((i) => isOpen(i) && set.has(i.target));
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
