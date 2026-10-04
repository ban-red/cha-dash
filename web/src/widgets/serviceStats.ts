/**
 * The service tile's live stat per integration (docs/design/workbench.md →
 * Service tile), read generically from the app resource's metrics and attrs
 * by `attrs.type`, so one table covers the Tier 0 integrations and the specs
 * alike. Keys are the ones each source publishes (integrations/<id>/README.md,
 * specs/README.md), which follow the Demo Home's (sim/snapshots).
 *
 * | Type | Primary | Facts |
 * |---|---|---|
 * | sonarr, radarr | `queue` (Radarr in the Demo Home: `queued`) | `wanted`; next release from `attrs.calendar` |
 * | prowlarr | `grabs` | `queries.24h`, `failures.24h` |
 * | bazarr | `wanted` (else `missing.episodes` + `missing.movies`) | episodes, movies; the trend |
 * | traefik | `attrs.routers` | `attrs.services`, `attrs.middlewares` |
 * | uptimekuma | `monitors.up` of `attrs.monitors` | uptime 24 h, `monitors.down` |
 * | immich | `photos` | `videos`, `storage.gib` |
 * | mealie | `recipes` (metric, else attr) | `attrs.categories`, `attrs.tags` |
 * | linkding, karakeep | `bookmarks` | `attrs.archived`/`attrs.favorites`, `attrs.tags` |
 * | tautulli | `streams` | `streams.transcode`, `bandwidth.mbps` |
 * | plex, jellyfin | `streams` | `attrs.movies`, `attrs.shows` |
 * | qbittorrent | `dl.mbps` | `leeching`, `seeding` |
 * | sabnzbd | `dl.mbps` | `queue`, `left.gib` |
 * | pihole, adguard | `blocked.pct` | `blocklist` (the context line), `queries.24h` |
 * | homeassistant | `people_home` of `attrs.people_total` | `lights_on`, `power.kw` |
 * | frigate | `attrs.cameras` | `inference.ms` |
 * | pbs | `tasks.failed_24h` | datastores |
 * | seerr | `requests.pending` | `requests.processing`, `requests.available` |
 * | a plain URL (`check`) | `attrs.status_code` (else latency) | latency, uptime 24 h |
 * | anything else | its first series that isn't a latency | the check's latency |
 */
import { fmtInt, fmtNum, hhmm } from '../lib/format';
import { attr, byKind, incoming, series } from '../model/graph';
import type { GraphDoc, MetricsDoc, Resource, Series } from '../model/types';
import { LATENCY_KEYS, fmtUptime, latencyOf, metricWords, nextRelease, trendOf, typeOf, unitOf, uptimeOf } from './read';

export interface Fact {
  value: string;
  label: string;
}

export interface Reading {
  /** The primary value; null when unknown (shown as "—", never 0). */
  value: number | null;
  text: string;
  /** The unit or noun after the value: "queued", "/9 up", "photos". */
  unit: string;
  /** The metric key or attr it came from, for permission checks. */
  key: string;
  /** The primary series, for the sparkline and its freshness. */
  series?: Series;
  /** Secondary facts, most useful first. */
  facts: Fact[];
  /** Set when the source works and has nothing to show: "Queue clear". */
  empty?: string;
  /** A trend or what's next: "down 40 in 24 h", "next: Black Clover 04:00". */
  context?: string;
}

interface Ctx {
  g: GraphDoc | undefined;
  m: MetricsDoc | undefined;
  r: Resource;
  now: number;
}

const first = (c: Ctx, keys: readonly string[]) => {
  for (const key of keys) {
    const s = series(c.m, c.r.id, key);
    if (s) return { key, s };
  }
  return undefined;
};

/** A fact from a metric ("25 wanted"), or nothing when the source doesn't publish it. */
function mf(c: Ctx, key: string, words: string, fmt: (v: number | null) => string = (v) => fmtNum(v)): Fact[] {
  const s = series(c.m, c.r.id, key);
  return s ? [{ value: fmt(s.value), label: words }] : [];
}
/** A fact from an attr. */
function af(c: Ctx, key: string, words: string): Fact[] {
  const v = attr<number>(c.r, key);
  return typeof v === 'number' ? [{ value: fmtNum(v), label: words }] : [];
}

