/**
 * The M0 widget catalog: what each widget type is called and which sizes it
 * declares (02 · Experience → Widget sizes). Components live in
 * `src/widgets/`; this file is data only, so the default-layout builder and
 * tests don't pull in Vue.
 *
 * A layout item has no config field, so a widget that shows one resource
 * carries it in its item id: `<type>@<resource id>` (resource ids never
 * contain "@"). Singletons use the bare type as their id.
 */
import type { SizeId } from '@chadash/layout';

export interface WidgetSpec {
  type: string;
  name: string;
  sizes: SizeId[];
  size: SizeId;
  /** Needs a target resource (`type@resource`). */
  bound?: boolean;
}

const spec = (type: string, name: string, size: SizeId, sizes: SizeId[], bound = false): WidgetSpec =>
  bound ? { type, name, size, sizes, bound } : { type, name, size, sizes };

export const WIDGETS: Record<string, WidgetSpec> = {
  fleet: spec('fleet', 'Fleet', 'large', ['wide', 'large', 'hero']),
  host: spec('host', 'Host', 'wide', ['small', 'wide'], true),
  backups: spec('backups', 'Backups', 'small', ['small', 'wide']),
  cameras: spec('cameras', 'Cameras', 'large', ['wide', 'large', 'hero']),
  homeassistant: spec('homeassistant', 'Home Assistant', 'wide', ['small', 'wide']),
  opnsense: spec('opnsense', 'OPNsense', 'wide', ['small', 'wide']),
  uptimekuma: spec('uptimekuma', 'Uptime Kuma', 'small', ['small', 'wide']),
  traefik: spec('traefik', 'Traefik', 'small', ['small', 'wide']),
  plex: spec('plex', 'Now playing', 'wide', ['small', 'wide']),
  arr: spec('arr', 'Queue', 'small', ['small', 'wide'], true),
  downloads: spec('downloads', 'Downloads', 'wide', ['small', 'wide']),
  calendar: spec('calendar', 'Up next', 'wide', ['wide', 'tall']),
  containers: spec('containers', 'Containers', 'small', ['small', 'wide', 'large']),
  incidents: spec('incidents', 'Incidents', 'large', ['wide', 'tall', 'large']),
  /** Any imported service without a catalog widget: `service@homepage:<section>/<entry>`. */
  service: spec('service', 'Service', 'small', ['small', 'wide'], true),
};

export function itemId(type: string, target?: string): string {
  return target ? `${type}@${target}` : type;
}

export function parseItemId(id: string): { type: string; target?: string } {
  const at = id.indexOf('@');
  if (at < 0) return { type: id };
  return { type: id.slice(0, at), target: id.slice(at + 1) };
}
