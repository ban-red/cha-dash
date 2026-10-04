/**
 * Graph and metric readers the widgets share. Pure functions of the topic
 * documents, by kind and relation, never by fixed ID, so the Demo Home's
 * `app:sonarr` and a real `sonarr:<instance id>/app` read the same way.
 */
import { duration, fmtInt, fmtNum } from '../lib/format';
import { attr, byKind, distinct, incoming, label, outgoing, resolve, series } from '../model/graph';
import { normalizeHealth, resourceLinks, type GraphDoc, type HistoryAxis, type MetricsDoc, type Resource, type Series } from '../model/types';

/** An app's integration type: `attrs.type`, or the Demo Home's `app:<type>` ID. */
export const typeOf = (r: Resource | undefined): string | undefined => attr<string>(r, 'type') ?? /^app:([a-z0-9_-]+)$/.exec(r?.id ?? '')?.[1];

/** Latency keys a check (built-in or Uptime Kuma) may report, by preference. */
export const LATENCY_KEYS = ['http.ms', 'tcp.ms', 'ping.ms', 'response.ms'] as const;

export function latencyOf(m: MetricsDoc | undefined, id: string): { key: string; series: Series } | undefined {
  for (const key of LATENCY_KEYS) {
    const s = series(m, id, key);
    if (s) return { key, series: s };
  }
  return undefined;
}

/** The points after the first known one: before it, the check didn't exist yet. */
function sinceFirstKnown(h: readonly (number | null)[] | undefined): (number | null)[] {
  const vs = h ?? [];
  const first = vs.findIndex((v) => typeof v === 'number');
  return first < 0 ? [] : vs.slice(first);
}

/**
 * Uptime over the history window, in percent with one decimal: Uptime
 * Kuma's own `uptime_24h` when it has one, else the share of latency samples
 * that answered (a failed check's latency is unknown, never 0).
 */
export function uptimeOf(r: Resource | undefined, latency: Series | undefined): number | null {
  const own = attr<number>(r, 'uptime_24h');
  if (typeof own === 'number') return own;
  const pts = sinceFirstKnown(latency?.history);
  if (!pts.length) return null;
  return Math.round((pts.filter((v) => typeof v === 'number').length / pts.length) * 1000) / 10;
}

export const fmtUptime = (v: number | null): string => (v === null ? '—' : v >= 99.95 ? '100' : v.toFixed(1));

const norm = (u: string) => u.replace(/\/+$/, '').toLowerCase();

/**
 * The HTTP check that watches an app: a `check` resource whose target is one
 * of the app's links or whose name is the app's, else the app's own latency
 * series (the Demo Home keeps its built-in checks there).
 */
export function checkFor(g: GraphDoc | undefined, m: MetricsDoc | undefined, app: Resource | undefined): { resource?: Resource; series?: Series } | undefined {
  if (!app) return undefined;
  if (app.kind === 'check') {
    const l = latencyOf(m, app.id);
    return l ? { resource: app, series: l.series } : { resource: app };
  }
  const urls = new Set(resourceLinks(app).map((l) => norm(l.url)));
  const url = attr<string>(app, 'url');
  if (url) urls.add(norm(url));
  const name = label(app).toLowerCase();
  const check = byKind(g, 'check').find((c) => {
    const t = attr<string>(c, 'target');
    return (t && urls.has(norm(t))) || label(c).toLowerCase() === name;
  });
  if (check) {
    const l = latencyOf(m, check.id);
    return l ? { resource: check, series: l.series } : { resource: check };
  }
  const own = latencyOf(m, app.id);
  return own ? { series: own.series } : undefined;
}

/** "service up · 46 ms" (or down), from the check that watches it. Empty without one. */
export function checkWords(c: { resource?: Resource; series?: Series } | undefined): string {
  if (!c) return '';
  const h = c.resource ? normalizeHealth(c.resource).status : undefined;
  const v = c.series?.value;
  if (h === 'down' || (h === undefined && c.series && v === null && !c.series.stale)) return 'service down';
  if (typeof v === 'number') return `service up · ${fmtInt(v)} ms`;
  return h === 'ok' ? 'service up' : '';
}

/** The history window: 48 points 30 minutes apart is 24 hours. */
function windowMs(axis: HistoryAxis | undefined): number {
  if (!axis) return NaN;
  if (axis.points > 0 && axis.step_s > 0) return axis.points * axis.step_s * 1000;
  return Date.parse(axis.end) - Date.parse(axis.start);
}