function metric(c: Ctx, keys: readonly string[], unit: string, more: Partial<Reading> = {}, emptyWhenZero?: string): Reading {
  const hit = first(c, keys);
  const v = hit?.s.value ?? null;
  const out: Reading = { value: v, text: fmtNum(v), unit, key: hit?.key ?? keys[0]!, facts: [], ...more };
  if (hit) out.series = hit.s;
  if (emptyWhenZero && v === 0) out.empty = emptyWhenZero;
  return out;
}

function attrStat(c: Ctx, key: string, unit: string, more: Partial<Reading> = {}, emptyWhenZero?: string): Reading {
  const v = attr<number>(c.r, key);
  const value = typeof v === 'number' ? v : null;
  const out: Reading = { value, text: fmtNum(value), unit, key, facts: [], ...more };
  if (emptyWhenZero && value === 0) out.empty = emptyWhenZero;
  return out;
}

function arr(c: Ctx): Reading {
  const next = nextRelease(c.r, c.now);
  const more: Partial<Reading> = { facts: mf(c, 'wanted', 'wanted') };
  if (next) more.context = `next: ${next.title} ${hhmm(next.at)}`;
  return metric(c, ['queue', 'queued'], 'queued', more, 'Queue clear');
}

/** Bazarr: `wanted` (the spec's sum), else the Demo Home's two badges added up. */
function bazarr(c: Ctx): Reading {
  const eps = series(c.m, c.r.id, 'missing.episodes');
  const movies = series(c.m, c.r.id, 'missing.movies');
  const facts = [...mf(c, 'missing.episodes', 'episodes'), ...mf(c, 'missing.movies', 'movies')];
  let wanted = series(c.m, c.r.id, 'wanted');
  if (!wanted && (eps || movies)) {
    const a = eps?.history ?? [];
    const b = movies?.history ?? [];
    const n = Math.max(a.length, b.length);
    const history = Array.from({ length: n }, (_, i) => {
      const x = a[i] ?? null;
      const y = b[i] ?? null;
      return x === null && y === null ? null : (x ?? 0) + (y ?? 0);
    });
    const value = eps?.value == null && movies?.value == null ? null : (eps?.value ?? 0) + (movies?.value ?? 0);
    wanted = { value, history, unit: 'count' };
    const asOf = eps?.as_of ?? movies?.as_of;
    if (asOf) wanted.as_of = asOf;
    if (eps?.stale || movies?.stale) wanted.stale = true;
  }
  const v = wanted?.value ?? null;
  const out: Reading = { value: v, text: fmtNum(v), unit: 'subtitles missing', key: 'wanted', facts };
  if (wanted) out.series = wanted;
  const trend = trendOf(wanted, c.m?.history);
  if (trend) out.context = trend;
  if (v === 0) out.empty = 'No subtitles missing';
  return out;
}

function uptimekuma(c: Ctx): Reading {
  const up = series(c.m, c.r.id, 'monitors.up');
  const down = series(c.m, c.r.id, 'monitors.down');
  const total = attr<number>(c.r, 'monitors') ?? (up?.value != null ? up.value + (down?.value ?? 0) : null);
  const monitors = incoming(c.g, c.r.id, 'part_of').filter((r) => r.kind === 'check');
  let uptime: number | null = null;
  const own = monitors.map((r) => attr<number>(r, 'uptime_24h')).filter((v): v is number => typeof v === 'number');
  if (own.length) uptime = Math.round((own.reduce((a, b) => a + b, 0) / own.length) * 10) / 10;
  else {
    const h = (down?.history ?? []).filter((v): v is number => typeof v === 'number');
    if (h.length) uptime = Math.round((h.filter((v) => v === 0).length / h.length) * 1000) / 10;
  }
  const v = up?.value ?? null;
  const out: Reading = {
    value: v,
    text: fmtInt(v),
    unit: `/${fmtInt(total)} up`,
    key: 'monitors.up',
    facts: [{ value: `${fmtUptime(uptime)}%`, label: 'all up · 24 h' }, ...mf(c, 'monitors.down', 'down', fmtInt)],
  };
  if (up) out.series = up;
  if (total === 0) out.empty = 'No monitors on this status page';
  return out;
}

