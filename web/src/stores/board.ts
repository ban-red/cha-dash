/**
 * The board on the front page: which board, its layout, its tiles, and its
 * revision.
 *
 *   GET /api/boards                 → {"boards":[{"id","name"}]}
 *   GET /api/boards/{id}/layout     → {"revision","format_version","layout": doc | null,"tiles": {…}}
 *   PUT /api/boards/{id}/layout     {revision, format_version, layout, tiles?} → 200 {"revision"} | 409
 *
 * The tiles document (stores/tiles.ts) is each item's configuration: it rides
 * on the same revision as the layout, so a save carries both, or only the
 * layout (a drag: the hub keeps the tiles it has). Saves run one at a time,
 * each on the revision the previous one returned.
 *
 * A null layout means a new board: build the default from the graph and PUT
 * it. A 409 means someone else saved first; theirs wins and is loaded.
 *
 * `load(id)` opens a board by id (the /boards/{id} route, or the board an
 * import just created); without one it opens the first. When the hub can't be
 * reached, the board from the last-known snapshot stands in, read-only.
 */
import { inject, provide, reactive, shallowRef, type InjectionKey } from 'vue';
import { LAYOUT_VERSION, fromDocument, toDocument, type Board, type LayoutDocument } from '@chadash/layout';
import { ApiError, api } from '../lib/api';
import { defaultBoard } from '../model/defaultLayout';
import type { GraphDoc } from '../model/types';
import { EMPTY_TILES, tilesFromDoc, type TilesDoc } from './tiles';

export interface BoardRef {
  id: string;
  name: string;
}

interface LayoutResponse {
  revision: number;
  format_version: number;
  layout: unknown;
  tiles?: unknown;
}

export type BoardStatus = 'idle' | 'loading' | 'ready' | 'error';

export interface BoardOptions {
  /** Resolves with the graph once it arrives (or undefined after a timeout). */
  waitForGraph: () => Promise<GraphDoc | undefined>;
  now?: () => number;
}

/** The board as the last-known snapshot keeps it. */
export interface StoredBoard {
  id: string;
  name: string;
  revision: number;
  layout: unknown;
  tiles?: unknown;
}

