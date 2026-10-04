/**
 * Tile configuration: what a board item shows that its layout document can't
 * carry (name, icon, link, description, chromeless, what it's bound to,
 * display options, where it came from). It lives on the hub next to each
 * layout revision and arrives with the layout (stores/board.ts):
 *
 *   GET /api/boards/{id}/layout → {revision, format_version, layout,
 *     tiles: {"<item id>": {name, icon, href, description, chromeless,
 *       bind: {instance, check, resource, sources: [{instance, capability, options}]},
 *       options, origin: {source, file, group, entry}}}}
 *   PUT /api/boards/{id}/layout  {revision, format_version, layout, tiles?}
 *
 * This module is the one adapter over that document: it reads it forgivingly
 * (a malformed entry is dropped, never thrown), edits it immutably, and hands
 * each widget its own entry. WidgetHost provides the entry for the item it
 * renders; `useTile()` reads it, and Tile.vue applies the name, link and
 * chromeless to every widget's identity line.
 */
import { computed, inject, provide, type ComputedRef, type InjectionKey } from 'vue';

export interface TileSource {
  instance: string;
  capability: string;
  options?: Record<string, unknown>;
}

export interface TileBinding {
  /** An integration instance id: status from "can Cha Dash read it?". */
  instance?: string;
  /** A checks target's name: status from "is it up?". */
  check?: string;
  /** A Home Graph resource id. Overrides the target in the item id (`type@resource`). */
  resource?: string;
  /** Instances read by capability (the media calendar reads Sonarr and Radarr). */
  sources?: TileSource[];
}

export interface TileOrigin {
  /** `homepage` (an import), `labels` (label discovery); absent when added by hand. */
  source?: string;
  file?: string;
  group?: string;
  entry?: string;
}

export interface TileConfig {
  name?: string;
  icon?: string;
  /** http(s) only: anything else is dropped, here and by the hub. */
  href?: string;
  description?: string;
  chromeless?: boolean;
  bind?: TileBinding;
  options?: Record<string, unknown>;
  origin?: TileOrigin;
}

/** A board's tiles by layout item id. Treated as immutable. */
export type TilesDoc = Readonly<Record<string, TileConfig>>;

export const EMPTY_TILES: TilesDoc = Object.freeze({});

type O = Record<string, unknown>;
const isObj = (v: unknown): v is O => typeof v === 'object' && v !== null && !Array.isArray(v);
const str = (v: unknown) => (typeof v === 'string' && v !== '' ? v : undefined);

/** The hub's rule (boards.HrefOK): http:// or https://, any case. */
export const hrefOk = (href: string | undefined): href is string => typeof href === 'string' && /^https?:\/\//i.test(href);

function sourceOf(v: unknown): TileSource | undefined {
  if (!isObj(v)) return undefined;
  const instance = str(v['instance']);
  const capability = str(v['capability']);
  if (!instance || !capability) return undefined;
  const s: TileSource = { instance, capability };
  if (isObj(v['options'])) s.options = v['options'];
  return s;
}

function bindingOf(v: unknown): TileBinding | undefined {
  if (!isObj(v)) return undefined;
  const b: TileBinding = {};
  for (const k of ['instance', 'check', 'resource'] as const) {
    const s = str(v[k]);
    if (s) b[k] = s;
  }
  if (Array.isArray(v['sources'])) {
    const sources = v['sources'].map(sourceOf).filter((s): s is TileSource => Boolean(s));
    if (sources.length) b.sources = sources;
  }
  return Object.keys(b).length ? b : undefined;
}

function originOf(v: unknown): TileOrigin | undefined {
  if (!isObj(v)) return undefined;
  const o: TileOrigin = {};
  for (const k of ['source', 'file', 'group', 'entry'] as const) {
    const s = str(v[k]);
    if (s) o[k] = s;
  }
  return Object.keys(o).length ? o : undefined;
}

/** One entry of the document, or undefined when it isn't an object. Unknown fields are dropped. */
export function tileConfigOf(v: unknown): TileConfig | undefined {
  if (!isObj(v)) return undefined;
  const c: TileConfig = {};
  for (const k of ['name', 'icon', 'description'] as const) {
    const s = str(v[k]);
    if (s) c[k] = s;
  }
  const href = str(v['href']);
  if (hrefOk(href)) c.href = href;
  if (v['chromeless'] === true) c.chromeless = true;
  const bind = bindingOf(v['bind']);
  if (bind) c.bind = bind;
  if (isObj(v['options'])) c.options = v['options'];
  const origin = originOf(v['origin']);
  if (origin) c.origin = origin;
  return c;
}

/** The whole document from the hub (or a snapshot). Never throws. */
export function tilesFromDoc(raw: unknown): TilesDoc {
  if (!isObj(raw)) return EMPTY_TILES;
  const out: Record<string, TileConfig> = {};
  for (const [id, v] of Object.entries(raw)) {
    const c = id ? tileConfigOf(v) : undefined;
    if (c) out[id] = c;
  }
  return out;
}

/** Without empty fields, so an emptied entry disappears rather than lingering as `{}`. */
function compact(c: TileConfig): TileConfig | undefined {
  const out = tileConfigOf(c);
  return out && Object.keys(out).length ? out : undefined;
}

/** `tiles` with one entry replaced (or removed, when `config` is empty or undefined). */
export function withTile(tiles: TilesDoc, itemId: string, config: TileConfig | undefined): TilesDoc {
  const next: Record<string, TileConfig> = { ...tiles };
  const c = config ? compact(config) : undefined;
  if (c) next[itemId] = c;
  else delete next[itemId];
  return next;
}

/** Only the entries for items still on the board (a removed tile's config goes with it). */
export function pruneTiles(tiles: TilesDoc, itemIds: Iterable<string>): TilesDoc {
  const keep = new Set(itemIds);
  const ids = Object.keys(tiles);
  if (ids.every((id) => keep.has(id))) return tiles;
  const next: Record<string, TileConfig> = {};
  for (const id of ids) if (keep.has(id)) next[id] = tiles[id]!;
  return next;
}

/** "Servers → ProxmoxPluto": where an imported tile came from. */
export function originLabel(c: TileConfig | undefined): string {
  const o = c?.origin;
  if (!o) return '';
  return [o.group, o.entry].filter(Boolean).join(' → ');
}

/** The tile a widget is rendering, provided by WidgetHost. */
export interface TileContext {
  itemId: string;
  type: string;
  config: ComputedRef<TileConfig | undefined>;
}

const KEY: InjectionKey<TileContext> = Symbol('tile');

export function provideTile(ctx: TileContext): void {
  provide(KEY, ctx);
}

const NONE = computed<TileConfig | undefined>(() => undefined);

/** The current tile's config. Outside WidgetHost (a preview, a test) there is none. */
export function useTile(): TileContext {
  return inject(KEY, { itemId: '', type: '', config: NONE });
}
