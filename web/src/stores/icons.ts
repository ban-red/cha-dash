/**
 * Icons the hub cached for tiles (internal/icons). The CSP keeps tiles from
 * loading images from other origins, so the hub fetches each icon itself and
 * serves it same-origin:
 *
 *   GET /api/icons → {by_icon: {<tile icon URL>: path}, by_origin: {<origin>: path},
 *                     by_instance: {<instance id>: path}}
 *
 * where a path is /api/icons/<id>?v=<etag>. Only fetched icons are listed;
 * anything else keeps its monogram, which also stands in when an image
 * fails to load. The front page's board loads the map once, when a tile has
 * something to look it up by (BoardSurface.vue); a tile never waits for it.
 *
 * A tile bound to an instance with no cached icon falls back to the bundled
 * logo of its integration, and any tile whose name says a service ("Sonarr",
 * "Plex Media Server") to that service's logo (lib/serviceIcons.ts).
 */
import { computed, shallowRef } from 'vue';
import { api } from '../lib/api';
import { serviceIconByName, serviceIconUrl } from '../lib/serviceIcons';
import { catalogEntry, integrations } from './integrations';
import type { TileConfig } from './tiles';

export interface IconMap {
  by_icon: Record<string, string>;
  by_origin: Record<string, string>;
  by_instance: Record<string, string>;
}

const EMPTY: IconMap = { by_icon: {}, by_origin: {}, by_instance: {} };
const PATH = /^\/api\/icons\/ic_[a-z0-9]{8,64}(\?v=[0-9a-f]{1,64})?$/;

const map = shallowRef<IconMap>(EMPTY);
let loading: Promise<void> | undefined;

/** Only same-origin icon paths the hub hands out; anything else is dropped. */
function clean(v: unknown): Record<string, string> {
  const out: Record<string, string> = {};
  if (v && typeof v === 'object') for (const [k, p] of Object.entries(v)) if (typeof p === 'string' && PATH.test(p)) out[k] = p;
  return out;
}

/** The icon map: empty until loadIcons has run. */
export const iconMap = computed(() => map.value);

/** Load the map, once. The board does it when a tile could use an icon. */
export function loadIcons(): Promise<void> {
  loading ??= api<Partial<IconMap>>('/api/icons')
    .then((m) => {
      map.value = { by_icon: clean(m?.by_icon), by_origin: clean(m?.by_origin), by_instance: clean(m?.by_instance) };
    })
    .catch(() => {
      map.value = EMPTY; // an older hub, or offline: monograms
    });
  return loading;
}

/** For tests: forget the loaded map. */
export function resetIcons(): void {
  loading = undefined;
  map.value = EMPTY;
}

function originOf(href: string | undefined): string | undefined {
  if (!href) return undefined;
  try {
    const u = new URL(href);
    return u.protocol === 'http:' || u.protocol === 'https:' ? u.origin : undefined;
  } catch {
    return undefined;
  }
}

/** Does a tile have anything an icon could be found by? */
export function wantsIcon(cfg: TileConfig | undefined, href: string | undefined, name?: string): boolean {
  return !!(cfg?.icon || href || cfg?.bind?.instance || serviceIconByName(name));
}

/** The cached icon for a tile: its icon URL, else its link's site, else a bundled logo from its name, else its instance's (cached, then bundled). */
export function iconFor(m: IconMap, cfg: TileConfig | undefined, href: string | undefined, name?: string): string | undefined {
  if (cfg?.icon && m.by_icon[cfg.icon]) return m.by_icon[cfg.icon];
  const o = originOf(href);
  if (o && m.by_origin[o]) return m.by_origin[o];
  // A name that says a service beats the instance it's bound to: a "Sonarr"
  // tile on the Unraid instance is Sonarr, not Unraid.
  const named = serviceIconByName(name);
  if (named) return named;
  const inst = cfg?.bind?.instance;
  return inst ? (m.by_instance[inst] ?? bundledFor(inst)) : undefined;
}

/** The bundled logo for an instance's integration: the manifest's icon slug, else its id. */
function bundledFor(instance: string): string | undefined {
  const id = integrations.instances.find((i) => i.id === instance)?.integration;
  return id ? serviceIconUrl(catalogEntry(id)?.icon || id) : undefined;
}