/** "down 40 in 24h": the change over a series' history window. Undefined when flat or unknown. */
export function trendOf(s: Series | undefined, axis: HistoryAxis | undefined, unit = ''): string | undefined {
  const pts = (s?.history ?? []).filter((v): v is number => typeof v === 'number');
  if (pts.length < 2) return undefined;
  const diff = pts[pts.length - 1]! - pts[0]!;
  if (Math.abs(diff) < 1) return undefined;
  const span = windowMs(axis);
  const hours = Math.round(span / 3_600_000);
  const window = !Number.isFinite(span) ? '' : hours >= 144 ? ' this week' : hours > 48 ? ` in ${Math.round(hours / 24)} days` : ` in ${hours} h`;
  return `${diff < 0 ? 'down' : 'up'} ${fmtNum(Math.abs(diff))}${unit}${window}`;
}

/** The soonest future (or just aired) entry in an app's `calendar` attr. */
export function nextRelease(r: Resource | undefined, nowMs: number): { at: string; title: string; detail?: string } | undefined {
  const cal = attr<unknown[]>(r, 'calendar');
  if (!Array.isArray(cal)) return undefined;
  let best: { at: string; title: string; detail?: string } | undefined;
  for (const e of cal) {
    const x = e as { at?: string; title?: string; detail?: string };
    if (!x.at || !x.title || Date.parse(x.at) < nowMs) continue;
    if (!best || Date.parse(x.at) < Date.parse(best.at)) best = x.detail ? { at: x.at, title: x.title, detail: x.detail } : { at: x.at, title: x.title };
  }
  return best;
}

/**
 * One of each thing: a resource another source also reports is an alias of
 * the correlator's primary (`<alias> same_as <primary>`; model/graph.ts), so
 * lists swap aliases for their primary and drop repeats.
 */
export function oneEach(g: GraphDoc | undefined, list: readonly Resource[]): Resource[] {
  const seen = new Set<string>();
  const out: Resource[] = [];
  for (const r of list) {
    const p = outgoing(g, r.id, 'same_as')[0] ?? r;
    if (!seen.has(p.id)) {
      seen.add(p.id);
      out.push(p);
    }
  }
  return out;
}

/** Guests (VMs and LXCs) on a host, on every host of a cluster, or everywhere. */
export function guestsOf(g: GraphDoc | undefined, scope: Resource | undefined): Resource[] {
  const isGuest = (r: Resource) => r.kind === 'vm' || r.kind === 'lxc';
  if (!scope) return distinct(g).filter(isGuest);
  if (scope.kind === 'cluster') {
    const hosts = incoming(g, scope.id, 'part_of').filter((r) => r.kind === 'host');
    const ids = new Set(hosts.map((h) => h.id));
    // Real Proxmox guests are cluster-scoped; they still run on a node.
    return distinct(g).filter((r) => isGuest(r) && outgoing(g, r.id, 'runs_on').some((h) => ids.has(h.id)));
  }
  return oneEach(g, incoming(g, scope.id, 'runs_on').filter(isGuest));
}

/**
 * Containers on an engine, on a host (its engines' and its own: Unraid's
 * containers run on the host directly), or everywhere; one row per container
 * whichever sources report it.
 */
export function containersOf(g: GraphDoc | undefined, scope: Resource | undefined): Resource[] {
  const isC = (r: Resource) => r.kind === 'container';
  if (!scope) return distinct(g).filter(isC);
  const direct = incoming(g, scope.id, 'runs_on');
  const viaEngines = direct.filter((r) => r.kind === 'engine').flatMap((e) => incoming(g, e.id, 'runs_on'));
  return oneEach(g, [...direct, ...viaEngines].filter(isC));
}

/** Storage that belongs to a host: PVE storages, Unraid pools and its array. */
export function storageOf(g: GraphDoc | undefined, host: Resource | undefined): Resource[] {
  if (!host) return [];
  const kinds = new Set(['storage', 'pool', 'array']);
  return incoming(g, host.id, 'part_of').filter((r) => kinds.has(r.kind));
}

/** The array a widget means: the target itself, the array on a target host, or the first array. */
export function arrayFor(g: GraphDoc | undefined, target: string | undefined): Resource | undefined {
  const t = target ? resolve(g, target) : undefined;
  if (t?.kind === 'array') return t;
  if (t) return incoming(g, t.id, 'part_of').find((r) => r.kind === 'array');
  return byKind(g, 'array')[0];
}

