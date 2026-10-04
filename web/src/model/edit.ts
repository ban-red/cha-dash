/**
 * Edit-mode changes to a board and its tiles, as pure functions: add a
 * widget from the catalog, edit a tile's config, remove a tile. Each returns
 * a new BoardState (the board and its tiles together), so undo and redo can
 * step over both and the board store can save them on one revision.
 */
import { SIZES, remove, smartInsert, type Board, type SizeId } from '@chadash/layout';
import { itemId, parseItemId, type WidgetSpec } from './widgets';
import { pruneTiles, withTile, type TileConfig, type TilesDoc } from '../stores/tiles';

export interface BoardState {
  readonly board: Board;
  readonly tiles: TilesDoc;
}

export const itemIds = (board: Board): string[] => board.sections.flatMap((s) => s.items.map((i) => i.id));

/** The resource an item shows: its tile's bound resource, else the one in its id. */
export function targetOf(id: string, tiles: TilesDoc): string | undefined {
  return tiles[id]?.bind?.resource ?? parseItemId(id).target;
}

/** The sizes of a widget that fit a section, in its declared order. */
export function sizesFor(spec: Pick<WidgetSpec, 'sizes'>, span: number): SizeId[] {
  return spec.sizes.filter((s) => SIZES[s].w <= span);
}

/** "Small 2×2". */
export const sizeName = (s: SizeId): string => `${SIZES[s].name} ${SIZES[s].w}×${SIZES[s].h}`;

function randomPart(): string {
  const bytes = new Uint8Array(6);
  crypto.getRandomValues(bytes);
  return [...bytes].map((b) => b.toString(36).padStart(2, '0')).join('').slice(0, 10);
}

/**
 * The id a new item gets: `<type>@<resource>` for a widget bound to one
 * resource, the bare type for a singleton, and `service@tile:<random>` for a
 * service tile. Undefined when the board already shows it.
 */
export function newItemId(state: BoardState, spec: Pick<WidgetSpec, 'type' | 'bound'>, target?: string): string | undefined {
  if (spec.type === 'service') return itemId('service', `tile:${randomPart()}`);
  const items = state.board.sections.flatMap((s) => s.items);
  if (spec.bound) {
    if (!target) return undefined;
    if (items.some((i) => i.type === spec.type && targetOf(i.id, state.tiles) === target)) return undefined;
    const id = itemId(spec.type, target);
    return items.some((i) => i.id === id) ? undefined : id;
  }
  return items.some((i) => i.id === spec.type) ? undefined : spec.type;
}

export interface AddRequest {
  spec: WidgetSpec;
  sectionId: string;
  size: SizeId;
  /** The resource, for a widget bound to one. */
  target?: string;
  /** The new tile's config (a service tile's name, link and binding). */
  config?: TileConfig;
}

export interface Added extends BoardState {
  id: string;
}

/**
 * Add a widget into a section, in the first gap where it fits (nothing else
 * moves). Throws a LayoutError when nothing fits, or Error when the board
 * already shows it.
 */
export function addWidget(state: BoardState, req: AddRequest): Added {
  const id = newItemId(state, req.spec, req.target);
  if (!id) throw new Error(`${req.spec.name} is already on this board`);
  const size = req.spec.sizes.includes(req.size) ? req.size : req.spec.size;
  const board = smartInsert(state.board, { id, type: req.spec.type, size, supportedSizes: req.spec.sizes }, { sectionId: req.sectionId });
  const tiles = req.config ? withTile(state.tiles, id, req.config) : state.tiles;
  return { board, tiles, id };
}

/** Replace one tile's config (undefined or empty clears it). */
export function editTile(state: BoardState, id: string, config: TileConfig | undefined): BoardState {
  return { board: state.board, tiles: withTile(state.tiles, id, config) };
}

/** Remove a tile and its config. Undo brings both back. */
export function removeTile(state: BoardState, id: string): BoardState {
  const board = remove(state.board, id);
  return { board, tiles: pruneTiles(state.tiles, itemIds(board)) };
}

/** The section an item is in. */
export function sectionOf(board: Board, id: string): string | undefined {
  return board.sections.find((s) => s.items.some((i) => i.id === id))?.id;
}
