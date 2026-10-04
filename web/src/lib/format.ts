/**
 * Number and time formatting (02 · Experience → Number formatting rules):
 * tabular figures (CSS), units set apart by the caller, three significant
 * digits by default, relative time on display, and "—" for unknown. Never a
 * fake 0.
 */

export const UNKNOWN = '—';

let displayTimeZone: string | undefined;

/**
 * The time zone absolute times are shown in. The Demo Home keeps UTC as its
 * clock (sim/README.md), and its incident copy quotes UTC times, so demo mode
 * sets 'UTC'. Undefined means the browser's zone.
 */
export function setDisplayTimeZone(tz: string | undefined): void {
  displayTimeZone = tz;
}

export function getDisplayTimeZone(): string | undefined {
  return displayTimeZone;
}

const isNum = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v);

/** Integer with grouping: 1240 → "1,240". */
export function fmtInt(v: number | null | undefined): string {
  return isNum(v) ? Math.round(v).toLocaleString('en-US') : UNKNOWN;
}

/**
 * Three significant digits, compact above 10,000: 0.84 → "0.84", 9.3 → "9.3",
 * 291 → "291", 1116 → "1,116", 44099 → "44.1k".
 */
export function fmtNum(v: number | null | undefined, digits = 3): string {
  if (!isNum(v)) return UNKNOWN;
  const a = Math.abs(v);
  if (a >= 1e6) return trim((v / 1e6).toPrecision(digits)) + 'M';
  if (a >= 1e4) return trim((v / 1e3).toPrecision(digits)) + 'k';
  if (a >= 100) return fmtInt(v);
  if (a === 0) return '0';
  return trim(v.toPrecision(digits));
}

const trim = (s: string) => (s.includes('.') && !s.includes('e') ? s.replace(/\.?0+$/, '') : s);

/** A percentage without its unit: 92.2 → "92". */
export function fmtPct(v: number | null | undefined): string {
  return isNum(v) ? String(Math.round(v)) : UNKNOWN;
}

function toDate(v: string | number | Date | undefined): Date | undefined {
  if (v === undefined) return undefined;
  const d = v instanceof Date ? v : new Date(v);
  return Number.isNaN(d.getTime()) ? undefined : d;
}

/** "14:02" in the display time zone. */
export function hhmm(v: string | number | Date | undefined): string {
  const d = toDate(v);
  if (!d) return UNKNOWN;
  return d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', hour12: false, timeZone: displayTimeZone });
}

/** "Sat 3 Oct" in the display time zone. */
export function shortDate(v: string | number | Date | undefined): string {
  const d = toDate(v);
  if (!d) return UNKNOWN;
  return d.toLocaleDateString('en-GB', { weekday: 'short', day: 'numeric', month: 'short', timeZone: displayTimeZone });
}

/** "Saturday, 3 October 2026". */
export function longDate(v: string | number | Date | undefined): string {
  const d = toDate(v);
  if (!d) return UNKNOWN;
  return d.toLocaleDateString('en-GB', { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric', timeZone: displayTimeZone });
}

/** Absolute time for titles and hovers: "3 Oct 2026, 14:02 UTC". */
export function absolute(v: string | number | Date | undefined): string {
  const d = toDate(v);
  if (!d) return UNKNOWN;
  return d.toLocaleString('en-GB', {
    day: 'numeric',
    month: 'short',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
    timeZone: displayTimeZone,
    timeZoneName: 'short',
  });
}

/** A compact duration: 45s, 4m, 3h 10m, 13d 3h. */
export function duration(ms: number): string {
  if (!isNum(ms) || ms < 0) return UNKNOWN;
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return m % 60 ? `${h}h ${m % 60}m` : `${h}h`;
  const d = Math.floor(h / 24);
  return h % 24 ? `${d}d ${h % 24}h` : `${d}d`;
}

/** "4m ago", "just now", or the absolute time when it's more than a week. */
export function ago(v: string | number | Date | undefined, now: number): string {
  const d = toDate(v);
  if (!d) return UNKNOWN;
  const ms = now - d.getTime();
  if (ms < 45_000) return 'just now';
  if (ms > 7 * 86_400_000) return shortDate(d);
  return `${duration(ms).split(' ')[0]} ago`;
}

/** Since-phrasing for status words: "since 14:02", or "since Thu 1 Oct" when not today. */
export function since(v: string | number | Date | undefined, now: number): string {
  const d = toDate(v);
  if (!d) return '';
  return now - d.getTime() < 20 * 3_600_000 ? `since ${hhmm(d)}` : `since ${shortDate(d)}`;
}

/** Monogram for a service: "Home Assistant" → "HA", "qBittorrent" → "QB". */
export function monogram(name: string): string {
  const words = name.replace(/[^A-Za-z0-9 ]+/g, ' ').trim().split(/\s+/).filter(Boolean);
  if (words.length >= 2) return ((words[0]?.[0] ?? '') + (words[1]?.[0] ?? '')).toUpperCase();
  return (words[0] ?? '?').slice(0, 2).toUpperCase();
}

export const plural = (n: number, one: string, many = one + 's') => `${n} ${n === 1 ? one : many}`;

/** Number words for prose: 2 → "two" (up to twelve), else the digits. */
export function numberWord(n: number): string {
  const w = ['zero', 'one', 'two', 'three', 'four', 'five', 'six', 'seven', 'eight', 'nine', 'ten', 'eleven', 'twelve'];
  return w[n] ?? String(n);
}