export function createBoardStore(opts: BoardOptions) {
  const state = reactive({
    status: 'idle' as BoardStatus,
    boards: [] as BoardRef[],
    boardId: '',
    name: '',
    revision: 0,
    /** Built here and not saved (e.g. the stored layout is from a newer version). */
    unsaved: false,
    /** Showing the last-known board because the hub can't be reached. */
    lastKnown: false,
    error: '',
  });
  const board = shallowRef<Board | undefined>(undefined);
  /** Each item's configuration, by item id. */
  const tiles = shallowRef<TilesDoc>(EMPTY_TILES);
  /** The last stored layout document as the hub has it, for the snapshot. */
  let stored: unknown = undefined;
  let fallback: StoredBoard | undefined;
  /** The save in flight: the next one waits for it, so each uses the revision it returned. */
  let saving: Promise<unknown> = Promise.resolve();
  /** Bumped by a 409, which drops the saves queued behind it. */
  let generation = 0;

  /** The board to fall back on while the hub is unreachable. */
  function restore(b: StoredBoard | undefined): void {
    fallback = b;
    if (!b) return;
    if (board.value) {
      if (state.boardId === b.id) {
        state.lastKnown = true;
        state.status = 'ready';
      }
      return;
    }
    try {
      board.value = fromDocument(b.layout);
      tiles.value = tilesFromDoc(b.tiles);
      stored = b.layout;
      state.boardId = b.id;
      state.name = b.name;
      state.revision = b.revision;
      state.lastKnown = true;
      state.status = 'ready';
    } catch {
      /* a layout this version can't read: the page waits for the hub */
    }
  }

  /**
   * Show the board the hub inlined into the page (lib/inline.ts). It's
   * current, so no load is needed; false if this version can't read it.
   */
  function seed(boards: BoardRef[], b: StoredBoard): boolean {
    try {
      board.value = fromDocument(b.layout);
    } catch {
      return false;
    }
    tiles.value = tilesFromDoc(b.tiles);
    stored = b.layout;
    state.boards = boards;
    state.boardId = b.id;
    state.name = b.name;
    state.revision = b.revision;
    state.unsaved = false;
    state.lastKnown = false;
    state.status = 'ready';
    return true;
  }

  async function load(id?: string): Promise<void> {
    state.status = 'loading';
    state.error = '';
    try {
      const { boards } = await api<{ boards: BoardRef[] }>('/api/boards');
      state.boards = boards ?? [];
      const pick = (id && state.boards.find((b) => b.id === id)) || state.boards[0];
      if (!pick) {
        // No board on the hub yet: show the default, unsaved.
        board.value = defaultBoard(await opts.waitForGraph(), (opts.now ?? Date.now)());
        tiles.value = EMPTY_TILES;
        state.unsaved = true;
        state.lastKnown = false;
        state.status = 'ready';
        return;
      }
      state.boardId = pick.id;
      state.name = pick.name;
      await loadLayout();
      state.lastKnown = false;
      state.status = 'ready';
    } catch (err) {
      if (err instanceof ApiError && err.status === 0 && fallback && (!id || fallback.id === id)) {
        restore(fallback);
        if (board.value) return;
      }
      state.status = 'error';
      state.error =
        err instanceof ApiError && err.status === 0
          ? "Can't reach the hub to load this board."
          : err instanceof ApiError && err.status === 403
            ? 'Only the owner can open boards in this version.'
            : "This board couldn't be loaded.";
    }
  }

  async function loadLayout(): Promise<void> {
    const res = await api<LayoutResponse>(`/api/boards/${encodeURIComponent(state.boardId)}/layout`);
    state.revision = res.revision ?? 0;
    tiles.value = tilesFromDoc(res.tiles);
    if (res.layout !== null && res.layout !== undefined) {
      try {
        board.value = fromDocument(res.layout);
        stored = res.layout;
        state.unsaved = false;
        return;
      } catch {
        // A newer or damaged document: show the default, and don't overwrite theirs.
        board.value = defaultBoard(await opts.waitForGraph(), (opts.now ?? Date.now)());
        state.unsaved = true;
        return;
      }
    }
    await createDefault();
  }

  async function createDefault(): Promise<void> {
    const b = defaultBoard(await opts.waitForGraph(), (opts.now ?? Date.now)());
    board.value = b;
    try {
      const res = await put(b);
      state.revision = res.revision;
      stored = toDocument(b);
      state.unsaved = false;
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        // Another tab or device created it first. Load theirs.
        const again = await api<LayoutResponse>(`/api/boards/${encodeURIComponent(state.boardId)}/layout`);
        state.revision = again.revision ?? state.revision;
        tiles.value = tilesFromDoc(again.tiles);
        if (again.layout) {
          try {
            board.value = fromDocument(again.layout);
            stored = again.layout;
            state.unsaved = false;
            return;
          } catch {
            /* keep the default on screen */
          }
        }
      }
      state.unsaved = true;
    }
  }

  function put(b: Board, t?: TilesDoc): Promise<{ revision: number }> {
    const layout: LayoutDocument = toDocument(b);
    return api<{ revision: number }>(`/api/boards/${encodeURIComponent(state.boardId)}/layout`, {
      method: 'PUT',
      body: t === undefined ? { revision: state.revision, format_version: LAYOUT_VERSION, layout } : { revision: state.revision, format_version: LAYOUT_VERSION, layout, tiles: t },
    });
  }

  /**
   * Show and store an edited board (the edit-mode seam), with its tiles when
   * they changed too. Returns false on a 409: someone else saved first, and
   * their layout is loaded instead.
   */
  function save(b: Board, t?: TilesDoc): Promise<boolean> {
    board.value = b;
    if (t !== undefined) tiles.value = t;
    const gen = generation;
    const run = saving.then(async () => {
      // A 409 while this one waited loaded the other writer's board: don't overwrite it.
      if (gen !== generation) return false;
      try {
        const res = await put(b, t);
        state.revision = res.revision;
        stored = toDocument(b);
        state.unsaved = false;
        return true;
      } catch (err) {
        if (err instanceof ApiError && err.status === 409) {
          generation++;
          await loadLayout();
          return false;
        }
        state.unsaved = true;
        throw err;
      }
    });
    saving = run.catch(() => undefined);
    return run;
  }

  /** What the last-known snapshot keeps: the saved layout, never an unsaved one. */
  function snapshot(): StoredBoard | undefined {
    if (!state.boardId || stored === undefined) return undefined;
    return { id: state.boardId, name: state.name, revision: state.revision, layout: stored, tiles: tiles.value };
  }

  return { state, board, tiles, load, save, restore, seed, snapshot };
}

export type BoardStore = ReturnType<typeof createBoardStore>;

const KEY: InjectionKey<BoardStore> = Symbol('board');
export const provideBoard = (b: BoardStore) => provide(KEY, b);
/** The board store, or undefined outside the app (a tile in a preview or a test). */
export const useBoardIfAny = (): BoardStore | undefined => inject(KEY, undefined);
export function useBoard(): BoardStore {
  const b = inject(KEY);
  if (!b) throw new Error('useBoard() outside the app');
  return b;
}
