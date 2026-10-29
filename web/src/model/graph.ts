/**
 * Read-only views over the Home Graph and metrics: the questions widgets ask.
 * Pure functions of the topic documents, so they're tested against the
 * simulator snapshots directly.
 */
import { normalizeHealth, seriesKey, type GraphDoc, type Health, type MetricsDoc, type Resource, type Series } from './types';

export const resources = (g: GraphDoc | undefined): Resource[] => (g ? Object.values(g.resources) : []);

/**
 * One thing seen by several sources (Docker and Unraid both report a
 * container) stays one resource per source in the graph; the hub's
 * correlator picks a primary and publishes `<other> same_as <primary>`
 * (internal/correlate). Aliases are the resources with an outgoing
 * `same_as`: lists skip them, so they show one of each thing.
 */
export function aliases(g: GraphDoc | undefined): Set<string> {
  const out = new Set<string>();
  if (g) for (const rel of Object.values(g.relations)) if (rel.type === 'same_as') out.add(rel.from);
  return out;
}

/** The resources, one per thing: no aliases. */
export const distinct = (g: GraphDoc | undefined): Resource[] => {
  const alias = aliases(g);
  return resources(g).filter((r) => !alias.has(r.id));
};

/** The primary and every alias of `id`'s thing, primary first: what each source says about it. */
export function members(g: GraphDoc | undefined, id: string): Resource[] {
  if (!g) return [];
  const primary = outgoing(g, id, 'same_as')[0] ?? g.resources[id];
  if (!primary) return [];
  return [primary, ...incoming(g, primary.id, 'same_as').sort((a, b) => a.id.localeCompare(b.id))];
}

/** The integration instances reporting a thing, by name ("Docker", "Unraid"). */
export function sourcesOf(g: GraphDoc | undefined, id: string): string[] {
  const names: string[] = [];
  for (const m of members(g, id)) {
    const n = m.source ? label(g?.resources[m.source]) || m.source : '';
    if (n && !names.includes(n)) names.push(n);
  }
  return names;
}

const HEALTH_RANK: Record<string, number> = { down: 0, attention: 1, repair: 2, maintenance: 3, unknown: 4, ok: 5 };

/** A thing's health across its sources: the worst one, with its reason. */
export function mergedHealth(g: GraphDoc | undefined, id: string): Health {
  let best: Health | undefined;
  for (const m of members(g, id)) {
    const h = normalizeHealth(m);
    if (!best || (HEALTH_RANK[h.status] ?? 9) < (HEALTH_RANK[best.status] ?? 9)) best = h;
  }
  return best ?? { status: 'unknown' };
}

export const byKind = (g: GraphDoc | undefined, kind: string): Resource[] => {
  const alias = aliases(g);
  return resources(g)
    .filter((r) => r.kind === kind && !alias.has(r.id))
    .sort((a, b) => label(a).localeCompare(label(b)));
};

export const label = (r: Resource | undefined): string => r?.name || r?.id || '';

export function series(m: MetricsDoc | undefined, resource: string, metric: string): Series | undefined {
  return m?.series?.[seriesKey(resource, metric)];
}

/** The current value, or null when unknown. */
export function value(m: MetricsDoc | undefined, resource: string, metric: string): number | null {
  const s = series(m, resource, metric);
  return s && typeof s.value === 'number' ? s.value : null;
}

/**
 * The resource a widget means by `id`. The exact resource if there is one
 * (the Demo Home's `app:plex`); otherwise, for `app:<type>`, the app a real
 * integration instance publishes as `<type>:<instance id>/app` with
 * `attrs.type` = <type> (the first by ID, if there are several).
 */
export function resolve(g: GraphDoc | undefined, id: string): Resource | undefined {
  if (!g || !id) return undefined;
  const exact = g.resources[id];
  if (exact) return exact;
  const type = /^app:([a-z0-9_-]+)$/.exec(id)?.[1];
  return type ? appsOfType(g, type)[0] : undefined;
}

/** Every app of one integration type, by ID: `app:<type>` itself, or instances' apps. */
export function appsOfType(g: GraphDoc | undefined, type: string): Resource[] {
  return resources(g)
    .filter((r) => r.kind === 'app' && (r.id === `app:${type}` || attr<string>(r, 'type') === type))
    .sort((a, b) => a.id.localeCompare(b.id));
}

/** The integration instance reporting on a resource: whoever `monitors` it. */
export function integrationOf(g: GraphDoc | undefined, id: string | undefined): Resource | undefined {
  return id ? incoming(g, id, 'monitors').find((r) => r.kind === 'integration') : undefined;
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
 * magpie's engine → "magpie"; Home Assistant in a VM on heron → "heron";
 * a container on rook → "rook".
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

/** One container, whichever sources report it: Docker's view first, the worst health of all. */
export interface ContainerRow {
  id: string;
  name: string;
  state: string | undefined;
  health: Health;
  /** The integration instances that report it ("Magpie Docker", "Magpie"). */
  sources: string[];
}

export interface EngineSummary {
  id: string;
  name: string;
  health: Health;
  running: number;
  total: number;
  unhealthy: ContainerRow[];
  containers: ContainerRow[];
}

export function engines(g: GraphDoc | undefined): EngineSummary[] {
  const alias = aliases(g);
  return byKind(g, 'engine').map((e) => {
    const cs: ContainerRow[] = incoming(g, e.id, 'runs_on')
      .filter((r) => r.kind === 'container' && !alias.has(r.id))
      .sort((a, b) => label(a).localeCompare(label(b)))
      .map((c) => ({ id: c.id, name: label(c), state: c.state, health: mergedHealth(g, c.id), sources: sourcesOf(g, c.id) }));
    return {
      id: e.id,
      name: label(e),
      health: normalizeHealth(e),
      running: cs.filter((c) => c.state === 'running').length,
      total: cs.length,
      unhealthy: cs.filter((c) => c.health.status === 'attention' || c.health.status === 'down'),
      containers: cs,
    };
  });
}

export interface CalendarEntry {
  at: string;
  title: string;
  detail?: string;
  source: string;
  /** The *arr isn't monitoring it; only listed where the tile asks. */
  unmonitored?: boolean;
}

/**
 * Upcoming releases from the *arrs' `calendar` attr, soonest first.
 * Unmonitored ones (`unmonitored: true` on an entry) are left out unless
 * `withUnmonitored` says yes for their source.
 */
export function calendar(g: GraphDoc | undefined, nowMs: number, withUnmonitored?: (r: Resource) => boolean): CalendarEntry[] {
  const out: CalendarEntry[] = [];
  for (const r of resources(g)) {
    const cal = attr<unknown[]>(r, 'calendar');
    if (!Array.isArray(cal)) continue;
    const unmon = withUnmonitored?.(r) ?? false;
    for (const e of cal) {
      const entry = e as { at?: string; title?: string; detail?: string; unmonitored?: boolean };
      if (entry.unmonitored === true && !unmon) continue;
      if (entry.at && entry.title && Date.parse(entry.at) >= nowMs - 3_600_000) {
        const item: CalendarEntry = { at: entry.at, title: entry.title, source: label(r) };
        if (entry.detail) item.detail = entry.detail;
        if (entry.unmonitored === true) item.unmonitored = true;
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
