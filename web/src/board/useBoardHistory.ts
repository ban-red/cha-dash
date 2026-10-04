/**
 * Undo and redo over a board and its tiles together, on @chadash/layout's
 * immutable history. A drag changes the board, an Edit changes the tiles, a
 * Remove changes both; each is one step, and stepping back restores both.
 *
 * A board that arrives from elsewhere (the other writer's layout after a
 * 409) isn't the present one, so it starts a fresh history.
 */
import { canRedo, canUndo, commit, createHistory, redo, undo, type Board, type History } from '@chadash/layout';
import { computed, shallowRef, toRaw, watch } from 'vue';
import type { BoardState } from '../model/edit';

export function useBoardHistory(source: () => BoardState) {
  const raw = (s: BoardState): BoardState => ({ board: toRaw(s.board), tiles: toRaw(s.tiles) });
  const history = shallowRef<History<BoardState>>(createHistory(raw(source())));

  watch(
    () => toRaw(source().board),
    (board: Board) => {
      if (board !== history.value.present.board) history.value = createHistory(raw(source()));
    },
  );

  return {
    present: computed(() => history.value.present),
    canUndo: computed(() => canUndo(history.value)),
    canRedo: computed(() => canRedo(history.value)),
    /** Record a new present. */
    commit(next: BoardState): void {
      history.value = commit(history.value, raw(next));
    },
    /** Step back; returns the new present. */
    undo(): BoardState {
      history.value = undo(history.value);
      return history.value.present;
    },
    /** Step forward; returns the new present. */
    redo(): BoardState {
      history.value = redo(history.value);
      return history.value.present;
    },
  };
}
