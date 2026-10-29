<script setup lang="ts">
/**
 * The board in edit mode: <LayoutBoard> from @chadash/layout-vue with drag,
 * snapping resize and keyboard moves, plus adding, editing and removing
 * tiles, and undo/redo over all of it. Loaded on demand from BoardSurface,
 * so motion and the gesture code never ride with a read-only front page;
 * the widget picker and the tile editor are chunks of their own again,
 * loaded on first use.
 *
 * - **Add:** the toolbar's "Add widget", a section's "Add", or ⌘K's "Add
 *   widget" (`ui.state.addingWidget`) opens the picker (WidgetPicker.vue);
 *   the widget lands in the first gap that fits.
 * - **Dismissed:** label discovery's dismissed services on this board, to
 *   offer again (DismissedServices.vue, lazy), when there are any.
 * - **Edit / Remove:** for the selected tile (the one last focused or
 *   grabbed), from the toolbar; Delete or Backspace removes it too. A
 *   tile's cog outside edit mode (`ui.state.editingTile`) opens its Edit. Removing
 *   never asks: Undo puts it back, config and all.
 * - **Flow:** how sections share the board (Sections, Bento, Mosaic), for
 *   everyone who views it. A mosaic is edited as bento (LayoutBoard).
 * - **History:** one step per gesture, add, edit or remove, over the board
 *   and its tiles together (useBoardHistory). Every step is shown and saved
 *   through the board store; on a 409 the store loads the other writer's
 *   board, which starts a fresh history because it isn't the present one.
 */
import { computed, defineAsyncComponent, nextTick, onMounted, ref, watch } from 'vue';
import { LayoutError, setFlow, type Board, type Breakpoint, type Flow, type Item } from '@chadash/layout';
import { LayoutBoard } from '@chadash/layout-vue';
import { onKeyStroke } from '@vueuse/core';
import SectionHeader from './SectionHeader.vue';
import { useBoardHistory } from './useBoardHistory';
import WidgetHost from '../widgets/WidgetHost.vue';
import { label } from '../model/graph';
import { labelContainers } from '../model/discovery';
import { addWidget, editTile, removeTile, sectionOf, targetOf, type AddRequest, type BoardState } from '../model/edit';
import { WIDGETS } from '../model/widgets';
import { useBoard } from '../stores/board';
import { useDiscoveryIfAny } from '../stores/discovery';
import { useLive } from '../stores/live';
import type { TileConfig } from '../stores/tiles';
import { useUi } from '../stores/ui';

const WidgetPicker = defineAsyncComponent(() => import('./WidgetPicker.vue'));
const TileEditor = defineAsyncComponent(() => import('./TileEditor.vue'));
const DismissedServices = defineAsyncComponent(() => import('./DismissedServices.vue'));

const { board, breakpoint } = defineProps<{ board: Board; breakpoint?: Breakpoint | undefined }>();
const emit = defineEmits<{ done: [] }>();

const store = useBoard();
const live = useLive();
const ui = useUi();
const discovery = useDiscoveryIfAny();
const history = useBoardHistory(() => ({ board, tiles: store.tiles.value }));
const present = history.present;

/** The tile the toolbar's Edit and Remove act on. */
const selected = ref('');
/** The picker, open on a section. */
const picking = ref<{ sectionId: string } | null>(null);
/** The tile being edited. */
const editingId = ref('');
/** The dismissed services' list is open. */
const showDismissed = ref(false);
const dismissedCount = computed(() => discovery?.dismissedServices.value.length ?? 0);

const items = computed(() => new Map(present.value.board.sections.flatMap((s) => s.items.map((i) => [i.id, i] as const))));
const selectedItem = computed(() => items.value.get(selected.value));
const editingItem = computed(() => items.value.get(editingId.value));

function persist(next: BoardState): void {
  // Tiles go along only when they changed: a drag leaves the hub's as they are.
  const saving = next.tiles !== store.tiles.value ? store.save(next.board, next.tiles) : store.save(next.board);
  saving.then(
    (saved) => {
      if (!saved) ui.toast('This board changed somewhere else. Showing the latest layout.', 'error');
    },
    () => ui.toast('Couldn’t save the layout. It stays here; the next change retries.', 'error'),
  );
}

function apply(next: BoardState): void {
  history.commit(next);
  persist(next);
}

function onCommit(next: Board): void {
  apply({ board: next, tiles: present.value.tiles });
}

function step(direction: 'undo' | 'redo'): void {
  const can = direction === 'undo' ? history.canUndo.value : history.canRedo.value;
  if (!can) return;
  persist(direction === 'undo' ? history.undo() : history.redo());
}

