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
}

const spec = (type: string, name: string, size: SizeId, sizes: SizeId[], more: Partial<WidgetSpec> = {}): WidgetSpec => ({ type, name, size, sizes, ...more });

export const WIDGETS: Record<string, WidgetSpec> = {
  fleet: spec('fleet', 'Fleet', 'large', ['wide', 'tall', 'large', 'hero']),
  host: spec('host', 'Host', 'wide', ['glyph', 'strip', 'small', 'wide', 'large'], { bound: true, accepts: { kinds: ['host'] } }),
  guests: spec('guests', 'Guests', 'wide', ['wide', 'tall', 'large'], { accepts: { kinds: ['host', 'cluster'] } }),
  containers: spec('containers', 'Containers', 'small', ['small', 'wide', 'tall', 'large'], { accepts: { kinds: ['engine', 'host'] } }),
  unraid: spec('unraid', 'Unraid array', 'wide', ['glyph', 'strip', 'small', 'wide', 'tall', 'large', 'hero'], { accepts: { kinds: ['array', 'host'] } }),
  storage: spec('storage', 'Storage', 'small', ['glyph', 'strip', 'small', 'wide'], {
    bound: true,
    accepts: { kinds: ['storage', 'pool', 'share', 'backup_target', 'array', 'disk'] },
  }),
  backups: spec('backups', 'Backups', 'small', ['strip', 'small', 'wide', 'large'], { accepts: { kinds: ['backup_target'] } }),
  cameras: spec('cameras', 'Cameras', 'large', ['small', 'wide', 'large', 'hero'], { accepts: { kinds: ['app', 'camera'], types: ['frigate'] } }),
  homeassistant: spec('homeassistant', 'Home Assistant', 'wide', ['glyph', 'strip', 'small', 'wide', 'large'], {
    accepts: { kinds: ['app'], types: ['homeassistant'] },
  }),
  opnsense: spec('opnsense', 'OPNsense', 'wide', ['glyph', 'strip', 'small', 'wide', 'large'], { accepts: { kinds: ['router'] } }),
  uptimekuma: spec('uptimekuma', 'Uptime Kuma', 'small', ['small', 'wide'], { accepts: { kinds: ['app'], types: ['uptimekuma'] } }),
  traefik: spec('traefik', 'Traefik', 'small', ['small', 'wide'], { accepts: { kinds: ['app'], types: ['traefik'] } }),
  plex: spec('plex', 'Now playing', 'wide', ['glyph', 'strip', 'small', 'wide', 'large'], { accepts: { kinds: ['app'], types: ['plex', 'jellyfin', 'tautulli'] } }),
  arr: spec('arr', 'Queue', 'small', ['small', 'wide'], { bound: true, accepts: { kinds: ['app'], types: ['sonarr', 'radarr'] } }),
  downloads: spec('downloads', 'Downloads', 'wide', ['glyph', 'small', 'wide', 'tall'], { accepts: { kinds: ['app'], types: ['qbittorrent', 'sabnzbd'] } }),
  calendar: spec('calendar', 'Up next', 'wide', ['strip', 'wide', 'tall', 'large'], { accepts: { kinds: ['app'], types: ['sonarr', 'radarr'] } }),
  incidents: spec('incidents', 'Incidents', 'large', ['wide', 'tall', 'large']),
  /**
   * Any service: an imported Homepage entry (`service@homepage:<section>/<entry>`),
   * an app (`service@sonarr:<instance id>/app`, `service@app:sonarr`) or a check.
   */
  service: spec('service', 'Service', 'small', ['glyph', 'small', 'wide'], { bound: true, accepts: { kinds: ['app', 'check'] } }),
  /** One series: `metric@<resource id>#<metric key>`. */
  metric: spec('metric', 'Metric', 'small', ['glyph', 'strip', 'small', 'wide'], { bound: true }),
  check: spec('check', 'Uptime checks', 'small', ['glyph', 'strip', 'small', 'wide'], { accepts: { kinds: ['check', 'app', 'integration'], types: ['uptimekuma'] } }),
  bookmarks: spec('bookmarks', 'Bookmarks', 'small', ['glyph', 'small', 'wide', 'tall']),
  clock: spec('clock', 'Clock', 'small', ['glyph', 'strip', 'small', 'wide']),
  weather: spec('weather', 'Weather', 'small', ['glyph', 'strip', 'small', 'wide'], { accepts: { kinds: ['weather'] } }),
};

export function itemId(type: string, target?: string): string {
  return target ? `${type}@${target}` : type;
}

export function parseItemId(id: string): { type: string; target?: string } {
  const at = id.indexOf('@');
  if (at < 0) return { type: id };
  return { type: id.slice(0, at), target: id.slice(at + 1) };
}
