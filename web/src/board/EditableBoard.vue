<script setup lang="ts">
/**
 * The board in edit mode: <LayoutBoard> from @chadash/layout-vue with drag,
 * snapping resize and keyboard moves, plus undo/redo. Loaded on demand from
 * BoardSurface, so motion and the gesture code never ride with a read-only
 * front page.
 *
 * Every committed gesture is shown and saved through the board store; on a
 * 409 the store loads the other writer's layout, which starts a fresh
 * history because it isn't the present board.
 */
import type { Board, Breakpoint, Item } from '@chadash/layout';
import { LayoutBoard, useLayoutHistory } from '@chadash/layout-vue';
import { onKeyStroke } from '@vueuse/core';
import SectionHeader from './SectionHeader.vue';
import WidgetHost from '../widgets/WidgetHost.vue';
import { label } from '../model/graph';
import { parseItemId, WIDGETS } from '../model/widgets';
import { useBoard } from '../stores/board';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';

const { board, breakpoint } = defineProps<{ board: Board; breakpoint?: Breakpoint | undefined }>();
const emit = defineEmits<{ done: [] }>();

const store = useBoard();
const live = useLive();
const ui = useUi();
const history = useLayoutHistory(() => board);

function persist(next: Board): void {
  store.save(next).then(
    (saved) => {
      if (!saved) ui.toast('This board changed somewhere else. Showing the latest layout.', 'error');
    },
    () => ui.toast('Couldn’t save the layout. It stays here; the next change retries.', 'error'),
  );
}

function onCommit(next: Board): void {
  history.commit(next);
  persist(next);
}

function step(direction: 'undo' | 'redo'): void {
  const can = direction === 'undo' ? history.canUndo.value : history.canRedo.value;
  if (!can) return;
  persist(direction === 'undo' ? history.undo() : history.redo());
}

/** "Host: jupiter", "Cameras": the name in labels and announcements. */
function itemLabel(item: Item): string {
  const { type, target } = parseItemId(item.id);
  const name = WIDGETS[type]?.name ?? type;
  const r = target ? live.graph.value?.resources[target] : undefined;
  return r ? `${name}: ${label(r)}` : name;
}

onKeyStroke('z', (e) => {
  if (!(e.metaKey || e.ctrlKey) || e.altKey) return;
  e.preventDefault();
  step(e.shiftKey ? 'redo' : 'undo');
});
</script>

<template>
  <div class="edit-bar" role="toolbar" aria-label="Edit layout">
    <span class="edit-hint">Drag a tile, or resize it from its corner. With the keyboard, Enter picks a tile up and the arrows move it.</span>
    <span v-if="store.state.unsaved" class="edit-unsaved" role="status">Not saved</span>
    <button type="button" class="btn ghost sm" :disabled="!history.canUndo.value" @click="step('undo')">Undo</button>
    <button type="button" class="btn ghost sm" :disabled="!history.canRedo.value" @click="step('redo')">Redo</button>
    <button type="button" class="btn primary sm" @click="emit('done')">Done</button>
  </div>
  <LayoutBoard
    :board="history.board.value"
    v-bind="breakpoint ? { breakpoint } : {}"
    :editing="true"
    :item-label="itemLabel"
    @commit="onCommit"
  >
    <template #section="{ section }">
      <SectionHeader :section="section" />
    </template>
    <template #item="{ item, size }">
      <WidgetHost :item="item" :size="size" />
    </template>
  </LayoutBoard>
</template>

<style>
.edit-bar {
  position: sticky;
  top: var(--space-2);
  z-index: 5;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
  margin: 0 0 var(--space-4);
  padding: var(--space-2) var(--space-3);
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-lg);
  background: var(--surface);
}
.edit-hint {
  flex: 1 1 18rem;
  font-size: var(--type-caption);
  color: var(--text-2);
}
.edit-unsaved {
  font-size: var(--type-caption);
  color: var(--attention-text);
}
</style>
