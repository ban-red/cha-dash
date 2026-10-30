/**
 * Metrics history from the hub (GET /api/history, ADR-0031): averages, lows
 * and highs on a fixed grid, oldest first, null for a gap. The hub picks
 * the tier: by the minute for an hour, by the quarter hour for a day or a
 * week, by the hour beyond with long-term history on.
 */
import { api } from '../lib/api';
import { hhmm, shortDate } from '../lib/format';

export type HistoryLevel = 'off' | 'standard' | 'long-term';

export interface HistorySeries {
  unit: string;
  /** Unix ms of point 0; point i is at start + i × step. */
  start: number;
  step: number;
  avg: (number | null)[];
  min: (number | null)[];
  max: (number | null)[];
}

export interface HistoryReply {
  level: HistoryLevel;
  series: Record<string, HistorySeries>;
}

/** The ranges a sheet offers, newest last. */
export const SPANS = [
  { id: '1h', name: '1 h', ms: 3_600_000, points: 60 },
  { id: '24h', name: '24 h', ms: 86_400_000, points: 96 },
  { id: '7d', name: '7 d', ms: 7 * 86_400_000, points: 168 },
  { id: '30d', name: '30 d', ms: 30 * 86_400_000, points: 720 },
] as const;
export type SpanId = (typeof SPANS)[number]['id'];

/** The series' history over the span ending at now. */
export function fetchHistory(keys: readonly string[], span: SpanId, now: number): Promise<HistoryReply> {
  const s = SPANS.find((x) => x.id === span)!;
  const q = new URLSearchParams({ from: String(now - s.ms), to: String(now), points: String(s.points) });
  for (const k of keys) q.append('series', k);
  return api<HistoryReply>(`/api/history?${q.toString()}`);
}

/** When point i is: "14:05" within a day, "Sat 3 Oct 14:00" beyond. */
export function pointTime(h: HistorySeries, i: number): string {
  const t = h.start + i * h.step;
  return h.step < 3_600_000 && h.avg.length * h.step <= 86_400_000 ? hhmm(t) : `${shortDate(t)} ${hhmm(t)}`;
}

/** The highest high, or undefined when there's no point. */
export function peak(h: HistorySeries): number | undefined {
  let out: number | undefined;
  for (const v of h.max) if (typeof v === 'number' && (out === undefined || v > out)) out = v;
  return out;
}

/** How long the level keeps points, for the sheet's hint. */
export function keepWords(level: HistoryLevel): string {
  if (level === 'long-term') return 'Cha Dash keeps them by the minute for 48 hours, then by the quarter hour, the hour and the day.';
  if (level === 'standard') return 'Cha Dash keeps them by the minute for 48 hours and by the quarter hour for 14 days.';
  return 'Cha Dash doesn’t keep them: history is off on this hub.';
}

export interface StateChange {
  /** Unix ms the status began (the first is moved to the range's start). */
  at: number;
  status: string;
}

export interface StatesReply {
  level: HistoryLevel;
  states: StateChange[];
  /** Milliseconds in each status over the range, up to now. */
  totals: Record<string, number>;
  /** The share of the watched time it was ok or attention; null when none was watched. */
  up: number | null;
}

/** A resource's health over the last ms milliseconds. */
export function fetchStates(resource: string, ms: number, now: number): Promise<StatesReply> {
  const q = new URLSearchParams({ resource, from: String(now - ms), to: String(now) });
  return api<StatesReply>(`/api/history/states?${q.toString()}`);
}

/** "Down", "Needs attention", "Cha Dash stopped": a status as a phrase. */
export function stateWords(status: string): string {
  switch (status) {
    case 'ok':
      return 'Healthy';
    case 'attention':
      return 'Needs attention';
    case 'down':
      return 'Down';
    case 'repair':
      return 'Needs repair';
    case 'maintenance':
      return 'In maintenance';
    case 'stopped':
      return 'Cha Dash stopped';
    case 'gone':
      return 'Gone from the graph';
  }
  return 'Unknown';
}

/** "99.2%": rounded down, so 100% means it never went down. */
export function upWords(up: number): string {
  return `${Math.floor(up * 1000) / 10}%`;
}

export interface StateSegment {
  status: string;
  /** Left edge and width, as percentages of the range. */
  left: number;
  width: number;
  from: number;
  to: number;
}

