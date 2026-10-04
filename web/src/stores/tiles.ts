/**
 * What an imported tile shows: its name, link, description, and what watches
 * it. The layout document only carries ids, types and sizes, so until tiles
 * get a real config home (wave 6) this comes from the last Homepage import's
 * record, read through this one adapter:
 *
 *   GET /api/import/homepage → {record: {board_id, tiles: [{item_id, section,
 *     widget, size, name, icon, href, description, chromeless, instance,
 *     check, sources, options, origin}], …}}   (404 before the first import)
 *
 * Loaded once, on demand, by the first widget that needs it; reloaded after
 * an import is applied.
 */
import { reactive } from 'vue';
import { ApiError, api } from '../lib/api';

export interface TileMeta {
  itemId: string;
  section?: string;
  widget?: string;
  name?: string;
  icon?: string;
  /** http(s) only; anything else is dropped. */
  href?: string;
  description?: string;
  chromeless?: boolean;
  /** The integration instance the tile shows. */
  instance?: string;
  /** The checks target that watches it. */
  check?: string;
  /** Where in the source it came from: "Servers → ProxmoxPluto". */
  origin?: string;
}

type O = Record<string, unknown>;
const str = (v: unknown) => (typeof v === 'string' && v !== '' ? v : undefined);

/** One record tile → TileMeta. Unknown fields are ignored; a bad href is dropped. */
export function tileMetaOf(t: unknown): TileMeta | undefined {
  const o = (t ?? {}) as O;
  const itemId = str(o['item_id']);
  if (!itemId) return undefined;
  const out: TileMeta = { itemId };
  for (const [k, from] of [
    ['section', 'section'],
    ['widget', 'widget'],
    ['name', 'name'],
    ['icon', 'icon'],
    ['description', 'description'],
    ['instance', 'instance'],
    ['check', 'check'],
  ] as const) {
    const v = str(o[from]);
    if (v) out[k] = v;
  }
  const href = str(o['href']);
  if (href && /^https?:\/\//i.test(href)) out.href = href;
  if (o['chromeless'] === true) out.chromeless = true;
  const origin = (o['origin'] ?? {}) as O;
  const where = [str(origin['group']), str(origin['entry'])].filter(Boolean).join(' → ');
  if (where) out.origin = where;
  return out;
}

export function tileMetaFromRecord(res: unknown): Record<string, TileMeta> {
  const rec = ((res as O | null)?.['record'] ?? res) as O | null;
  const tiles = Array.isArray(rec?.['tiles']) ? (rec['tiles'] as unknown[]) : [];
  const out: Record<string, TileMeta> = {};
  for (const t of tiles) {
    const m = tileMetaOf(t);
    if (m) out[m.itemId] = m;
  }
  return out;
}

export const tiles = reactive({
  byItem: {} as Record<string, TileMeta>,
  status: 'idle' as 'idle' | 'loading' | 'ready' | 'none' | 'error',
});

let inflight: Promise<void> | undefined;

/** Load the record once (later calls share the first). */
export function ensureTileMeta(force = false): Promise<void> {
  if (!force && (tiles.status === 'ready' || tiles.status === 'none')) return Promise.resolve();
  if (inflight) return inflight;
  tiles.status = 'loading';
  inflight = (async () => {
    try {
      tiles.byItem = tileMetaFromRecord(await api('/api/import/homepage'));
      tiles.status = 'ready';
    } catch (err) {
      // 404: nothing imported yet. 403 or a missing route: no metadata, tiles fall back to their ids.
      tiles.status = err instanceof ApiError && err.status !== 0 ? 'none' : 'error';
    } finally {
      inflight = undefined;
    }
  })();
  return inflight;
}

/** For tests. */
export function resetTileMeta(): void {
  tiles.byItem = {};
  tiles.status = 'idle';
  inflight = undefined;
}
