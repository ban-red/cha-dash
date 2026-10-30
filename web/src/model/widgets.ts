/**
 * The widget catalog: what each widget type is called and which sizes it
 * declares (docs/design/workbench.md → Coverage at a glance; 02 · Experience →
 * Widget sizes). Components live in `src/widgets/`; this file is data only,
 * so the default-layout builder and tests don't pull in Vue.
 *
 * A layout item has no config field, so a widget that shows one resource
 * carries it in its item id: `<type>@<resource id>` (resource ids never
 * contain "@"). Singletons use the bare type as their id. A few widgets take
 * an optional target that narrows them (`containers@docker:<instance>/engine`,
 * `guests@<host>`); `bound` is only for those that need one.
 */
import type { SizeId } from '@chadash/layout';

export interface WidgetSpec {
  type: string;
  name: string;
  sizes: SizeId[];
  size: SizeId;
  /** Needs a target resource (`type@resource`). */
  bound?: boolean;
  /**
   * What a target can be, for the widget picker: resource kinds, and for
   * `app`, integration types (`attrs.type`). Optional when `bound` isn't set.
   * `metric` targets a series (`<resource id>#<metric key>`), `clock` an IANA
   * time zone and `bookmarks` a group name.
   */
  accepts?: { kinds: string[]; types?: string[] };
  /** Where the widget picker files it. */
  group: WidgetGroup;
  /** What it shows, in one short sentence, for the picker. */
  about: string;
  /** A bundled service icon slug for the picker (lib/serviceIcons.ts); else a monogram. */
  icon?: string;
  /** Settings of its own, edited in Widget settings and kept in the tile's `options` (widgets/options.ts). */
  options?: WidgetOption[];
}

/**
 * One setting a widget offers in Widget settings. Its value lives in the
 * tile config's `options[key]`; a missing or malformed value is the default.
 *
 * - `boolean`: a checkbox.
 * - `choice`: a select of string values.
 * - `resources`: which resources of a kind to show, in the graph's order;
 *   none picked means all. Limited to those the tile's target relates to
 *   with `via` when the target is one.
 */
export type WidgetOption = { key: string; label: string; help?: string } & (
  | { kind: 'boolean'; default: boolean }
  | { kind: 'choice'; default: string; choices: { value: string; label: string }[] }
  | { kind: 'resources'; resourceKind: string; via?: string }
);

/** The widget picker's groups, in the order it shows them. */
export const WIDGET_GROUPS = [
  { id: 'servers', name: 'Servers' },
  { id: 'network', name: 'Network' },
  { id: 'media', name: 'Media' },
  { id: 'home', name: 'Home' },
  { id: 'basics', name: 'Basics' },
] as const;
export type WidgetGroup = (typeof WIDGET_GROUPS)[number]['id'];

type More = Pick<WidgetSpec, 'group' | 'about'> & Partial<WidgetSpec>;
const spec = (type: string, name: string, size: SizeId, sizes: SizeId[], more: More): WidgetSpec => ({ type, name, size, sizes, ...more });

/** Usenet clients publish SABnzbd's keys, torrent clients qBittorrent's (integrations/<id>/README.md). */
export const USENET_CLIENTS: readonly string[] = ['sabnzbd', 'nzbget'];
/** Every download client the Downloads tile reads, in the order it picks one. */
export const DOWNLOAD_CLIENTS: readonly string[] = ['qbittorrent', 'transmission', 'sabnzbd', 'nzbget'];

