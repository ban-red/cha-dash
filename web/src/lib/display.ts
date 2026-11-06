/**
 * A wall display's calm (docs/plan/15-v0.5.md → Wave 33): how bright it is,
 * whether it's tinted for the night, the one incident line that woke it, the
 * burn-in shift and how old its data is. A pure function of the time, the
 * incidents, the connection and the display's quiet hours, so every rule is
 * tested on a fixed clock (test/display.test.ts).
 *
 * - Full brightness for 2 minutes after a change, then dim.
 * - A warm tint from 21:00 to 07:00, hub time.
 * - In quiet hours it's dark; only an unacknowledged `down` problem wakes it.
 * - An unacknowledged `attention` or `down` problem that's new since the page
 *   first saw the Inbox wakes it to full and shows its one line, until it's
 *   resolved or acknowledged.
 * - Acknowledged problems are listed dimmed, with who's on them, and never wake it.
 * - The content moves a few pixels every 3 minutes against burn-in.
 * - When the socket is down, it says how old the data is.
 *
 * Hub time: the hub's zone through Intl when it names one, else its fixed
 * UTC offset in minutes.
 */
import type { GraphDoc, InboxDoc, Incident, Severity } from '../model/types';

export interface QuietHours {
  /** `HH:MM`, hub time. */
  start: string;
  end: string;
}

export interface CalmInput {
  /** The hub's clock, ms. */
  now: number;
  /** The hub's IANA zone, or '' when it has none. */
  zone: string;
  /** The hub's UTC offset in minutes, used when `zone` is empty or unknown. */
  offsetMinutes: number;
  /** The Inbox's incidents (any class and status). */
  incidents: readonly Incident[];
  /** Incidents open when the page first saw the Inbox: they don't wake it. */
  seen: ReadonlySet<string>;
  /** When something on the board last changed (hub clock). */
  lastChange: number;
  /** The socket is live. */
  live: boolean;
  /** When the last data arrived (hub clock); 0 if none ever did. */
  lastDataAt: number;
  quiet: QuietHours | null;
}

export interface CalmLine {
  id: string;
  severity: Severity;
  text: string;
}

export interface Calm {
  brightness: 'full' | 'dim' | 'dark';
  /** Warm tint: night, hub time. */
  tint: boolean;
  /** Inside the display's quiet hours. */
  quiet: boolean;
  /** The incident that woke the display, or null. */
  wake: CalmLine | null;
  /** Acknowledged problems at attention or down, with who's on them. */
  acknowledged: (CalmLine & { by: string })[];
  /** Burn-in offset in CSS pixels. */
  shift: { x: number; y: number };
  /** How old the data is while the socket is down; null while live. */
  stale: { ageMs: number; text: string } | null;
}

const MIN = 60_000;
/** Full brightness after a change. */
export const AWAKE_MS = 2 * MIN;
/** Burn-in step. */
export const SHIFT_MS = 3 * MIN;
const NIGHT_FROM = 21 * 60;
const NIGHT_TO = 7 * 60;

const zoneCache = new Map<string, Intl.DateTimeFormat | null>();
function formatter(zone: string): Intl.DateTimeFormat | null {
  if (!zoneCache.has(zone)) {
    try {
      zoneCache.set(zone, new Intl.DateTimeFormat('en-GB', { timeZone: zone, hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }));
    } catch {
      zoneCache.set(zone, null);
    }
  }
  return zoneCache.get(zone) ?? null;
}

/** Minutes since midnight, hub time. */
export function hubMinutes(now: number, zone: string, offsetMinutes: number): number {
  const f = zone ? formatter(zone) : null;
  if (f) {
    let h = 0;
    let m = 0;
    for (const p of f.formatToParts(new Date(now))) {
      if (p.type === 'hour') h = Number(p.value) % 24;
      else if (p.type === 'minute') m = Number(p.value);
    }
    return h * 60 + m;
  }
  const day = 24 * 60;
  const local = Math.floor((now + offsetMinutes * MIN) / MIN);
  return ((local % day) + day) % day;
}

export const isNight = (minutes: number): boolean => minutes >= NIGHT_FROM || minutes < NIGHT_TO;

function parseHHMM(s: string): number | undefined {
  const m = /^(\d{1,2}):(\d{2})$/.exec(s);
  if (!m) return undefined;
  const h = Number(m[1]);
  const mm = Number(m[2]);
  return h < 24 && mm < 60 ? h * 60 + mm : undefined;
}

