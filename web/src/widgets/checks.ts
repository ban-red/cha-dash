/**
 * Uptime checks as rows: the built-in checks and Uptime Kuma monitors, both
 * kind `check` (integrations/checks, integrations/uptimekuma), with their
 * latency series (`http.ms`, `tcp.ms`, `ping.ms`, `response.ms`).
 *
 * The Demo Home has no `check` resources: its built-in checks are the apps'
 * own `http.ms` series (sim/README.md → Metrics), and Uptime Kuma is the
 * app's `monitors.up`/`monitors.down`. Both are read as a fallback.
 */
import type { TileStatus } from '../components/Tile.vue';
import { attr, byKind, distinct, incoming, label, outgoing, series } from '../model/graph';
import { normalizeHealth, type GraphDoc, type MetricsDoc, type Resource, type Series } from '../model/types';
import { latencyOf, uptimeOf } from './read';
import { isWithheld, statusOf } from './subject';

export interface CheckRow {
  id: string;
  name: string;
  resource: Resource | undefined;
  status: TileStatus;
  reason?: string;
  since?: string;
  latency?: Series;
  uptime: number | null;
  /** The URL or host:port; hidden when withheld. */
  target?: string;
  code?: number;
}

export type Cell = 'ok' | 'attn' | 'down' | 'maint' | 'unknown';

function rowOf(m: MetricsDoc | undefined, r: Resource): CheckRow {
  const h = normalizeHealth(r);
  const lat = latencyOf(m, r.id);
  let status = statusOf(r);
  if (r.state === 'paused') status = 'off';
  const row: CheckRow = { id: r.id, name: label(r), resource: r, status, uptime: uptimeOf(r, lat?.series) };
  if (h.reason) row.reason = h.reason;
  if (h.since) row.since = h.since;
  if (lat) row.latency = lat.series;
  const t = attr<string>(r, 'target');
  if (t && !isWithheld(r, 'target')) row.target = t;
  const code = attr<number>(r, 'status_code');
  if (typeof code === 'number') row.code = code;
  return row;
}

/** A Demo Home app's built-in check, from its `http.ms` series. */
function appRow(r: Resource, s: Series): CheckRow {
  const status: TileStatus = s.value === null && !s.stale ? 'down' : typeof s.value === 'number' && s.warn !== undefined && s.value >= s.warn ? 'attention' : 'ok';
  return { id: r.id, name: label(r), resource: r, status, latency: s, uptime: uptimeOf(undefined, s) };
}

export interface CheckScope {
  rows: CheckRow[];
  /** Uptime Kuma without monitor resources (the Demo Home): its counts. */
  kuma?: { up: Series | undefined; down: Series | undefined; total: number | null };
}

/**
 * The checks a widget shows: one check, an app's monitors (Uptime Kuma), an
 * integration instance's checks, or every check.
 */
export function checkScope(g: GraphDoc | undefined, m: MetricsDoc | undefined, scope: Resource | undefined): CheckScope {
  if (scope?.kind === 'check') return { rows: [rowOf(m, scope)] };
  if (scope?.kind === 'integration') return { rows: outgoing(g, scope.id, 'monitors').filter((r) => r.kind === 'check').map((r) => rowOf(m, r)) };
  if (scope) {
    const monitors = incoming(g, scope.id, 'part_of').filter((r) => r.kind === 'check');
    if (monitors.length) return { rows: monitors.sort((a, b) => label(a).localeCompare(label(b))).map((r) => rowOf(m, r)) };
    const up = series(m, scope.id, 'monitors.up');
    const down = series(m, scope.id, 'monitors.down');
    if (up || down) {
      const total = attr<number>(scope, 'monitors') ?? (up?.value != null ? up.value + (down?.value ?? 0) : null);
      return { rows: [], kuma: { up, down, total } };
    }
    return { rows: [] };
  }
  const checks = byKind(g, 'check');
  if (checks.length) return { rows: checks.map((r) => rowOf(m, r)) };
  return {
    rows: distinct(g)
      .filter((r) => r.kind === 'app')
      .flatMap((r) => {
        const s = series(m, r.id, 'http.ms');
        return s ? [appRow(r, s)] : [];
      })
      .sort((a, b) => a.name.localeCompare(b.name)),
  };
}

