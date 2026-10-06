<script setup lang="ts">
/**
 * The board on the front page, and the only file that knows which renderer
 * draws it: the read-only StaticBoard, or EditableBoard (<LayoutBoard> from
 * @chadash/layout-vue) once you choose Edit. EditableBoard loads on demand,
 * so a front page you only read never downloads the gesture and motion code.
 * It also loads the hub's icon map when a tile could use it.
 */
import { defineAsyncComponent, watch } from 'vue';
import type { Board, Breakpoint } from '@chadash/layout';
import StaticBoard from './StaticBoard.vue';
import SectionHeader from './SectionHeader.vue';
import WidgetHost from '../widgets/WidgetHost.vue';
import { useUi } from '../stores/ui';
import { useBoard } from '../stores/board';
import { loadIcons, wantsIcon } from '../stores/icons';

const EditableBoard = defineAsyncComponent(() => import('./EditableBoard.vue'));

const { board, breakpoint } = defineProps<{ board: Board; breakpoint?: Breakpoint | undefined }>();
// Edit mode lives in the UI store, so ⌘K → "Edit layout" can toggle it too.
const ui = useUi();

// Tiles show the icons the hub cached (stores/icons.ts): load the map once
// the board has a tile that could use one.
const store = useBoard();
watch(
  () => store.tiles.value,
  (tiles) => {
    if (Object.values(tiles).some((t) => wantsIcon(t, t.href))) void loadIcons();
  },
  { immediate: true },
);
</script>

<template>
  <EditableBoard v-if="ui.state.editing && ui.can.value.editBoards" :board="board" :breakpoint="breakpoint" @done="ui.state.editing = false" />
  <template v-else>
    <div v-if="ui.can.value.editBoards" class="board-tools">
      <button type="button" class="btn ghost sm" @click="ui.state.editing = true">Edit layout</button>
    </div>
    <StaticBoard :board="board" :breakpoint="breakpoint">
      <template #section="{ section }">
        <SectionHeader :section="section" />
      </template>
      <template #item="{ item, size }">
        <WidgetHost :item="item" :size="size" />
      </template>
    </StaticBoard>
  </template>
</template>

<style>
.board-tools {
  display: flex;
  justify-content: flex-end;
  margin: 0 0 var(--space-2);
}
</style>
