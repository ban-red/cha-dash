<script setup lang="ts">
/**
 * A tile's menu: three dots top-right, shown on hover or focus (on a touch
 * screen, once the tile has focus). It opens a dropdown with
 *
 * - **Integration settings:** the integration that feeds the tile
 *   (model/integrations.ts → tileInstance), for a viewer who may open them;
 * - **Widget settings:** the tile's own Edit (name, link, icon, binding);
 * - **Resize:** the sizes the widget has that fit its section, saved at once;
 * - **Remove widget:** asks first, then removes it and its config, with Undo.
 *
 * Widget settings, Resize and Remove need a role that may edit boards. The menu is absent
 * outside the app, in edit mode (which has its own toolbar and handles), and
 * for a role that may do none of these. It lives in the top layer (the
 * Popover API), so the tile's clipped edges don't cut it; without that API
 * (older engines, test DOMs) it is a fixed-position box.
 */
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue';
import { findItem, resize, SIZES, sectionColumns, type SizeId } from '@chadash/layout';
import { removeTile, sizeName } from '../model/edit';
import { tileInstance } from '../model/integrations';
import { useBoardIfAny } from '../stores/board';
import { useLive } from '../stores/live';
import { useTile } from '../stores/tiles';
import { useUiIfAny } from '../stores/ui';

const { name } = defineProps<{ name: string }>();
const live = useLive();
const tile = useTile();
const ui = useUiIfAny();
const store = useBoardIfAny();

const instance = computed(() => tileInstance(live.graph.value, tile.type, tile.config.value, tile.target));
const live_ = computed(() => Boolean(ui && tile.itemId && !ui.state.editing));
const integration = computed(() => (live_.value && instance.value && ui?.can.value.integrations ? instance.value : undefined));
const widget = computed(() => live_.value && Boolean(ui?.can.value.editBoards));
const located = computed(() => (store?.board.value && tile.itemId ? findItem(store.board.value, tile.itemId) : undefined));
const canEdit = computed(() => Boolean(ui && !ui.state.editing && ui.can.value.editBoards && located.value));
const sizes = computed<SizeId[]>(() => {
  const loc = located.value;
  if (!loc) return [];
  const columns = sectionColumns(loc.section, 'desktop');
  return loc.item.supportedSizes.filter((s) => SIZES[s].w <= columns);
});
const current = computed(() => located.value?.item.size);
const canResize = computed(() => canEdit.value && sizes.value.length > 1);
const shown = computed(() => Boolean(integration.value || widget.value || canEdit.value));

const open = ref(false);
const view = ref<'menu' | 'resize' | 'remove'>('menu');
const trigger = ref<HTMLButtonElement | null>(null);
const pop = ref<HTMLElement | null>(null);

function items(): HTMLElement[] {
  return [...(pop.value?.querySelectorAll<HTMLElement>('[role^="menuitem"], [data-confirm]') ?? [])];
}

function place(): void {
  const b = trigger.value?.getBoundingClientRect();
  const p = pop.value;
  if (!b || !p) return;
  const w = p.offsetWidth;
  const h = p.offsetHeight;
  const left = Math.min(Math.max(8, b.right - w), window.innerWidth - w - 8);
  const top = b.bottom + 6 + h <= window.innerHeight - 8 ? b.bottom + 6 : Math.max(8, b.top - h - 6);
  p.style.left = `${Math.round(left)}px`;
  p.style.top = `${Math.round(top)}px`;
}

async function focusFirst(): Promise<void> {
  await nextTick();
  place();
  const all = items();
  // The current size, else the first item (Cancel, when asked to confirm).
  (all.find((el) => el.getAttribute('aria-checked') === 'true') ?? all[0])?.focus();
}

async function show(): Promise<void> {
  view.value = 'menu';
  open.value = true;
  await nextTick();
  const p = pop.value;
  if (p && typeof HTMLElement.prototype.showPopover === 'function') {
    p.setAttribute('popover', 'manual');
    p.showPopover();
  }
  await focusFirst();
}