const FLOW_CHOICES: readonly { flow: Flow; name: string; hint: string }[] = [
  { flow: 'sections', name: 'Sections', hint: 'Sections side by side, in rows' },
  { flow: 'bento', name: 'Bento', hint: 'Sections fit together, filling the gaps' },
  { flow: 'mosaic', name: 'Mosaic', hint: 'Every tile fits together, section by section. Edited as Bento.' },
];
const flow = computed<Flow>(() => present.value.board.flow ?? 'sections');

function chooseFlow(next: Flow): void {
  if (next === flow.value) return;
  apply({ board: setFlow(present.value.board, next), tiles: present.value.tiles });
  const choice = FLOW_CHOICES.find((c) => c.flow === next);
  ui.toast(next === 'mosaic' ? 'Mosaic. You’ll see it when you’re done editing.' : `${choice?.name ?? next}.`);
}

/** "Queue: Sonarr", "Cameras": the name in labels, announcements and toasts. */
function itemLabel(item: Pick<Item, 'id' | 'type'>): string {
  const name = WIDGETS[item.type]?.name ?? item.type;
  const own = present.value.tiles[item.id]?.name;
  if (own) return own === name ? name : `${name}: ${own}`;
  const target = targetOf(item.id, present.value.tiles);
  const r = target ? live.graph.value?.resources[target] : undefined;
  return r ? `${name}: ${label(r)}` : name;
}

function openPicker(sectionId?: string): void {
  const fallback = (selected.value && sectionOf(present.value.board, selected.value)) || present.value.board.sections[0]?.id || '';
  picking.value = { sectionId: sectionId ?? fallback };
}

async function focusItem(id: string): Promise<void> {
  await nextTick();
  const el = [...document.querySelectorAll<HTMLElement>('[data-item-id]')].find((e) => e.dataset['itemId'] === id);
  el?.focus();
}

function onAdd(req: AddRequest): void {
  try {
    const next = addWidget(present.value, req);
    picking.value = null;
    apply({ board: next.board, tiles: next.tiles });
    selected.value = next.id;
    const section = next.board.sections.find((s) => s.id === req.sectionId);
    ui.toast(`Added ${itemLabel({ id: next.id, type: req.spec.type })} to ${section?.title ?? 'the board'}.`);
    void focusItem(next.id);
  } catch (err) {
    ui.toast(err instanceof LayoutError ? `${req.spec.name} doesn’t fit there. Pick a smaller size or another section.` : (err as Error).message, 'error');
  }
}

function onEdit(config: TileConfig | undefined): void {
  const id = editingId.value;
  editingId.value = '';
  if (!items.value.has(id)) return;
  apply(editTile(present.value, id, config));
  void focusItem(id);
}

/** Where focus goes when a tile is removed: its neighbour in the section, else the first tile left. */
function neighbourOf(id: string): string | undefined {
  const section = present.value.board.sections.find((s) => s.items.some((i) => i.id === id));
  const at = section?.items.findIndex((i) => i.id === id) ?? -1;
  const near = section?.items[at + 1] ?? section?.items[at - 1];
  return near?.id ?? [...items.value.keys()].find((k) => k !== id);
}

function removeItem(id: string): void {
  const item = items.value.get(id);
  if (!item) return;
  const name = itemLabel(item);
  const config = present.value.tiles[id];
  const next = neighbourOf(id);
  apply(removeTile(present.value, id));
  if (selected.value === id) selected.value = '';
  if (next) void focusItem(next);
  ui.toast(`Removed ${name}. Undo puts it back.`);
  // A tile label discovery suggested and you removed isn't suggested again:
  // its container, or, for a catalog widget showing the container's app, the
  // containers its labels name.
  const origin = config?.origin;
  if (origin?.source === 'labels' && discovery) {
    const found = config?.bind?.resource ? [config.bind.resource] : labelContainers(live.graph.value, origin.group ?? '', origin.entry ?? '');
    if (found.length) discovery.remember(found).catch(() => undefined);
  }
}

// ⌘K → "Add widget", or a section header's Add: open the picker once edit mode is here.
watch(
  () => ui.state.addingWidget,
  (asked) => {
    if (!asked) return;
    ui.state.addingWidget = false;
    const section = ui.state.addSection;
    ui.state.addSection = '';
    openPicker(section || undefined);
  },
  { immediate: true },
);
// A tile's cog → its Edit: select it and open the editor.
watch(
  () => ui.state.editingTile,
  (id) => {
    if (!id) return;
    ui.state.editingTile = '';
    if (!items.value.has(id)) return;
    selected.value = id;
    editingId.value = id;
  },
  { immediate: true },
);
onMounted(() => {
  discovery?.ensureLoaded();
  // A widget picked on the normal board: add it now, as a step of this session.
  const req = ui.state.pendingAdd;
  ui.state.pendingAdd = undefined;
  if (req) onAdd(req);
});

