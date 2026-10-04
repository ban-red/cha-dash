/**
 * Read-only views over the Home Graph and metrics: the questions widgets ask.
 * Pure functions of the topic documents, so they're tested against the
 * simulator snapshots directly.
 */
import { normalizeHealth, seriesKey, type GraphDoc, type Health, type MetricsDoc, type Resource, type Series } from './types';

export const resources = (g: GraphDoc | undefined): Resource[] => (g ? Object.values(g.resources) : []);

export const byKind = (g: GraphDoc | undefined, kind: string): Resource[] =>
  resources(g)
    .filter((r) => r.kind === kind)
    .sort((a, b) => label(a).localeCompare(label(b)));

export const label = (r: Resource | undefined): string => r?.name || r?.id || '';

export function series(m: MetricsDoc | undefined, resource: string, metric: string): Series | undefined {
  return m?.series?.[seriesKey(resource, metric)];
}

/** The current value, or null when unknown. */
export function value(m: MetricsDoc | undefined, resource: string, metric: string): number | null {
  const s = series(m, resource, metric);
  return s && typeof s.value === 'number' ? s.value : null;
}

export function attr<T = unknown>(r: Resource | undefined, key: string): T | undefined {
  return r?.attrs?.[key] as T | undefined;
}

/** Targets of `from --type--> ?`. */
export function outgoing(g: GraphDoc | undefined, from: string, type: string): Resource[] {
  if (!g) return [];
  const out: Resource[] = [];
  for (const rel of Object.values(g.relations)) {
    if (rel.from === from && rel.type === type) {
      const r = g.resources[rel.to];
      if (r) out.push(r);
    }
  }
  return out;
}

/** Sources of `? --type--> to`. */
export function incoming(g: GraphDoc | undefined, to: string, type: string): Resource[] {
  if (!g) return [];
  const out: Resource[] = [];
  for (const rel of Object.values(g.relations)) {
    if (rel.to === to && rel.type === type) {
      const r = g.resources[rel.from];
      if (r) out.push(r);
    }
  }
  return out;
}

/**
 * Where something runs, in the words people use: an app in a container on
 * jupiter's engine → "jupiter"; Home Assistant in a VM on minig5 → "minig5";
 * a container on pvedocker → "pvedocker".
 */
export function placeOf(g: GraphDoc | undefined, id: string): string | undefined {
  if (!g) return undefined;
  let cur = g.resources[id];
  const seen = new Set<string>();
  let last: Resource | undefined;
  while (cur && !seen.has(cur.id)) {
    seen.add(cur.id);
    const next = outgoing(g, cur.id, 'runs_on')[0];
    if (!next) break;
    if (next.kind === 'engine') return label(next);
    last = next;
    cur = next;
  }
  return last && last.id !== id ? label(last) : undefined;
}

const ROLE_WORDS: Record<string, string> = {
  'network-core': 'network core',
  nas: 'NAS',
  docker: 'Docker host',
  workstation: 'workstation',
  'home-automation': 'home automation',
  cluster: 'cluster node',
};

export interface HostRow {
  id: string;
  name: string;
  role: string;
  platform: string;
  health: Health;
  cpu: number | null;
  mem: number | null;
  memSeries: Series | undefined;
  temp: number | null;
  guestsRunning: number;
  guestsTotal: number;
  /** Docker engines on the host itself (Unraid). */
  engines: number;
  stale: boolean;
}

export function hostRows(g: GraphDoc | undefined, m: MetricsDoc | undefined): HostRow[] {
  return byKind(g, 'host').map((h) => {
    const guests = incoming(g, h.id, 'runs_on').filter((r) => r.kind === 'vm' || r.kind === 'lxc');
    const role = h.labels?.['chadash.role'];
    const platform = String(attr(h, 'platform') ?? '');
    const mem = series(m, h.id, 'mem.pct');
    return {
      id: h.id,
      name: label(h),
      role: [(role && ROLE_WORDS[role]) || role, platform === 'unraid' ? 'Unraid' : ''].filter(Boolean).join(' · '),
      platform,
      health: normalizeHealth(h),
      cpu: value(m, h.id, 'cpu.pct'),
      mem: value(m, h.id, 'mem.pct'),
      memSeries: mem,
      temp: value(m, h.id, 'temp.c'),
      guestsRunning: guests.filter((r) => r.state === 'running').length,
      guestsTotal: guests.length,
      engines: incoming(g, h.id, 'runs_on').filter((r) => r.kind === 'engine').length,
      stale: Boolean(mem?.stale),
    };
  });
}

export interface EngineSummary {
  id: string;
  name: string;
  health: Health;
  running: number;
  total: number;
  unhealthy: Resource[];
  containers: Resource[];
}

export function engines(g: GraphDoc | undefined): EngineSummary[] {
  return byKind(g, 'engine').map((e) => {
    const cs = incoming(g, e.id, 'runs_on')
      .filter((r) => r.kind === 'container')
      .sort((a, b) => label(a).localeCompare(label(b)));
    return {
      id: e.id,
      name: label(e),
      health: normalizeHealth(e),
      running: cs.filter((c) => c.state === 'running').length,
      total: cs.length,
      unhealthy: cs.filter((c) => {
        const s = normalizeHealth(c).status;
        return s === 'attention' || s === 'down';
      }),
      containers: cs,
    };
  });
}

export interface CalendarEntry {
  at: string;
  title: string;
  detail?: string;
  source: string;
}

/** Upcoming releases from the *arrs' `calendar` attr, soonest first. */
export function calendar(g: GraphDoc | undefined, nowMs: number): CalendarEntry[] {
  const out: CalendarEntry[] = [];
  for (const r of resources(g)) {
    const cal = attr<unknown[]>(r, 'calendar');
    if (!Array.isArray(cal)) continue;
    for (const e of cal) {
      const entry = e as { at?: string; title?: string; detail?: string };
      if (entry.at && entry.title && Date.parse(entry.at) >= nowMs - 3_600_000) {
        const item: CalendarEntry = { at: entry.at, title: entry.title, source: label(r) };
        if (entry.detail) item.detail = entry.detail;
        out.push(item);
      }
    }
  }
  return out.sort((a, b) => Date.parse(a.at) - Date.parse(b.at));
}

/** The newest successful nightly backup run (end time), if any. */
export function lastBackup(g: GraphDoc | undefined): { at: string; ok: number; failed: number } | undefined {
  let best: { at: string; ok: number; failed: number } | undefined;
  for (const j of byKind(g, 'backup_job')) {
    const end = attr<string>(j, 'last_run.end');
    if (!end) continue;
    const ok = Number(attr(j, 'last_run.ok') ?? 0);
    const failed = Number(attr(j, 'last_run.failed') ?? 0);
    if (!best || Date.parse(end) > Date.parse(best.at)) best = { at: end, ok: (best?.ok ?? 0) + ok, failed: (best?.failed ?? 0) + failed };
    else best = { ...best, ok: best.ok + ok, failed: best.failed + failed };
  }
  return best;
}

export const isProblem = (h: Health) => h.status === 'attention' || h.status === 'down';
