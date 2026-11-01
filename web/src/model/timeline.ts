/**
 * The timeline (GET /api/timeline, internal/timeline): incidents, their
 * causes, the actions that ran, health changes and maintenance windows on
 * one time axis. The hub words every entry (`text`, with its catalog
 * `message` and `args`), so "Copy as Markdown" only arranges those
 * sentences under their days and times.
 */
import { api } from '../lib/api';
import { hhmm, longDate } from '../lib/format';

export type TimelineKind = 'incident' | 'cause' | 'action' | 'state' | 'window';

/** One entry, as internal/timeline `Entry` encodes it. */
export interface TimelineEntry {
  at: string;
  kind: TimelineKind;
  /** incident: opened, changed, caused, acknowledged, unacknowledged, expected, resolved; action: ok, failed; window: start, end. */
  event?: string;
  resource?: string;
  incident?: string;
  /** down, attention, ok or info: what the mark shows. */
  severity?: string;
  from?: string;
  to?: string;
  action?: string;
  task?: string;
  actor?: string;
  window?: string;
  /** The sentence, in English. */
  text: string;
  /** Its catalog ID and arguments. */
  message: string;
  args?: Record<string, unknown>;
}

/** GET /api/timeline's answer. Entries are oldest first. */
export interface Timeline {
  from: string;
  to: string;
  resource?: string;
  entries: TimelineEntry[];
  /** More than 1,000 entries: the oldest were left out. */
  truncated: boolean;
  /** The metrics history's level: with it off, there are no health changes. */
  history: string;
}

/** The ranges the timeline offers. */
export const TIMELINE_SPANS = [
  { id: '24h', name: '24 hours', ms: 86_400_000 },
  { id: '7d', name: '7 days', ms: 7 * 86_400_000 },
  { id: '30d', name: '30 days', ms: 30 * 86_400_000 },
  { id: '90d', name: '90 days', ms: 90 * 86_400_000 },
] as const;
export type TimelineSpan = (typeof TIMELINE_SPANS)[number]['id'];

/** The span's milliseconds; a week for anything unknown. */
export function spanMs(span: string): number {
  return TIMELINE_SPANS.find((s) => s.id === span)?.ms ?? 7 * 86_400_000;
}

/** The timeline of one resource (or the whole home) over the span ending at now. */
export function fetchTimeline(resource: string | undefined, span: TimelineSpan, now: number): Promise<Timeline> {
  const q = new URLSearchParams({ from: String(now - spanMs(span)), to: String(now) });
  if (resource) q.set('resource', resource);
  return api<Timeline>(`/api/timeline?${q.toString()}`);
}

/** Entries newest first, grouped by day (the display time zone's). */
export function byDay(entries: readonly TimelineEntry[]): { day: string; entries: TimelineEntry[] }[] {
  const out: { day: string; entries: TimelineEntry[] }[] = [];
  for (let i = entries.length - 1; i >= 0; i--) {
    const e = entries[i]!;
    const day = longDate(e.at);
    const last = out.at(-1);
    if (last && last.day === day) last.entries.push(e);
    else out.push({ day, entries: [e] });
  }
  return out;
}

/** What the mark beside an entry says, as a word for people who can't see its colour. */
export function markWord(e: TimelineEntry): string {
  switch (e.kind) {
    case 'cause':
      return 'cause';
    case 'action':
      return e.event === 'failed' ? 'action failed' : 'action';
    case 'window':
      return 'maintenance';
    case 'state':
      return 'health';
    default:
      if (e.event === 'acknowledged' || e.event === 'unacknowledged') return 'acknowledged';
      return e.event === 'resolved' ? 'resolved' : 'incident';
  }
}

/** Markdown text can't open a link or a heading by accident. */
function escapeMd(s: string): string {
  return s.replace(/([\\`*_[\]<>#|])/g, '\\$1').replace(/\s+/g, ' ').trim();
}

/**
 * The range as Markdown, oldest first: a heading, then a section per day
 * with a line per entry. `title` names what it's the timeline of.
 */
export function timelineMarkdown(tl: Timeline, title: string): string {
  const lines = [`# Timeline: ${escapeMd(title)}`, '', `${escapeMd(`${longDate(tl.from)} ${hhmm(tl.from)} to ${longDate(tl.to)} ${hhmm(tl.to)}`)}`];
  if (tl.truncated) lines.push('', 'Only the newest 1,000 entries are listed.');
  if (!tl.entries.length) {
    lines.push('', 'Nothing happened in this range.');
    return lines.join('\n') + '\n';
  }
  let day = '';
  for (const e of tl.entries) {
    const d = longDate(e.at);
    if (d !== day) {
      day = d;
      lines.push('', `## ${escapeMd(d)}`, '');
    }
    lines.push(`- ${hhmm(e.at)} ${escapeMd(e.text)}`);
  }
  return lines.join('\n') + '\n';
}
