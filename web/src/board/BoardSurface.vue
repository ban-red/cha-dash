<script setup lang="ts">
/**
 * The board on the front page, and the only file that knows which renderer
 * draws it: the read-only StaticBoard, or EditableBoard (<LayoutBoard> from
 * @chadash/layout-vue) once you choose Edit. EditableBoard loads on demand,
 * so a front page you only read never downloads the gesture and motion code.
 * It also loads the hub's icon map when a tile could use it.
 */
import { computed, defineAsyncComponent, ref, watch } from 'vue';
import type { Board, Breakpoint } from '@chadash/layout';
import StaticBoard from './StaticBoard.vue';
import SectionHeader from './SectionHeader.vue';
import WidgetHost from '../widgets/WidgetHost.vue';
import { useUi } from '../stores/ui';
import { useBoard } from '../stores/board';
import { loadIcons, wantsIcon } from '../stores/icons';
import { loadInstances } from '../stores/integrations';
import { settingsPath } from '../lib/router';
import type { AddRequest } from '../model/edit';

const EditableBoard = defineAsyncComponent(() => import('./EditableBoard.vue'));
const WidgetPicker = defineAsyncComponent(() => import('./WidgetPicker.vue'));

const { board, breakpoint } = defineProps<{ board: Board; breakpoint?: Breakpoint | undefined }>();
// Edit mode lives in the UI store, so ⌘K → "Edit layout" can toggle it too.
const ui = useUi();

// An empty board is a first run in miniature: offer the ways in.
const empty = computed(() => board.sections.every((s) => s.items.length === 0));

// Tiles show the icons the hub cached (stores/icons.ts): load the map once
// the board has a tile that could use one.
const store = useBoard();

// The widget picker opens over the board as it is; edit mode only starts once
// a widget is picked (the pick goes along in ui.state.pendingAdd). In edit mode
// EditableBoard answers addingWidget itself.
const picking = ref<string | null>(null);

function addTo(sectionId: string): void {
  ui.state.addSection = sectionId;
  ui.state.addingWidget = true;
}
watch(
  () => ui.state.addingWidget,
  (asked) => {
    if (!asked || ui.state.editing || !ui.can.value.editBoards) return;
    ui.state.addingWidget = false;
    picking.value = ui.state.addSection || board.sections[0]?.id || '';
    ui.state.addSection = '';
  },
  { immediate: true },
);
function picked(req: AddRequest): void {
  picking.value = null;
  ui.state.pendingAdd = req;
  ui.state.editing = true;
}
watch(
  () => store.tiles.value,
  (tiles) => {
    if (Object.values(tiles).some((t) => wantsIcon(t, t.href))) void loadIcons();
    // Bound tiles borrow their integration's bundled logo; viewers who may not list instances keep the monogram.
    if (Object.values(tiles).some((t) => t.bind?.instance)) void loadInstances().catch(() => undefined);
  },
  { immediate: true },
);
</script>

<template>
  <EditableBoard v-if="ui.state.editing && ui.can.value.editBoards" :board="board" :breakpoint="breakpoint" @done="ui.state.editing = false" />
  <template v-else>
    <div v-if="empty && ui.can.value.panes.includes('import')" class="board-empty">
      <p><b>Bring in what you run.</b> Import another dashboard’s config, let Cha Dash discover services, or add them one by one.</p>
      <span class="board-empty-acts">
        <button type="button" class="btn primary sm" @click="ui.openSettings('import')">Import a config</button>
        <button type="button" class="btn sm" @click="ui.openSettings('import', 'discover')">Discover</button>
        <a class="btn sm ghost" :href="settingsPath('integrations', 'new')" @click.prevent="ui.openSettings('integrations', 'new')">Add one</a>
      </span>
    </div>
    <StaticBoard :board="board" :breakpoint="breakpoint">
      <template #section="{ section }">
        <!-- Editors get an Add on hover or focus: it opens the picker on this section. -->
        <div v-if="ui.can.value.editBoards" class="sh-live">
          <SectionHeader :section="section" />
          <button type="button" class="btn ghost sm sh-add" :aria-label="`Add a widget to ${section.title ?? section.id}`" @click="addTo(section.id)">Add</button>
        </div>
        <SectionHeader v-else :section="section" />
      </template>
      <template #item="{ item, size }">
        <WidgetHost :item="item" :size="size" />
      </template>
    </StaticBoard>
    <WidgetPicker v-if="picking !== null" :board="board" :tiles="store.tiles.value" :section-id="picking" @add="picked" @close="picking = null" />
  </template>
</template>

<style>
.board-empty {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  margin: 0 0 var(--space-4);
  padding: var(--space-4);
  border: var(--hairline) dashed var(--line-2);
  border-radius: var(--radius-md);
}
.board-empty p {
  margin: 0;
  color: var(--text-2);
}
.board-empty-acts {
  display: inline-flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}
</style>
