/**
 * The Briefing: one calm sentence about the home, symptom first. Pure, so the
 * copy is tested without a DOM. Phrases that name an incident carry its id so
 * the page can open the Inbox on it.
 */
import { byKind, calendar, lastBackup, value, type CalendarEntry } from './graph';
import { needsYou, openProblems, openRepairs } from './inbox';
import { normalizeHealth, type GraphDoc, type Incident, type InboxDoc, type MetricsDoc } from './types';

export interface HomeSummary {
  ready: boolean;
  problems: Incident[];
  repairs: Incident[];
  servicesHealthy: number;
  servicesTotal: number;
  cameras: number;
  peopleHome: number | null;
  peopleTotal: number | null;
  next: CalendarEntry | undefined;
  lastBackup: { at: string; ok: number; failed: number } | undefined;
  arrayPct: number | null;
  worst: 'down' | 'attention' | 'ok';
}

export function summarize(g: GraphDoc | undefined, m: MetricsDoc | undefined, inbox: InboxDoc | undefined, nowMs: number): HomeSummary {
  const apps = byKind(g, 'app');
  const problems = openProblems(inbox);
  const array = byKind(g, 'array')[0];
  const ha = g?.resources['app:homeassistant'];
  const peopleTotal = ha?.attrs?.['people_total'];
  return {
    ready: Boolean(g),
    problems,
    repairs: openRepairs(inbox).filter(needsYou),
    servicesHealthy: apps.filter((a) => normalizeHealth(a).status === 'ok').length,
    servicesTotal: apps.length,
    cameras: byKind(g, 'camera').length,
    peopleHome: value(m, 'app:homeassistant', 'people_home'),
    peopleTotal: typeof peopleTotal === 'number' ? peopleTotal : null,
    next: calendar(g, nowMs)[0],
    lastBackup: lastBackup(g),
    arrayPct: array ? value(m, array.id, 'used.pct') : null,
    worst: problems.some((p) => p.severity === 'down') ? 'down' : problems.length ? 'attention' : 'ok',
  };
}

export function peoplePhrase(s: HomeSummary, prose = false): string {
  if (s.peopleHome === null) return '';
  if (s.peopleHome === 0) return 'nobody home';
  if (prose && s.peopleTotal === 2 && s.peopleHome === 2) return 'both of you home';
  return `${s.peopleHome} ${s.peopleHome === 1 ? 'person' : 'people'} home`;
}

/** "a", "a and b", "a, b and c". */
export function joinAnd(items: readonly string[]): string {
  if (items.length <= 1) return items[0] ?? '';
  return `${items.slice(0, -1).join(', ')} and ${items[items.length - 1]}`;
}

/** Sentence-case an incident title for the middle of a sentence ("Backup of …" → "backup of …"). */
export function inSentence(title: string): string {
  const first = title.split(' ')[0] ?? '';
  // Only ordinary words lose their capital; names ("Plex", "PBS", "qBittorrent") keep theirs.
  return COMMON_FIRST_WORDS.has(first) ? first.toLowerCase() + title.slice(first.length) : title;
}

const COMMON_FIRST_WORDS = new Set(['A', 'An', 'The', 'Backup', 'Backups', 'Memory', 'Disk', 'Disks', 'Update', 'Updates', 'Certificate', 'Container', 'Host', 'Storage', 'Network', 'Internet']);
