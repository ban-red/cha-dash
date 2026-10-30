<script setup lang="ts">
/**
 * The board on the front page, and the only file that knows which renderer
 * draws it: the read-only StaticBoard, or EditableBoard (<LayoutBoard> from
 * @chadash/layout-vue) once you choose Edit. EditableBoard loads on demand,
 * so a front page you only read never downloads the gesture and motion code.
 * It also loads the hub's icon map when a tile could use it.
 */
import { computed, defineAsyncComponent, ref, watch } from 'vue';
import { findItem, type Board, type Breakpoint } from '@chadash/layout';
import StaticBoard from './StaticBoard.vue';
import SectionHeader from './SectionHeader.vue';
import SectionMenu from './SectionMenu.vue';
import WidgetHost from '../widgets/WidgetHost.vue';
import { useUi } from '../stores/ui';
import { useBoard } from '../stores/board';
import { loadIcons, wantsIcon } from '../stores/icons';
import { loadInstances } from '../stores/integrations';
import { settingsPath } from '../lib/router';
import { editTile, type AddRequest } from '../model/edit';
import type { TileConfig } from '../stores/tiles';
import UiButton from '../ui/Button.vue';

const EditableBoard = defineAsyncComponent(() => import('./EditableBoard.vue'));
const WidgetPicker = defineAsyncComponent(() => import('./WidgetPicker.vue'));
const TileEditor = defineAsyncComponent(() => import('./TileEditor.vue'));

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
// A tile's menu → Widget settings: the tile editor over the board as it is. In
// edit mode EditableBoard answers editingTile itself.
const editingId = ref('');
const editingItem = computed(() => (editingId.value ? findItem(board, editingId.value)?.item : undefined));
watch(
  () => ui.state.editingTile,
  (id) => {
    if (!id || ui.state.editing) return;
    ui.state.editingTile = '';
    if (ui.can.value.editBoards && findItem(board, id)) editingId.value = id;
  },
  { immediate: true },
);
function saveTile(config: TileConfig | undefined): void {
  const id = editingId.value;
  editingId.value = '';
  if (!id || !findItem(board, id)) return;
  const next = editTile({ board, tiles: store.tiles.value }, id, config);
  store.save(next.board, next.tiles).then(
    (ok) => {
      if (!ok) ui.toast('This board changed somewhere else. Showing the latest layout.', 'error');
    },
    () => ui.toast('Couldn’t save the tile.', 'error'),
  );
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
        <UiButton variant="primary" size="sm" @click="ui.openSettings('import')">Import a config</UiButton>
        <UiButton size="sm" @click="ui.openSettings('import', 'discover')">Discover</UiButton>
        <UiButton variant="ghost" size="sm" :href="settingsPath('integrations', 'new')" @click.prevent="ui.openSettings('integrations', 'new')">Add one</UiButton>
      </span>
    </div>
    <StaticBoard :board="board" :breakpoint="breakpoint">
      <template #section="{ section }">
        <!-- Editors get an Add on hover or focus: it opens the picker on this section. -->
        <div v-if="ui.can.value.editBoards" class="sh-live">
          <SectionHeader :section="section" />
          <UiButton variant="ghost" size="sm" class="sh-add" :aria-label="`Add a widget to ${section.title ?? section.id}`" @click="addTo(section.id)">Add</UiButton>
          <SectionMenu :section="section" />
        </div>
        <SectionHeader v-else :section="section" />
      </template>
      <template #item="{ item, size }">
        <WidgetHost :item="item" :size="size" />
      </template>
    </StaticBoard>
    <TileEditor v-if="editingItem" :item="editingItem" :config="store.tiles.value[editingItem.id]" @save="saveTile" @close="editingId = ''" />
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