export const WIDGETS: Record<string, WidgetSpec> = {
  fleet: spec('fleet', 'Fleet', 'large', ['wide', 'panel', 'tall', 'column', 'large', 'hero'], { group: 'servers', about: 'Every host in one table: CPU, memory and guests.' }),
  host: spec('host', 'Host', 'wide', ['glyph', 'strip', 'small', 'wide', 'panel', 'large'], { bound: true, accepts: { kinds: ['host'] }, group: 'servers', about: 'One host in detail, with memory against its warning line.' }),
  guests: spec('guests', 'Guests', 'wide', ['wide', 'panel', 'tall', 'column', 'large'], { accepts: { kinds: ['host', 'cluster'] }, group: 'servers', about: 'VMs and LXCs, problems first, the ones stopped on purpose dimmed.', icon: 'proxmox' }),
  containers: spec('containers', 'Containers', 'small', ['small', 'wide', 'panel', 'tall', 'column', 'large'], { accepts: { kinds: ['engine', 'host'] }, group: 'servers', about: 'Docker containers on an engine or host; problems rise to the top.', icon: 'docker' }),
  unraid: spec('unraid', 'Unraid array', 'wide', ['glyph', 'strip', 'small', 'wide', 'panel', 'tall', 'column', 'large', 'hero'], { accepts: { kinds: ['array', 'host'] }, group: 'servers', about: 'The Unraid array, from its fill to a map of every disk.', icon: 'unraid' }),
  unraidhost: spec('unraidhost', 'Unraid server', 'wide', ['glyph', 'strip', 'small', 'wide', 'panel', 'tall', 'column', 'large'], { accepts: { kinds: ['host', 'array'] }, group: 'servers', about: 'The whole Unraid server: CPU, memory, array, parity, containers and VMs.', icon: 'unraid' }),
  storage: spec('storage', 'Storage', 'small', ['glyph', 'strip', 'small', 'wide'], {
    group: 'servers', about: 'How full one pool, datastore or share is.',
    bound: true,
    accepts: { kinds: ['storage', 'pool', 'share', 'backup_target', 'array', 'disk'] },
  }),
  backups: spec('backups', 'Backups', 'small', ['strip', 'small', 'wide', 'large'], { accepts: { kinds: ['backup_target'] }, group: 'servers', about: 'A backup datastore’s fill and how the nightly runs went.' }),
  cameras: spec('cameras', 'Cameras', 'large', ['small', 'wide', 'panel', 'large', 'hero'], {
    accepts: { kinds: ['app', 'camera'], types: ['frigate'] }, group: 'home', about: 'Frigate’s cameras: their pictures, last detections and controls.', icon: 'frigate',
    options: [
      { key: 'cameras', kind: 'resources', resourceKind: 'camera', via: 'exposes', label: 'Cameras', help: 'Which cameras the tile shows. None picked shows them all.' },
      { key: 'pictures', kind: 'boolean', default: true, label: 'Show pictures', help: 'When the Frigate connection shares them. Off draws a placeholder instead.' },
      {
        key: 'refresh', kind: 'choice', default: '10', label: 'Refresh pictures every',
        choices: [{ value: '2', label: '2 seconds' }, { value: '5', label: '5 seconds' }, { value: '10', label: '10 seconds' }, { value: '30', label: '30 seconds' }, { value: '60', label: 'Minute' }],
        help: 'Only while the tile is on screen.',
      },
      { key: 'detections', kind: 'boolean', default: true, label: 'List recent detections', help: 'On the larger sizes.' },
      { key: 'controls', kind: 'boolean', default: true, label: 'Show camera controls', help: 'Detection, recording and more for the main camera, for roles that may use them.' },
    ],
  }),
  homeassistant: spec('homeassistant', 'Home Assistant', 'wide', ['glyph', 'strip', 'small', 'wide', 'large'], {
    group: 'home', about: 'Who’s home, lights and switches, and house power.', icon: 'homeassistant',
    accepts: { kinds: ['app'], types: ['homeassistant'] },
  }),
  opnsense: spec('opnsense', 'OPNsense', 'wide', ['glyph', 'strip', 'small', 'wide', 'large'], { accepts: { kinds: ['router'] }, group: 'network', about: 'The router and its WAN: states, throughput and what’s down.', icon: 'opnsense' }),
  uptimekuma: spec('uptimekuma', 'Uptime Kuma', 'small', ['small', 'wide'], { accepts: { kinds: ['app'], types: ['uptimekuma'] }, group: 'network', about: 'Monitors up out of total, and the last 24 h as cells.', icon: 'uptimekuma' }),
  traefik: spec('traefik', 'Traefik', 'small', ['small', 'wide'], { accepts: { kinds: ['app'], types: ['traefik'] }, group: 'network', about: 'Routers, services and middlewares, with the routes behind them.', icon: 'traefik' }),
  plex: spec('plex', 'Now playing', 'wide', ['glyph', 'strip', 'small', 'wide', 'large'], { accepts: { kinds: ['app'], types: ['plex', 'jellyfin', 'tautulli'] }, group: 'media', about: 'What’s streaming on Plex, Jellyfin or Tautulli.', icon: 'plex' }),
  arr: spec('arr', 'Queue', 'small', ['small', 'wide'], { bound: true, accepts: { kinds: ['app'], types: ['sonarr', 'radarr', 'lidarr'] }, group: 'media', about: 'A Sonarr, Radarr or Lidarr queue, and what’s wanted.' }),
  downloads: spec('downloads', 'Downloads', 'wide', ['glyph', 'small', 'wide', 'panel', 'tall', 'column', 'large'], { accepts: { kinds: ['app'], types: [...DOWNLOAD_CLIENTS] }, group: 'media', about: 'Download speed and queue from your torrent or Usenet client.' }),
  calendar: spec('calendar', 'Up next', 'wide', ['strip', 'wide', 'panel', 'tall', 'column', 'large'], { accepts: { kinds: ['app'], types: ['sonarr', 'radarr', 'lidarr'] }, group: 'media', about: 'What Sonarr and Radarr expect next.' }),
  incidents: spec('incidents', 'Incidents', 'large', ['wide', 'panel', 'tall', 'column', 'large'], { group: 'basics', about: 'Everything open, problems first; the latest fix when it’s quiet.' }),
  /**
   * Any service: an imported Homepage entry (`service@homepage:<section>/<entry>`),
   * an app (`service@sonarr:<instance id>/app`, `service@app:sonarr`) or a check.
   */
  service: spec('service', 'Service', 'small', ['glyph', 'small', 'wide'], { bound: true, accepts: { kinds: ['app', 'check'] }, group: 'network', about: 'Any service: a name, a link, and a status from what it’s bound to.' }),
  /** One series: `metric@<resource id>#<metric key>`. */
  metric: spec('metric', 'Metric', 'small', ['glyph', 'strip', 'small', 'wide'], { bound: true, group: 'servers', about: 'One number with its unit, normal band and trend.' }),
  check: spec('check', 'Uptime checks', 'small', ['glyph', 'strip', 'small', 'wide'], { accepts: { kinds: ['check', 'app', 'integration'], types: ['uptimekuma'] }, group: 'network', about: 'The built-in checks and Uptime Kuma monitors, up or down.' }),
  bookmarks: spec('bookmarks', 'Bookmarks', 'small', ['glyph', 'small', 'wide', 'panel', 'tall', 'column'], { group: 'basics', about: 'Links to the services Cha Dash found, with their state.' }),
  clock: spec('clock', 'Clock', 'small', ['glyph', 'strip', 'small', 'wide'], { group: 'basics', about: 'The time, on the dot-matrix face.' }),
  weather: spec('weather', 'Weather', 'small', ['glyph', 'strip', 'small', 'wide', 'panel', 'tall', 'large'], { accepts: { kinds: ['weather'] }, group: 'home', about: 'Now, the next hours and five days, with the sun, wind and rain.' }),
};

export function itemId(type: string, target?: string): string {
  return target ? `${type}@${target}` : type;
}

export function parseItemId(id: string): { type: string; target?: string } {
  const at = id.indexOf('@');
  if (at < 0) return { type: id };
  return { type: id.slice(0, at), target: id.slice(at + 1) };
}
