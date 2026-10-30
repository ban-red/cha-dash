/**
 * What the Home Graph can feed: for each widget in the catalog
 * (model/widgets.ts), whether it would have something to show, and for a
 * widget bound to one resource, which resources it can show. The widget
 * picker and the tile editor read the catalog through this, generically:
 *
 * - A widget bound to one resource (`bound`) lists candidates; the item id
 *   carries the one picked (`arr@sonarr:i_x/app`), as everywhere else.
 * - The generic `service` tile binds to whatever gives it a status: a check,
 *   an integration instance, or any resource (stores/tiles.ts → bind).
 * - Anything else is a singleton, available when the graph has its data.
 *
 * A few catalog types are fed by a different kind or app type than their
 * name says (`fleet` by hosts, `downloads` by qBittorrent or SABnzbd, `plex`
 * by Plex, Jellyfin or Tautulli, `storage` by anything with a fill level,
 * `metric` by series, `clock` by nothing at all); RULES says so.
 * Any other type, including ones added to the catalog later, is fed by
 * resources of its own kind, or by apps whose `attrs.type` is its name.
 */
import { DOWNLOAD_CLIENTS, type WidgetSpec } from './widgets';
import { appsOfType, byKind, calendar, label, placeOf, resolve, resources } from './graph';
import { unraidHostFor } from '../widgets/read';
import { resourceLinks, type GraphDoc, type Resource } from './types';

export type BindKind = 'none' | 'resource' | 'service';

export function bindKind(spec: Pick<WidgetSpec, 'type' | 'bound'>): BindKind {
  if (spec.type === 'service') return 'service';
  return spec.bound ? 'resource' : 'none';
}

export interface Feed {
  /** The graph has something for it to show. */
  available: boolean;
  /** For a widget bound to one resource: what it can show. */
  candidates: Resource[];
}

interface Rule {
  candidates?: (g: GraphDoc) => Resource[];
  available?: (g: GraphDoc, nowMs: number) => boolean;
}

const kind = (k: string) => (g: GraphDoc) => byKind(g, k).length > 0;
const app = (type: string) => (g: GraphDoc) => Boolean(resolve(g, `app:${type}`));

const anyOf =
  (...tests: ((g: GraphDoc) => boolean)[]) =>
  (g: GraphDoc) =>
    tests.some((t) => t(g));

/** Kinds with a fill level (`used.pct`). Unraid shares publish no usage, so they aren't offered. */
const STORAGE_KINDS = ['storage', 'pool', 'array', 'backup_target'];

/**
 * Series a metric tile can show, by the kinds that publish them (the picker
 * has the graph, not the metrics): host CPU and memory, the WAN's
 * throughput, fill levels, disk temperatures, house power.
 */
const METRICS_BY_KIND: Record<string, string[]> = {
  host: ['cpu.pct', 'mem.pct'],
  wan: ['rx.mbps', 'tx.mbps'],
  storage: ['used.pct'],
  pool: ['used.pct'],
  array: ['used.pct'],
  backup_target: ['used.pct'],
  disk: ['temp.c'],
};
const METRIC_WORDS: Record<string, string> = { 'cpu.pct': 'CPU', 'mem.pct': 'memory', 'rx.mbps': 'download', 'tx.mbps': 'upload', 'used.pct': 'used', 'temp.c': 'temperature', 'power.kw': 'power' };

function metricCandidates(g: GraphDoc): Resource[] {
  const out: Resource[] = [];
  for (const [k, keys] of Object.entries(METRICS_BY_KIND)) {
    for (const r of byKind(g, k)) for (const key of keys) out.push({ id: `${r.id}#${key}`, kind: 'metric', name: `${label(r)} · ${METRIC_WORDS[key] ?? key}` });
  }
  for (const r of appsOfType(g, 'homeassistant')) out.push({ id: `${r.id}#power.kw`, kind: 'metric', name: `${label(r)} · power` });
  return out;
}