/** Inside quiet hours, which may span midnight. Equal or malformed ends are never quiet. */
export function inQuiet(minutes: number, q: QuietHours | null | undefined): boolean {
  if (!q) return false;
  const a = parseHHMM(q.start);
  const b = parseHHMM(q.end);
  if (a === undefined || b === undefined || a === b) return false;
  return a < b ? minutes >= a && minutes < b : minutes >= a || minutes < b;
}

/** Eight spots within a few pixels of home, visited in turn. */
const SPOTS: readonly { x: number; y: number }[] = [
  { x: 0, y: 0 },
  { x: 3, y: 1 },
  { x: 2, y: -3 },
  { x: -1, y: 3 },
  { x: -3, y: -1 },
  { x: 1, y: -2 },
  { x: -2, y: 2 },
  { x: 3, y: -1 },
];

/** The burn-in shift at a moment: a new spot every 3 minutes. */
export function shiftAt(now: number): { x: number; y: number } {
  const step = Math.floor(now / SHIFT_MS);
  return SPOTS[((step % SPOTS.length) + SPOTS.length) % SPOTS.length]!;
}

/** "Data from 12 minutes ago": plain words for the age of the data. */
export function staleWords(ageMs: number): string {
  const m = Math.floor(ageMs / MIN);
  if (m < 1) return 'Data from less than a minute ago';
  if (m < 60) return `Data from ${m} minute${m === 1 ? '' : 's'} ago`;
  const h = Math.floor(m / 60);
  if (h < 48) return `Data from ${h} hour${h === 1 ? '' : 's'} ago`;
  return `Data from ${Math.floor(h / 24)} days ago`;
}

const loud = (i: Incident) => i.class === 'problem' && i.status === 'open' && (i.severity === 'attention' || i.severity === 'down');
const RANK: Record<string, number> = { down: 2, attention: 1 };
const byUrgency = (a: Incident, b: Incident) => (RANK[b.severity] ?? 0) - (RANK[a.severity] ?? 0) || b.opened_at.localeCompare(a.opened_at);
const line = (i: Incident): CalmLine => ({ id: i.id, severity: i.severity, text: i.title });

export function calm(i: CalmInput): Calm {
  const minutes = hubMinutes(i.now, i.zone, i.offsetMinutes);
  const quiet = inQuiet(minutes, i.quiet);
  const open = i.incidents.filter(loud);
  const waking = open.filter((x) => !x.acknowledged && !i.seen.has(x.id) && (!quiet || x.severity === 'down')).sort(byUrgency);
  const wake = waking[0] ? line(waking[0]) : null;
  const acknowledged = open
    .filter((x) => x.acknowledged)
    .sort(byUrgency)
    .map((x) => ({ ...line(x), by: x.acknowledged!.by }));
  const brightness = wake ? 'full' : quiet ? 'dark' : i.now - i.lastChange < AWAKE_MS ? 'full' : 'dim';
  let stale: Calm['stale'] = null;
  if (!i.live) stale = i.lastDataAt > 0 ? { ageMs: Math.max(0, i.now - i.lastDataAt), text: staleWords(Math.max(0, i.now - i.lastDataAt)) } : { ageMs: 0, text: 'Waiting for the hub' };
  return { brightness, tint: isNight(minutes), quiet, wake, acknowledged, shift: shiftAt(i.now), stale };
}

function healthOf(h: unknown): string {
  if (typeof h === 'string') return h;
  if (h && typeof h === 'object' && 'status' in h) return String((h as { status: unknown }).status);
  return '';
}

/**
 * What counts as a change on the board: each resource's health and state, and
 * each incident's status, severity and acknowledgement. Metrics tick all the
 * time and don't count; neither does the order things arrive in.
 */
export function changeKey(graph: GraphDoc | undefined, inbox: InboxDoc | undefined): string {
  if (!graph && !inbox) return '';
  const parts: string[] = [];
  for (const r of Object.values(graph?.resources ?? {})) parts.push(`r ${r.id} ${healthOf(r.health)} ${r.state ?? ''}`);
  for (const x of Object.values(inbox?.incidents ?? {})) parts.push(`i ${x.id} ${x.status} ${x.severity} ${x.acknowledged?.by ?? ''}`);
  return parts.sort().join('\n');
}