const media = (c: Ctx) =>
  metric(c, ['streams'], 'playing', { facts: [...af(c, 'movies', 'movies'), ...af(c, 'shows', 'shows')] }, 'Nothing playing');

/** Pi-hole and AdGuard Home publish the same keys: blocked %, with the blocklist as context. */
function dnsFilter(c: Ctx): Reading {
  const list = series(c.m, c.r.id, 'blocklist')?.value;
  const more: Partial<Reading> = { facts: [...mf(c, 'queries.24h', 'queries · 24 h'), ...mf(c, 'blocked.24h', 'blocked · 24 h'), ...mf(c, 'clients', 'clients', fmtInt)] };
  if (typeof list === 'number') more.context = `${fmtNum(list)} domains on the blocklist`;
  const r = metric(c, ['blocked.pct'], '% blocked', more);
  r.text = r.value === null ? '—' : r.value.toFixed(1);
  return r;
}

const bookmarks = (archived: string) => (c: Ctx) =>
  metric(c, ['bookmarks'], 'bookmarks', { facts: [...af(c, archived, archived), ...af(c, 'tags', 'tags')] }, 'No bookmarks yet');

const READERS: Record<string, (c: Ctx) => Reading> = {
  sonarr: arr,
  radarr: arr,
  prowlarr: (c) => metric(c, ['grabs'], 'grabs', { facts: [...mf(c, 'queries.24h', 'queries · 24 h'), ...mf(c, 'failures.24h', 'failures · 24 h', fmtInt)] }),
  bazarr,
  traefik: (c) => attrStat(c, 'routers', 'routers', { facts: [...af(c, 'services', 'services'), ...af(c, 'middlewares', 'middlewares')] }),
  uptimekuma,
  immich: (c) =>
    metric(c, ['photos'], 'photos', { facts: [...mf(c, 'videos', 'videos'), ...mf(c, 'storage.gib', 'GiB', (v) => (v === null ? '—' : v.toFixed(1)))] }, 'No photos yet'),
  mealie: (c) => {
    const facts = [...af(c, 'categories', 'categories'), ...af(c, 'tags', 'tags')];
    return series(c.m, c.r.id, 'recipes') ? metric(c, ['recipes'], 'recipes', { facts }, 'No recipes yet') : attrStat(c, 'recipes', 'recipes', { facts }, 'No recipes yet');
  },
  linkding: bookmarks('archived'),
  karakeep: bookmarks('favorites'),
  tautulli: (c) =>
    metric(c, ['streams'], 'playing', { facts: [...mf(c, 'streams.transcode', 'transcoding', fmtInt), ...mf(c, 'bandwidth.mbps', 'Mb/s')] }, 'Nothing playing'),
  plex: media,
  jellyfin: media,
  sabnzbd: (c) => {
    const r = metric(c, ['dl.mbps'], 'Mb/s ↓', { facts: [...mf(c, 'queue', 'in the queue', fmtInt), ...mf(c, 'left.gib', 'GiB left')] });
    if (series(c.m, c.r.id, 'queue')?.value === 0) r.empty = 'Queue clear';
    return r;
  },
  pihole: dnsFilter,
  adguard: dnsFilter,
  qbittorrent: (c) => {
    const r = metric(c, ['dl.mbps'], 'Mb/s ↓', { facts: [...mf(c, 'leeching', 'leeching', fmtInt), ...mf(c, 'seeding', 'seeding', fmtInt)] });
    const leech = series(c.m, c.r.id, 'leeching')?.value;
    if (leech === 0 && (r.value ?? 0) < 0.1) r.empty = 'Idle';
    return r;
  },
  homeassistant: (c) =>
    metric(c, ['people_home'], `/${fmtInt(attr<number>(c.r, 'people_total'))} home`, { facts: [...mf(c, 'lights_on', 'lights on', fmtInt), ...mf(c, 'power.kw', 'kW')] }),
  frigate: (c) => attrStat(c, 'cameras', 'cameras', { facts: mf(c, 'inference.ms', 'ms inference') }),
  pbs: (c) => {
    const stores = byKind(c.g, 'backup_target').filter((d) => d.id.startsWith(c.r.id.replace(/\/app$/, '/')) || incoming(c.g, c.r.id, 'part_of').includes(d));
    return metric(c, ['tasks.failed_24h'], 'failed · 24 h', { facts: stores.length ? [{ value: String(stores.length), label: stores.length === 1 ? 'datastore' : 'datastores' }] : [] }, 'No failed tasks');
  },
  seerr: (c) =>
    metric(c, ['requests.pending'], 'pending', { facts: [...mf(c, 'requests.processing', 'processing', fmtInt), ...mf(c, 'requests.available', 'available', fmtInt)] }, 'No pending requests'),
};