function close(refocus = false): void {
  open.value = false;
  if (refocus) trigger.value?.focus();
}

function onDoc(e: Event): void {
  const t = e.target as Node;
  if (!pop.value?.contains(t) && !trigger.value?.contains(t)) close();
}
function onKey(e: KeyboardEvent): void {
  if (e.key !== 'Escape') return;
  e.stopPropagation();
  if (view.value !== 'menu') void goto('menu');
  else close(true);
}
watch(open, (v) => {
  if (v) {
    document.addEventListener('pointerdown', onDoc);
    document.addEventListener('keydown', onKey, true);
  } else {
    document.removeEventListener('pointerdown', onDoc);
    document.removeEventListener('keydown', onKey, true);
  }
});
onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onDoc);
  document.removeEventListener('keydown', onKey, true);
});

async function goto(v: 'menu' | 'resize' | 'remove'): Promise<void> {
  view.value = v;
  await focusFirst();
}

function onMenuKey(e: KeyboardEvent): void {
  const all = items();
  const i = all.indexOf(document.activeElement as HTMLElement);
  let next = -1;
  if (e.key === 'ArrowDown') next = (i + 1) % all.length;
  else if (e.key === 'ArrowUp') next = (i - 1 + all.length) % all.length;
  else if (e.key === 'Home') next = 0;
  else if (e.key === 'End') next = all.length - 1;
  else if (e.key === 'Tab') close();
  if (next < 0) return;
  e.preventDefault();
  all[next]?.focus();
}

function openIntegration(): void {
  close();
  ui?.openSettings('integrations', integration.value);
}

function openWidget(): void {
  close();
  ui?.openTileSettings(tile.itemId, undefined);
}

function saved(promise: Promise<boolean>): void {
  promise.then(
    (ok) => {
      if (!ok) ui?.toast('This board changed somewhere else. Showing the latest layout.', 'error');
    },
    () => ui?.toast('Couldn’t save the layout.', 'error'),
  );
}

function pickSize(size: SizeId): void {
  const board = store?.board.value;
  close(true);
  if (!board || size === current.value) return;
  try {
    saved(store!.save(resize(board, tile.itemId, size)));
    ui?.toast(`${name} is now ${sizeName(size)}.`);
  } catch {
    ui?.toast(`${name} doesn’t fit there as ${sizeName(size)}.`, 'error');
  }
}

function remove(): void {
  const board = store?.board.value;
  const tiles = store?.tiles.value;
  close();
  if (!store || !board || !tiles) return;
  const next = removeTile({ board, tiles }, tile.itemId);
  saved(store.save(next.board, next.tiles));
  ui?.toast(`Removed ${name}.`, 'ok', 8000, { label: 'Undo', run: () => saved(store.save(board, tiles)) });
}
</script>

