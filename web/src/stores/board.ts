/**
 * The board on the front page: which board, its layout, and its revision.
 *
 *   GET /api/boards                 → {"boards":[{"id","name"}]}
 *   GET /api/boards/{id}/layout     → {"revision","format_version","layout": doc | null}
 *   PUT /api/boards/{id}/layout     {revision, format_version, layout} → 200 {"revision"} | 409
 *
 * A null layout means a new board: build the default from the graph and PUT
 * it. A 409 means someone else saved first; theirs wins and is loaded.
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

export function createBoardStore(opts: BoardOptions) {
  const state = reactive({
    status: 'idle' as BoardStatus,
    boards: [] as BoardRef[],
    boardId: '',
    name: '',
    revision: 0,
    /** Built here and not saved (e.g. the stored layout is from a newer version). */
    unsaved: false,
    error: '',
  });
  const board = shallowRef<Board | undefined>(undefined);

  async function load(): Promise<void> {
    state.status = 'loading';
    state.error = '';
    try {
      const { boards } = await api<{ boards: BoardRef[] }>('/api/boards');
      state.boards = boards ?? [];
      const first = state.boards[0];
      if (!first) {
        // No board on the hub yet: show the default, unsaved.
        board.value = defaultBoard(await opts.waitForGraph(), (opts.now ?? Date.now)());
        state.unsaved = true;
        state.status = 'ready';
        return;
      }
      state.boardId = first.id;
      state.name = first.name;
      await loadLayout();
      state.status = 'ready';
    } catch (err) {
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
      state.unsaved = false;
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        // Another tab or device created it first. Load theirs.
        const again = await api<LayoutResponse>(`/api/boards/${encodeURIComponent(state.boardId)}/layout`);
        state.revision = again.revision ?? state.revision;
        if (again.layout) {
          try {
            board.value = fromDocument(again.layout);
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

  return { state, board, load, save };
}

export type BoardStore = ReturnType<typeof createBoardStore>;

const KEY: InjectionKey<BoardStore> = Symbol('board');
export const provideBoard = (b: BoardStore) => provide(KEY, b);
export function useBoard(): BoardStore {
  const b = inject(KEY);
  if (!b) throw new Error('useBoard() outside the app');
  return b;
}