/** A plain URL's built-in check (or an Uptime Kuma monitor): status code, latency, uptime. */
function check(c: Ctx): Reading {
  const lat = latencyOf(c.m, c.r.id);
  const code = attr<number>(c.r, 'status_code');
  const uptime = uptimeOf(c.r, lat?.series);
  const latency: Fact[] = lat ? [{ value: fmtInt(lat.series.value), label: 'ms' }] : [];
  const up: Fact[] = [{ value: `${fmtUptime(uptime)}%`, label: 'up · 24 h' }];
  if (typeof code === 'number') {
    const out: Reading = { value: code, text: String(code), unit: 'HTTP', key: 'status_code', facts: [...latency, ...up] };
    if (lat) out.series = lat.series;
    return out;
  }
  const v = lat?.series.value ?? null;
  const out: Reading = { value: v, text: fmtInt(v), unit: 'ms', key: lat?.key ?? 'http.ms', facts: up };
  if (lat) out.series = lat.series;
  return out;
}

/** Anything else: its first series that isn't a check's latency, else the latency. */
function fallback(c: Ctx): Reading {
  const prefix = `${c.r.id}#`;
  const keys = Object.keys(c.m?.series ?? {})
    .filter((k) => k.startsWith(prefix))
    .map((k) => k.slice(prefix.length))
    .sort();
  const stat = keys.find((k) => !(LATENCY_KEYS as readonly string[]).includes(k));
  if (stat) {
    const s = series(c.m, c.r.id, stat)!;
    const unit = unitOf(s, stat);
    return { value: s.value, text: fmtNum(s.value), unit: unit || metricWords(stat).toLowerCase(), key: stat, series: s, facts: [] };
  }
  return check(c);
}

export function readService(g: GraphDoc | undefined, m: MetricsDoc | undefined, r: Resource, now: number): Reading {
  const c: Ctx = { g, m, r, now };
  if (r.kind === 'check') return check(c);
  const type = typeOf(r);
  const reader = type ? READERS[type] : undefined;
  return reader ? reader(c) : fallback(c);
}

/** Display names of the integrations, for a tile's context when the instance has its own name. */
export const TYPE_NAMES: Record<string, string> = {
  sonarr: 'Sonarr',
  radarr: 'Radarr',
  prowlarr: 'Prowlarr',
  bazarr: 'Bazarr',
  traefik: 'Traefik',
  uptimekuma: 'Uptime Kuma',
  immich: 'Immich',
  mealie: 'Mealie',
  linkding: 'Linkding',
  karakeep: 'Karakeep',
  tautulli: 'Tautulli',
  plex: 'Plex',
  jellyfin: 'Jellyfin',
  qbittorrent: 'qBittorrent',
  sabnzbd: 'SABnzbd',
  pihole: 'Pi-hole',
  adguard: 'AdGuard Home',
  homeassistant: 'Home Assistant',
  frigate: 'Frigate',
  pbs: 'PBS',
  seerr: 'Seerr',
};

