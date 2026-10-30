/**
 * Events for the Calendar widget, from calendar resources (kind `calendar`,
 * `attrs.events`; integrations/ical and integrations/homeassistant): each
 * `{start, end, all_day, title, location?}`, timed ones as RFC 3339 and
 * all-day ones as `YYYY-MM-DD` with `end` exclusive. Media releases (Up
 * next's `attrs.calendar`) can join them as zero-length timed events.
 *
 * Days are keyed `YYYY-MM-DD` in the display time zone, so an all-day event
 * stays on its date wherever the browser is, and a timed one lands on the
 * day the board shows.
 */
import { getDisplayTimeZone } from '../lib/format';
import { attr, label, type CalendarEntry } from './graph';
import type { Resource } from './types';

export interface AgendaEvent {
  key: string;
  title: string;
  location?: string;
  allDay: boolean;
  /** Instants; an all-day event's run from its first day's start to its last's end, in the display zone. */
  startMs: number;
  endMs: number;
  /** The first and last day it's on (inclusive). */
  startDay: string;
  endDay: string;
  /** The calendar (or app) it came from. */
  source: Resource;
  /** For a media release: its detail line and that it's one. */
  media?: { detail?: string };
}

const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** "2026-10-03": the day an instant falls on, in the display time zone. */
export function dayKey(ms: number): string {
  return new Date(ms).toLocaleDateString('en-CA', { timeZone: getDisplayTimeZone() });
}

/** A day key moved by whole days. */
export function addDays(day: string, n: number): string {
  const [y, m, d] = day.split('-').map(Number) as [number, number, number];
  return new Date(Date.UTC(y, m - 1, d + n)).toISOString().slice(0, 10);
}

/** Whole days from one key to another. */
export function daysBetween(from: string, to: string): number {
  return Math.round((Date.parse(`${to}T00:00:00Z`) - Date.parse(`${from}T00:00:00Z`)) / 86_400_000);
}

/** Monday 0 … Sunday 6. */
export function weekday(day: string): number {
  return (new Date(`${day}T12:00:00Z`).getUTCDay() + 6) % 7;
}

const starts = new Map<string, number>();
/**
 * The instant a day starts in the display time zone: from noon UTC, onto the
 * day (zones reach ±14 h), then back to where the date changes, by the hour
 * and then the minute. Cached per zone and day.
 */
export function dayStart(day: string): number {
  const memo = `${getDisplayTimeZone() ?? ''}|${day}`;
  const hit = starts.get(memo);
  if (hit !== undefined) return hit;
  let t = Date.parse(`${day}T12:00:00Z`);
  while (dayKey(t) > day) t -= 3_600_000;
  while (dayKey(t) < day) t += 3_600_000;
  while (dayKey(t - 3_600_000) === day) t -= 3_600_000;
  while (dayKey(t - 60_000) === day) t -= 60_000;
  starts.set(memo, t);
  return t;
}

/** The events of calendar resources, soonest first, all-day before timed on the same start. */
export function eventsOf(calendars: readonly Resource[]): AgendaEvent[] {
  const out: AgendaEvent[] = [];
  for (const r of calendars) {
    const list = attr<unknown[]>(r, 'events');
    if (!Array.isArray(list)) continue;
    list.forEach((raw, i) => {
      const e = raw as { start?: unknown; end?: unknown; all_day?: unknown; title?: unknown; location?: unknown };
      if (typeof e.start !== 'string' || typeof e.title !== 'string' || !e.title) return;
      const allDay = e.all_day === true || DATE.test(e.start);
      const location = typeof e.location === 'string' && e.location ? e.location : undefined;
      if (allDay) {
        if (!DATE.test(e.start)) return;
        const end = typeof e.end === 'string' && DATE.test(e.end) && e.end > e.start ? e.end : addDays(e.start, 1);
        const endDay = addDays(end, -1);
        out.push({ key: `${r.id}#${i}`, title: e.title, ...(location ? { location } : {}), allDay, startMs: dayStart(e.start), endMs: dayStart(end), startDay: e.start, endDay, source: r });
        return;
      }
      const startMs = Date.parse(e.start);
      if (!Number.isFinite(startMs)) return;
      const parsedEnd = typeof e.end === 'string' ? Date.parse(e.end) : NaN;
      const endMs = Number.isFinite(parsedEnd) && parsedEnd >= startMs ? parsedEnd : startMs;
      // An event ending at midnight ends on the day before.
      const endDay = endMs > startMs ? dayKey(endMs - 1) : dayKey(startMs);
      out.push({ key: `${r.id}#${i}`, title: e.title, ...(location ? { location } : {}), allDay, startMs, endMs, startDay: dayKey(startMs), endDay, source: r });
    });
  }
  return out.sort((a, b) => a.startMs - b.startMs || Number(b.allDay) - Number(a.allDay) || a.title.localeCompare(b.title));
}

/** Media releases as events, from Up next's entries and the apps they came from (matched by name). */
export function mediaEvents(entries: readonly CalendarEntry[], apps: readonly Resource[]): AgendaEvent[] {
  const byName = new Map(apps.map((r) => [label(r), r]));
  return entries.flatMap((e, i) => {
    const source = byName.get(e.source);
    const ms = Date.parse(e.at);
    if (!source || !Number.isFinite(ms)) return [];
    const day = dayKey(ms);
    return [{ key: `media#${i}#${e.at}`, title: e.title, allDay: false, startMs: ms, endMs: ms, startDay: day, endDay: day, source, media: e.detail ? { detail: e.detail } : {} }];
  });
}

/** The events on a day: all-day ones first, then by start. */
export function onDay(events: readonly AgendaEvent[], day: string): AgendaEvent[] {
  return events
    .filter((e) => e.startDay <= day && day <= e.endDay)
    .sort((a, b) => Number(b.allDay || b.startDay < day) - Number(a.allDay || a.startDay < day) || a.startMs - b.startMs);
}