/** Delete or Backspace on a focused tile removes it (not while it's picked up). */
function onKeydown(e: KeyboardEvent): void {
  if ((e.key !== 'Delete' && e.key !== 'Backspace') || e.metaKey || e.ctrlKey || e.altKey) return;
  const el = e.target instanceof HTMLElement ? e.target : null;
  const id = el?.dataset['itemId'];
  if (!id || el?.getAttribute('aria-pressed') === 'true') return;
  e.preventDefault();
  removeItem(id);
}

onKeyStroke('z', (e) => {
  if (!(e.metaKey || e.ctrlKey) || e.altKey || picking.value || editingId.value) return;
  e.preventDefault();
  step(e.shiftKey ? 'redo' : 'undo');
});
</script>

<template>
  <div class="edit-bar" role="toolbar" aria-label="Edit layout">
    <span v-if="selectedItem" class="edit-sel">
      <span class="edit-sel-name">{{ itemLabel(selectedItem) }}</span>
      <button type="button" class="btn sm" @click="editingId = selectedItem.id">Edit</button>
      <button type="button" class="btn sm" @click="removeItem(selectedItem.id)">Remove</button>
    </span>
    <span v-else class="edit-hint">Drag a tile, or resize it from its corner. Select one to edit or remove it. With the keyboard, Enter picks a tile up and the arrows move it.</span>
    <span v-if="store.state.unsaved" class="edit-unsaved" role="status">Not saved</span>
    <div class="flow-seg" role="group" aria-label="Layout">
      <button v-for="c in FLOW_CHOICES" :key="c.flow" type="button" :aria-pressed="flow === c.flow" :title="c.hint" @click="chooseFlow(c.flow)">{{ c.name }}</button>
    </div>
    <button type="button" class="btn sm" @click="openPicker()">Add widget</button>
    <button v-if="dismissedCount" type="button" class="btn ghost sm" @click="showDismissed = true">Dismissed · {{ dismissedCount }}</button>
    <button type="button" class="btn ghost sm" :disabled="!history.canUndo.value" @click="step('undo')">Undo</button>
    <button type="button" class="btn ghost sm" :disabled="!history.canRedo.value" @click="step('redo')">Redo</button>
    <button type="button" class="btn primary sm" @click="emit('done')">Done</button>
  </div>
  <div class="edit-surface" @keydown="onKeydown">
    <LayoutBoard
      :board="present.board"
      v-bind="breakpoint ? { breakpoint } : {}"
      :editing="true"
      :item-label="itemLabel"
      @commit="onCommit"
      @select="selected = $event"
    >
      <template #section="{ section }">
        <div class="sh-edit">
          <SectionHeader :section="section" />
          <button type="button" class="btn ghost sm sh-add" :aria-label="`Add a widget to ${section.title ?? section.id}`" @click="openPicker(section.id)">Add</button>
        </div>
      </template>
      <template #item="{ item, size }">
        <WidgetHost :item="item" :size="size" />
      </template>
    </LayoutBoard>
  </div>
  <WidgetPicker v-if="picking" :board="present.board" :tiles="present.tiles" :section-id="picking.sectionId" @add="onAdd" @close="picking = null" />
  <TileEditor v-if="editingItem" :item="editingItem" :config="present.tiles[editingItem.id]" @save="onEdit" @close="editingId = ''" />
  <DismissedServices v-if="showDismissed && discovery" @close="showDismissed = false" />
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
.edit-sel {
  flex: 1 1 18rem;
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
}
.edit-sel-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font: var(--weight-medium) var(--type-small) / 1.2 var(--font-ui);
}
.edit-unsaved {
  font-size: var(--type-caption);
  color: var(--attention-text);
}
/* Layout: Sections, Bento, Mosaic. */
.flow-seg {
  display: flex;
  padding: 2px;
  border-radius: calc(var(--radius-md) + 2px);
  background: color-mix(in oklch, var(--text) 7%, transparent);
}
.flow-seg button {
  padding: 5px 10px;
  border: 0;
  border-radius: var(--radius-md);
  background: transparent;
  font: var(--weight-medium) var(--type-small) / 1 var(--font-ui);
  color: var(--text-2);
  white-space: nowrap;
  cursor: pointer;
}
.flow-seg button:hover {
  color: var(--text);
}
.flow-seg button[aria-pressed='true'] {
  background: var(--surface);
  color: var(--text);
  box-shadow:
    0 0 0 var(--hairline) var(--line-2),
    0 1px 2px color-mix(in oklch, var(--bg) 60%, transparent);
}
.flow-seg button:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.sh-edit {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  width: 100%;
  min-width: 0;
}
.sh-edit > .sh {
  flex: 1;
  min-width: 0;
}
.sh-add {
  flex: none;
}
</style>
