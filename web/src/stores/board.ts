/**
 * The board on the front page: which board, its layout, and its revision.
 *
 *   GET /api/boards                 → {"boards":[{"id","name"}]}
 *   GET /api/boards/{id}/layout     → {"revision","format_version","layout": doc | null}
 *   PUT /api/boards/{id}/layout     {revision, format_version, layout} → 200 {"revision"} | 409
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

export interface BoardRef {
  id: string;
  name: string;
}

interface LayoutResponse {
  revision: number;
  format_version: number;
  layout: unknown;
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
  /** The last stored layout document as the hub has it, for the snapshot. */
  let stored: unknown = undefined;
  let fallback: StoredBoard | undefined;

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

  function put(b: Board): Promise<{ revision: number }> {
    const layout: LayoutDocument = toDocument(b);
    return api<{ revision: number }>(`/api/boards/${encodeURIComponent(state.boardId)}/layout`, {
      method: 'PUT',
      body: { revision: state.revision, format_version: LAYOUT_VERSION, layout },
    });
  }

  /**
   * Show and store an edited board (the edit-mode seam). Returns false on a
   * 409: someone else saved first, and their layout is loaded instead.
   */
  async function save(b: Board): Promise<boolean> {
    board.value = b;
    try {
      const res = await put(b);
      state.revision = res.revision;
      stored = toDocument(b);
      state.unsaved = false;
      return true;
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        await loadLayout();
        return false;
      }
      state.unsaved = true;
      throw err;
    }
  }

  /** What the last-known snapshot keeps: the saved layout, never an unsaved one. */
  function snapshot(): StoredBoard | undefined {
    if (!state.boardId || stored === undefined) return undefined;
    return { id: state.boardId, name: state.name, revision: state.revision, layout: stored };
  }

  return { state, board, load, save, restore, snapshot };
}

export type BoardStore = ReturnType<typeof createBoardStore>;

const KEY: InjectionKey<BoardStore> = Symbol('board');
export const provideBoard = (b: BoardStore) => provide(KEY, b);
export function useBoard(): BoardStore {
  const b = inject(KEY);
  if (!b) throw new Error('useBoard() outside the app');
  return b;
}
