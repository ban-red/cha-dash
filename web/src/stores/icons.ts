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
 */
import { computed, shallowRef } from 'vue';
import { api } from '../lib/api';
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
export function wantsIcon(cfg: TileConfig | undefined, href: string | undefined): boolean {
  return !!(cfg?.icon || href || cfg?.bind?.instance);
}

/** The cached icon for a tile: its icon URL, else its link's site, else its instance's. */
export function iconFor(m: IconMap, cfg: TileConfig | undefined, href: string | undefined): string | undefined {
  if (cfg?.icon && m.by_icon[cfg.icon]) return m.by_icon[cfg.icon];
  const o = originOf(href);
  if (o && m.by_origin[o]) return m.by_origin[o];
  const inst = cfg?.bind?.instance;
  return inst ? m.by_instance[inst] : undefined;
}
