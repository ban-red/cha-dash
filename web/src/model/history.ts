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