const RULES: Record<string, Rule> = {
  fleet: { available: kind('host') },
  cameras: { available: kind('camera') },
  opnsense: { available: (g) => kind('router')(g) || app('opnsense')(g) },
  backups: { available: kind('backup_target') },
  containers: { available: anyOf(kind('container'), kind('engine')) },
  guests: { available: anyOf(kind('vm'), kind('lxc')) },
  unraid: { available: kind('array') },
  unraidhost: { available: (g) => unraidHostFor(g, undefined) !== undefined },
  storage: { candidates: (g) => STORAGE_KINDS.flatMap((k) => byKind(g, k)) },
  metric: { candidates: metricCandidates },
  check: { available: anyOf(kind('check'), app('uptimekuma')) },
  // Bookmarks lists the services the graph discovered (their links); Linkding and Karakeep are its sources.
  bookmarks: { available: anyOf(app('linkding'), app('karakeep'), (g) => resources(g).some((r) => r.kind === 'app' && resourceLinks(r).length > 0)) },
  plex: { available: anyOf(app('plex'), app('jellyfin'), app('tautulli')) },
  downloads: { available: anyOf(...DOWNLOAD_CLIENTS.map((t) => app(t))) },
  homeassistant: { available: app('homeassistant') },
  clock: { available: () => true },
  weather: { available: kind('weather') },
  calendar: { available: (g, now) => calendar(g, now - 7 * 86_400_000).length > 0 || ['sonarr', 'radarr', 'lidarr'].some((t) => appsOfType(g, t).length > 0) },
  agenda: { available: kind('calendar') },
  incidents: { available: () => true },
  arr: { candidates: (g) => [...appsOfType(g, 'sonarr'), ...appsOfType(g, 'radarr'), ...appsOfType(g, 'lidarr')] },
  service: { available: () => true },
};

function genericCandidates(g: GraphDoc, type: string): Resource[] {
  const seen = new Set<string>();
  return [...byKind(g, type), ...appsOfType(g, type)].filter((r) => !seen.has(r.id) && (seen.add(r.id), true));
}

/** What the graph can feed one catalog entry. Without a graph yet, singletons count as available. */
export function feedFor(spec: Pick<WidgetSpec, 'type' | 'bound'>, g: GraphDoc | undefined, nowMs = Date.now()): Feed {
  const rule = RULES[spec.type];
  const bind = bindKind(spec);
  if (bind === 'service') return { available: true, candidates: [] };
  if (!g) return { available: bind === 'none', candidates: [] };
  if (bind === 'resource') {
    const candidates = rule?.candidates ? rule.candidates(g) : genericCandidates(g, spec.type);
    return { available: candidates.length > 0, candidates };
  }
  const available = rule?.available ? rule.available(g, nowMs) : Boolean(resolve(g, `app:${spec.type}`)) || byKind(g, spec.type).length > 0;
  return { available, candidates: [] };
}

/** "Sonarr · on magpie": a candidate in a list. */
export function candidateLabel(g: GraphDoc | undefined, r: Resource): string {
  const place = placeOf(g, r.id);
  return place ? `${label(r)} · on ${place}` : label(r);
}

/**
 * What a service tile can take its status from, as `<select>` groups. The
 * value encodes the binding: `check:<name>`, `instance:<id>` or
 * `resource:<id>`; '' is none.
 */
export interface BindingChoice {
  value: string;
  label: string;
}

export interface BindingGroup {
  label: string;
  choices: BindingChoice[];
}

const SERVICE_KINDS = new Set(['app', 'container', 'vm', 'lxc', 'host']);

export function serviceBindingGroups(g: GraphDoc | undefined): BindingGroup[] {
  const all = resources(g);
  const sorted = (rs: Resource[]) => [...rs].sort((a, b) => label(a).localeCompare(label(b)));
  const groups: BindingGroup[] = [
    { label: 'Checks', choices: sorted(all.filter((r) => r.kind === 'check')).map((r) => ({ value: `check:${label(r)}`, label: label(r) })) },
    {
      label: 'Integrations',
      choices: sorted(all.filter((r) => r.kind === 'integration' && r.id.startsWith('integration:'))).map((r) => ({
        value: `instance:${r.id.slice('integration:'.length)}`,
        label: label(r),
      })),
    },
    { label: 'Services and containers', choices: sorted(all.filter((r) => SERVICE_KINDS.has(r.kind))).map((r) => ({ value: `resource:${r.id}`, label: candidateLabel(g, r) })) },
  ];
  return groups.filter((gr) => gr.choices.length > 0);
}

export function encodeBinding(b: { instance?: string; check?: string; resource?: string } | undefined): string {
  if (b?.check) return `check:${b.check}`;
  if (b?.instance) return `instance:${b.instance}`;
  if (b?.resource) return `resource:${b.resource}`;
  return '';
}

export function decodeBinding(value: string): { instance?: string; check?: string; resource?: string } | undefined {
  const at = value.indexOf(':');
  if (at < 0) return undefined;
  const kind = value.slice(0, at);
  const id = value.slice(at + 1);
  if (!id) return undefined;
  if (kind === 'check') return { check: id };
  if (kind === 'instance') return { instance: id };
  if (kind === 'resource') return { resource: id };
  return undefined;
}