/** The changes as bar segments over [from, to]. */
export function segments(states: readonly StateChange[], from: number, to: number): StateSegment[] {
  const span = to - from;
  if (span <= 0) return [];
  return states.flatMap((s, i) => {
    const a = Math.max(s.at, from);
    const b = Math.min(states[i + 1]?.at ?? to, to);
    if (b <= a) return [];
    return [{ status: s.status, left: ((a - from) / span) * 100, width: ((b - a) / span) * 100, from: a, to: b }];
  });
}

/** A CSV cell: quoted when it holds a comma, quote or line break; text that a spreadsheet would run as a formula starts with '. */
function cell(v: string): string {
  const s = /^[=+\-@\t\r]/.test(v) && Number.isNaN(Number(v)) ? `'${v}` : v;
  return /[",\r\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
}

/**
 * The reply as CSV: a row a point, oldest first, with its time in UTC
 * (ISO 8601), then the average, low and high of each series, empty for a
 * gap. names labels the columns ("CPU" for "vm:a#cpu.pct"); the unit follows
 * in brackets.
 */
export function historyCsv(reply: HistoryReply, names: Record<string, string> = {}): string {
  const keys = Object.keys(reply.series);
  const times = new Set<number>();
  for (const k of keys) {
    const h = reply.series[k]!;
    for (let i = 0; i < h.avg.length; i++) times.add(h.start + i * h.step);
  }
  const head = ['time'];
  for (const k of keys) {
    const h = reply.series[k]!;
    const name = names[k] ?? k;
    const unit = h.unit ? ` (${h.unit})` : '';
    head.push(`${name} avg${unit}`, `${name} min${unit}`, `${name} max${unit}`);
  }
  const num = (v: number | null | undefined) => (typeof v === 'number' ? String(v) : '');
  const rows = [...times]
    .sort((a, b) => a - b)
    .map((t) => {
      const row = [new Date(t).toISOString()];
      for (const k of keys) {
        const h = reply.series[k]!;
        const i = (t - h.start) / h.step;
        row.push(num(h.avg[i]), num(h.min[i]), num(h.max[i]));
      }
      return row.map(cell).join(',');
    });
  return [head.map(cell).join(','), ...rows].join('\r\n') + '\r\n';
}

/** Saves text as a file through the browser; false when it can't. */
export function saveText(text: string, name: string, type = 'text/csv'): boolean {
  if (typeof URL.createObjectURL !== 'function') return false;
  const url = URL.createObjectURL(new Blob([text], { type: `${type};charset=utf-8` }));
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  a.rel = 'noopener';
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
  return true;
}

/** "plex-24h-2026-10-29.csv": a file name from a resource's name, the span and the day. */
export function csvName(resource: string, span: string, now: number): string {
  const base = resource
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 60);
  return `${base || 'history'}-${span}-${new Date(now).toISOString().slice(0, 10)}.csv`;
}

/** One slot of the Pulse strip: resources by the worst they were, and the ones that weren't ok. */
export interface PulseSlot {
  counts: Partial<Record<'ok' | 'attention' | 'repair' | 'down', number>>;
  issues: Record<string, string>;
}

export interface PulseReply {
  level: HistoryLevel;
  /** Unix ms of slot 0; slot i is [start + i × step, start + (i + 1) × step). */
  start: number;
  step: number;
  slots: PulseSlot[];
}

/** The whole home's health over the last day, by the quarter hour (GET /api/history/pulse). */
export function fetchPulse(now: number, points = 96): Promise<PulseReply> {
  const q = new URLSearchParams({ from: String(now - 86_400_000), to: String(now), points: String(points) });
  return api<PulseReply>(`/api/history/pulse?${q.toString()}`);
}

/** A slot's colour: the worst anyone was, or 'none' when nothing was watched. */
export function pulseTone(s: PulseSlot | undefined): 'down' | 'attention' | 'ok' | 'none' {
  if (!s) return 'none';
  if (s.counts.down) return 'down';
  if (s.counts.attention || s.counts.repair) return 'attention';
  return s.counts.ok ? 'ok' : 'none';
}

/** The slot holding at, or undefined outside the reply. */
export function pulseSlotAt(p: PulseReply | undefined, at: number): number | undefined {
  if (!p || p.step <= 0) return undefined;
  const i = Math.floor((at - p.start) / p.step);
  return i >= 0 && i < p.slots.length ? i : undefined;
}

/** What a resource was at a moment the Pulse covers: its issue, else ok; undefined when nothing was watched then. */
export function pastStatus(p: PulseReply | undefined, resource: string, at: number): string | undefined {
  const i = pulseSlotAt(p, at);
  const s = i === undefined ? undefined : p!.slots[i];
  if (!s || pulseTone(s) === 'none') return undefined;
  return s.issues[resource] ?? 'ok';
}