/** Human words for metric keys, for labels and accessible names. */
const METRIC_WORDS: Record<string, string> = {
  'cpu.pct': 'CPU',
  'mem.pct': 'Memory',
  'mem.mib': 'Memory',
  'swap.pct': 'Swap',
  'rootfs.pct': 'Root disk',
  'disk.pct': 'Disk',
  'temp.c': 'Temperature',
  'used.pct': 'Used',
  'rx.mbps': 'Download',
  'tx.mbps': 'Upload',
  'rx.gb_30d': 'Down, 30 days',
  'tx.gb_30d': 'Up, 30 days',
  'dl.mbps': 'Download',
  'ul.mbps': 'Upload',
  'power.kw': 'Power',
  'energy.kwh': 'Energy today',
  'http.ms': 'Response time',
  'tcp.ms': 'Connect time',
  'ping.ms': 'Ping',
  'response.ms': 'Response time',
  'inference.ms': 'Inference',
  people_home: 'People home',
  lights_on: 'Lights on',
  switches_on: 'Switches on',
  'detections.24h': 'Detections today',
  'tasks.failed_24h': 'Failed tasks, 24 h',
  'last_ok.age_h': 'Since last good backup',
};

export function metricWords(key: string): string {
  const known = METRIC_WORDS[key];
  if (known) return known;
  const s = key.replace(/[._]/g, ' ');
  return s[0]!.toUpperCase() + s.slice(1);
}

/** Display unit for a series: "%" stays tight, "count" disappears. */
export function unitOf(s: Series | undefined, key = ''): string {
  const u = s?.unit ?? (key.endsWith('.pct') ? '%' : key.endsWith('.ms') ? 'ms' : '');
  return u === 'count' ? '' : u;
}

/** A value with its own precision: percentages whole, temperatures whole, the rest three digits. */
export function fmtValue(v: number | null | undefined, unit: string): string {
  if (unit === '%' || unit === '°C' || unit === 'ms' || unit === 'MiB') return fmtInt(v);
  return fmtNum(v);
}

/** Minimum and maximum of the known history. */
export function extent(s: Series | undefined): { min: number; max: number } | undefined {
  const pts = (s?.history ?? []).filter((v): v is number => typeof v === 'number');
  if (!pts.length) return undefined;
  return { min: Math.min(...pts), max: Math.max(...pts) };
}

/** "13d 3h" since a boot or start time. */
export function upFor(at: string | undefined, nowMs: number): string {
  const t = at ? Date.parse(at) : NaN;
  return Number.isFinite(t) && nowMs >= t ? duration(nowMs - t) : '—';
}

/** "full in ~3 weeks": PBS's own estimate when it has one, else the trend of the history. */
export function fullIn(r: Resource | undefined, used: Series | undefined, axis: HistoryAxis | undefined): string | undefined {
  let days = attr<number>(r, 'full_in_days');
  if (typeof days !== 'number') {
    const pts = (used?.history ?? []).filter((v): v is number => typeof v === 'number');
    const span = windowMs(axis) / 86_400_000;
    if (pts.length < 2 || !Number.isFinite(span) || span <= 0) return undefined;
    const perDay = (pts[pts.length - 1]! - pts[0]!) / span;
    if (perDay <= 0.05) return undefined;
    days = (100 - pts[pts.length - 1]!) / perDay;
  }
  if (!Number.isFinite(days) || days < 0 || days > 180) return undefined;
  if (days < 2) return 'full in ~1 day';
  if (days < 14) return `full in ~${Math.round(days)} days`;
  if (days < 60) return `full in ~${Math.round(days / 7)} weeks`;
  return `full in ~${Math.round(days / 30)} months`;
}

/** Free space from a size in TB and a used percentage: "1.2 TB free". */
export function freeWords(r: Resource | undefined, usedPct: number | null | undefined): string {
  const tb = attr<number>(r, 'size_tb') ?? attr<number>(r, 'capacity_tb');
  if (typeof tb !== 'number' || typeof usedPct !== 'number') return '';
  const free = tb * (1 - usedPct / 100);
  return free >= 1 ? `${fmtNum(free, 2)} TB free` : `${fmtInt(free * 1000)} GB free`;
}