<template>
  <div v-if="shown" class="tile-menu">
    <button
      ref="trigger"
      type="button"
      class="tile-cog"
      aria-haspopup="menu"
      :aria-expanded="open"
      :aria-label="`${name} menu`"
      :title="`${name} menu`"
      @click.stop="open ? close() : show()"
    >
      <svg viewBox="0 0 16 16" aria-hidden="true">
        <circle cx="8" cy="3" r="1.3" /><circle cx="8" cy="8" r="1.3" /><circle cx="8" cy="13" r="1.3" />
      </svg>
    </button>
    <Teleport to="body">
      <div v-if="open" ref="pop" class="tile-pop glass" role="presentation" @click.stop>
        <div v-if="view === 'menu'" role="menu" :aria-label="`${name} menu`" @keydown="onMenuKey">
          <button v-if="integration" type="button" role="menuitem" class="menu-item" tabindex="-1" @click="openIntegration">
            <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M6 2v3M10 2v3M4.5 5h7v2.5a3.5 3.5 0 0 1-7 0zM8 11v3" /></svg>
            <span>Integration settings</span>
          </button>
          <button v-if="widget" type="button" role="menuitem" class="menu-item" tabindex="-1" @click="openWidget">
            <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M2.5 4.5h1.4M7.1 4.5h6.4M2.5 11.5h6.4M12.1 11.5h1.4" /><circle cx="5.5" cy="4.5" r="1.6" /><circle cx="10.5" cy="11.5" r="1.6" /></svg>
            <span>Widget settings</span>
          </button>
          <button v-if="canResize" type="button" role="menuitem" aria-haspopup="menu" class="menu-item" tabindex="-1" @click="goto('resize')">
            <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M9.5 2.5h4v4M13.5 2.5 9 7M6.5 13.5h-4v-4M2.5 13.5 7 9" /></svg>
            <span>Resize</span>
            <span class="menu-val">{{ current ? sizeName(current) : '' }}<svg viewBox="0 0 16 16" aria-hidden="true"><path d="m6 3.5 4.5 4.5L6 12.5" /></svg></span>
          </button>
          <div v-if="canEdit" class="menu-sep" role="separator"></div>
          <button v-if="canEdit" type="button" role="menuitem" class="menu-item danger" tabindex="-1" @click="goto('remove')">
            <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.6 8h5.8l.6-8M7 7v3.5M9 7v3.5" /></svg>
            <span>Remove widget</span>
          </button>
        </div>
        <div v-else-if="view === 'resize'" role="menu" :aria-label="`Resize ${name}`" @keydown="onMenuKey">
          <button
            v-for="s in sizes"
            :key="s"
            type="button"
            role="menuitemradio"
            class="menu-item"
            tabindex="-1"
            :aria-checked="s === current"
            @click="pickSize(s)"
          >
            <svg viewBox="0 0 16 16" aria-hidden="true"><path v-if="s === current" d="m3.5 8.5 3 3 6-7" /></svg>
            <span>{{ sizeName(s) }}</span>
          </button>
        </div>
        <div v-else role="alertdialog" :aria-label="`Remove ${name}?`" class="tile-confirm" @keydown="onMenuKey">
          <p>Remove <b>{{ name }}</b> from the board? Its settings on this tile go too.</p>
          <div class="tile-confirm-acts">
            <button type="button" class="btn sm" data-confirm @click="goto('menu')">Cancel</button>
            <button type="button" class="btn sm danger" data-confirm @click="remove">Remove</button>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style>
.tile-menu {
  display: contents;
}
.tile-pop {
  position: fixed;
  inset: auto;
  z-index: 60;
  width: 196px;
  margin: 0;
  padding: 4px;
  border-radius: var(--radius-md);
  color: var(--text);
}
.tile-pop .menu-item {
  justify-content: flex-start;
  gap: 8px;
  min-height: 28px;
  padding: 0 8px;
  border-radius: var(--radius-sm);
  font-size: var(--type-small);
}
.tile-pop .menu-item svg {
  flex: none;
  width: 14px;
  height: 14px;
  stroke: currentColor;
  fill: none;
  stroke-width: 1.4;
  stroke-linecap: round;
  stroke-linejoin: round;
  color: var(--text-3);
}
.tile-pop .menu-item .menu-val {
  margin-left: auto;
  font-size: var(--type-caption);
}
.tile-pop .menu-item .menu-val svg {
  width: 10px;
  height: 10px;
}
.tile-pop .menu-sep {
  height: var(--hairline);
  margin: 3px 4px;
  background: var(--line);
}
.tile-pop .menu-item.danger svg {
  color: inherit;
}
.tile-pop .menu-item.danger {
  color: var(--down);
}
.tile-confirm p {
  margin: 0 0 var(--space-2);
  padding: 4px 6px 0;
  font-size: var(--type-small);
  line-height: 1.4;
  color: var(--text-2);
}
.tile-confirm-acts {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-2);
}
.tile-confirm .btn.danger {
  color: var(--down);
}
</style>