const RANK: Record<Cell, number> = { down: 4, attn: 3, maint: 2, ok: 1, unknown: 0 };

/** One row's history as cells: unknown before its first sample, down where it didn't answer. */
function rowCells(row: CheckRow, n: number): Cell[] {
  const h = row.latency?.history ?? [];
  const warn = row.latency?.warn;
  const first = h.findIndex((v) => typeof v === 'number');
  const step = h.length / n;
  const out: Cell[] = [];
  for (let i = 0; i < n; i++) {
    const from = Math.floor(i * step);
    const to = Math.max(from + 1, Math.floor((i + 1) * step));
    let cell: Cell = 'unknown';
    for (let j = from; j < to && j < h.length; j++) {
      const v = h[j];
      const c: Cell = first < 0 || j < first ? 'unknown' : v === null || v === undefined ? 'down' : warn !== undefined && v >= warn ? 'attn' : 'ok';
      if (RANK[c] > RANK[cell]) cell = c;
    }
    if (row.status === 'maintenance' && cell !== 'unknown') cell = 'maint';
    out.push(cell);
  }
  return out;
}

/** `n` hourly cells for a set of checks: the worst row in each slot. */
export function cells(scope: CheckScope, n = 24): Cell[] {
  if (scope.kuma) {
    const h = scope.kuma.down?.history ?? [];
    const step = h.length / n;
    return Array.from({ length: n }, (_, i) => {
      const slice = h.slice(Math.floor(i * step), Math.max(Math.floor(i * step) + 1, Math.floor((i + 1) * step)));
      if (!slice.length || slice.every((v) => v === null)) return 'unknown';
      return slice.some((v) => (v ?? 0) > 0) ? 'down' : 'ok';
    });
  }
  const per = scope.rows.filter((r) => r.status !== 'off').map((r) => rowCells(r, n));
  return Array.from({ length: n }, (_, i) => per.reduce<Cell>((w, cs) => (RANK[cs[i]!] > RANK[w] ? cs[i]! : w), 'unknown'));
}

export function cellsSummary(cs: readonly Cell[]): string {
  const known = cs.filter((c) => c !== 'unknown');
  if (!known.length) return 'No history yet';
  const down = cs.filter((c) => c === 'down').length;
  const slow = cs.filter((c) => c === 'attn').length;
  if (!down && !slow) return `Last ${cs.length} hours: all up`;
  return `Last ${cs.length} hours: ${down ? `${down} with an outage` : ''}${down && slow ? ', ' : ''}${slow ? `${slow} slow` : ''}`;
}

/** Up, total and uptime for the scope. Paused checks aren't counted. */
export function counts(scope: CheckScope): { up: number | null; total: number | null; down: number; uptime: number | null } {
  if (scope.kuma) {
    const h = (scope.kuma.down?.history ?? []).filter((v): v is number => typeof v === 'number');
    const uptime = h.length ? Math.round((h.filter((v) => v === 0).length / h.length) * 1000) / 10 : null;
    return { up: scope.kuma.up?.value ?? null, total: scope.kuma.total, down: scope.kuma.down?.value ?? 0, uptime };
  }
  const active = scope.rows.filter((r) => r.status !== 'off');
  const up = active.filter((r) => r.status === 'ok' || r.status === 'attention').length;
  const down = active.filter((r) => r.status === 'down').length;
  const ups = active.map((r) => r.uptime).filter((v): v is number => v !== null);
  const uptime = ups.length ? Math.round((ups.reduce((a, b) => a + b, 0) / ups.length) * 10) / 10 : null;
  return { up: active.length ? up : null, total: active.length || null, down, uptime };
}
