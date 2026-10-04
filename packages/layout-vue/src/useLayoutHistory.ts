import {
  canRedo as engineCanRedo,
  canUndo as engineCanUndo,
  commit as engineCommit,
  createHistory,
  redo as engineRedo,
  undo as engineUndo,
  type Board,
  type History,
} from '@chadash/layout';
import { computed, isRef, shallowRef, toRaw, toValue, watch, type ComputedRef, type MaybeRefOrGetter } from 'vue';

export interface UseLayoutHistoryOptions {
  /** Maximum undo steps (the engine's default is 100). */
  readonly limit?: number;
}

export interface LayoutHistory {
  /** The present board. Bind it to `<LayoutBoard :board>`. */
  readonly board: ComputedRef<Board>;
  /** The underlying immutable `@chadash/layout` history. */
  readonly history: ComputedRef<History<Board>>;
  readonly canUndo: ComputedRef<boolean>;
  readonly canRedo: ComputedRef<boolean>;
  /** Record a new present (wire it to `@commit`). Committing the present itself is a no-op. */
  commit(next: Board): void;
  /** Step back; returns the new present. */
  undo(): Board;
  /** Step forward; returns the new present. */
  redo(): Board;
  /** Start a fresh history at `board` (for example after loading a saved layout). */
  reset(board: Board): void;
}

/**
 * Undo and redo over `@chadash/layout` history, as Vue state.
 *
 * Pass the initial board, or a ref or getter. With a ref or getter, a new
 * board that isn't the current present (a layout loaded from the hub, say)
 * starts a fresh history.
 */
export function useLayoutHistory(initial: MaybeRefOrGetter<Board>, options: UseLayoutHistoryOptions = {}): LayoutHistory {
  // Boards are immutable: keep raw objects even if a caller hands over a reactive proxy.
  const create = (board: Board): History<Board> =>
    options.limit === undefined ? createHistory(toRaw(board)) : createHistory(toRaw(board), { limit: options.limit });
  const history = shallowRef(create(toValue(initial)));

  if (isRef(initial) || typeof initial === 'function') {
    watch(
      () => toValue(initial),
      (board) => {
        if (toRaw(board) !== history.value.present) history.value = create(board);
      },
    );
  }

  return {
    board: computed(() => history.value.present),
    history: computed(() => history.value),
    canUndo: computed(() => engineCanUndo(history.value)),
    canRedo: computed(() => engineCanRedo(history.value)),
    commit(next) {
      history.value = engineCommit(history.value, toRaw(next));
    },
    undo() {
      history.value = engineUndo(history.value);
      return history.value.present;
    },
    redo() {
      history.value = engineRedo(history.value);
      return history.value.present;
    },
    reset(board) {
      history.value = create(board);
    },
  };
}
