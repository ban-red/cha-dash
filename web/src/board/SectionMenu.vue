<script setup lang="ts">
/**
 * A section header's menu: three dots at the header's right end, shown on
 * hover or focus (always on touch), with
 *
 * - **Section settings:** rename the section (an empty name clears the title);
 * - **Resize:** the section's width as a share of the 12-column board, from a
 *   quarter to the full row. Only widths the section's widest tile fits are
 *   offered;
 * - **Reorder:** move it up or down one place, or to the top or bottom (only
 *   the moves that change something).
 *
 * - **Add section before / after:** an empty, full-width section next to this one;
 * - **Remove section:** asks first, then removes the section and every widget
 *   in it, with Undo.
 *
 * Each choice saves at once.
 * Same dropdown as a tile's (components/TileCog.vue, whose `.tile-pop` styles
 * it shares): the top layer, arrow keys, Escape to step back.
 */
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue';
import { addSection, moveSection, setSectionSpan, setSectionTitle, SIZES, CANONICAL_COLUMNS } from '@chadash/layout';
import { removeSectionAndTiles } from '../model/edit';
import { useBoardIfAny } from '../stores/board';
import { useUiIfAny } from '../stores/ui';

const { section } = defineProps<{ section: { id: string; title?: string | undefined } }>();
const store = useBoardIfAny();
const ui = useUiIfAny();

const WIDTHS: readonly { span: number; name: string }[] = [
  { span: 12, name: 'Full width' },
  { span: 9, name: 'Three quarters' },
  { span: 8, name: 'Two thirds' },
  { span: 6, name: 'Half' },
  { span: 4, name: 'One third' },
  { span: 3, name: 'One quarter' },
];

const title = computed(() => section.title ?? section.id);
const located = computed(() => store?.board.value?.sections.find((s) => s.id === section.id));
const current = computed(() => located.value?.span);
const narrowest = computed(() => Math.max(1, ...(located.value?.items.map((i) => SIZES[i.size].w) ?? [1])));
const widths = computed(() => {
  const list = WIDTHS.filter((w) => w.span >= narrowest.value && w.span <= CANONICAL_COLUMNS);
  const at = current.value;
  if (at && !list.some((w) => w.span === at)) list.push({ span: at, name: `${at} of ${CANONICAL_COLUMNS} columns` });
  return list.sort((a, b) => b.span - a.span);
});
const index = computed(() => store?.board.value?.sections.findIndex((s) => s.id === section.id) ?? -1);
const count = computed(() => store?.board.value?.sections.length ?? 0);
const moves = computed(() => {
  const i = index.value;
  const last = count.value - 1;
  const all = [
    { id: 'top', name: 'Move to top', to: 0, show: i > 0, d: 'M3 3h10M8 13V6M5 9l3-3 3 3' },
    { id: 'up', name: 'Move up', to: i - 1, show: i > 0, d: 'M8 13V3M4.5 6.5 8 3l3.5 3.5' },
    { id: 'down', name: 'Move down', to: i + 1, show: i >= 0 && i < last, d: 'M8 3v10M4.5 9.5 8 13l3.5-3.5' },
    { id: 'bottom', name: 'Move to bottom', to: last, show: i >= 0 && i < last, d: 'M3 13h10M8 3v7M5 7l3 3 3-3' },
  ];
  return all.filter((m) => m.show);
});
const shown = computed(() => Boolean(ui?.can.value.editBoards && !ui.state.editing && located.value));

const open = ref(false);
const view = ref<'menu' | 'resize' | 'reorder' | 'remove' | 'settings'>('menu');
const trigger = ref<HTMLButtonElement | null>(null);
const pop = ref<HTMLElement | null>(null);

const items = (): HTMLElement[] => [...(pop.value?.querySelectorAll<HTMLElement>('[role^="menuitem"], [data-confirm]') ?? [])];
const name = ref('');
const widgetCount = computed(() => located.value?.items.length ?? 0);

function place(): void {
  const b = trigger.value?.getBoundingClientRect();
  const p = pop.value;
  if (!b || !p) return;
  const w = p.offsetWidth;
  const h = p.offsetHeight;
  p.style.left = `${Math.round(Math.min(Math.max(8, b.right - w), window.innerWidth - w - 8))}px`;
  p.style.top = `${Math.round(b.bottom + 6 + h <= window.innerHeight - 8 ? b.bottom + 6 : Math.max(8, b.top - h - 6))}px`;
}

async function focusFirst(): Promise<void> {
  await nextTick();
  place();
  const all = items();
  const field = pop.value?.querySelector<HTMLInputElement>('[data-focus]');
  if (field) {
    field.focus();
    field.select();
    return;
  }
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

async function goto(v: 'menu' | 'resize' | 'reorder' | 'remove' | 'settings'): Promise<void> {
  if (v === 'settings') name.value = section.title ?? '';
  view.value = v;
  await focusFirst();
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

function move(to: number, name: string): void {
  const board = store?.board.value;
  close(true);
  if (!board) return;
  store!.save(moveSection(board, section.id, to)).then(
    (ok) => {
      if (!ok) ui?.toast('This board changed somewhere else. Showing the latest layout.', 'error');
    },
    () => ui?.toast('Couldn’t save the layout.', 'error'),
  );
  ui?.toast(`${title.value}: ${name.toLowerCase()}.`);
}

function rename(): void {
  const board = store?.board.value;
  const next = name.value.trim();
  close(true);
  if (!store || !board || next === (section.title ?? '')) return;
  store.save(setSectionTitle(board, section.id, next)).then(
    (ok) => {
      if (!ok) ui?.toast('This board changed somewhere else. Showing the latest layout.', 'error');
    },
    () => ui?.toast('Couldn’t save the layout.', 'error'),
  );
  ui?.toast(next ? `Renamed the section to ${next}.` : `Cleared the name of ${title.value}.`);
}

function addNear(offset: 0 | 1): void {
  const board = store?.board.value;
  close(true);
  if (!store || !board || index.value < 0) return;
  const taken = new Set(board.sections.map((x) => x.id));
  let n = board.sections.length + 1;
  while (taken.has(`section-${n}`)) n++;
  store.save(addSection(board, { id: `section-${n}`, title: 'New section', span: CANONICAL_COLUMNS, items: [] }, index.value + offset)).then(
    (ok) => {
      if (!ok) ui?.toast('This board changed somewhere else. Showing the latest layout.', 'error');
    },
    () => ui?.toast('Couldn’t save the layout.', 'error'),
  );
  ui?.toast(`Added a new section ${offset ? 'after' : 'before'} ${title.value}.`);
}

function remove(): void {
  const board = store?.board.value;
  const tiles = store?.tiles.value;
  close();
  if (!store || !board || !tiles) return;
  const next = removeSectionAndTiles({ board, tiles }, section.id);
  const report = (p: Promise<boolean>): void => {
    p.then(
      (ok) => {
        if (!ok) ui?.toast('This board changed somewhere else. Showing the latest layout.', 'error');
      },
      () => ui?.toast('Couldn’t save the layout.', 'error'),
    );
  };
  report(store.save(next.board, next.tiles));
  ui?.toast(`Removed ${title.value}.`, 'ok', 8000, { label: 'Undo', run: () => report(store.save(board, tiles)) });
}

function pick(span: number): void {
  const board = store?.board.value;
  close(true);
  if (!board || span === current.value) return;
  try {
    store!.save(setSectionSpan(board, section.id, span)).then(
      (ok) => {
        if (!ok) ui?.toast('This board changed somewhere else. Showing the latest layout.', 'error');
      },
      () => ui?.toast('Couldn’t save the layout.', 'error'),
    );
    ui?.toast(`${title.value} is now ${widths.value.find((w) => w.span === span)?.name.toLowerCase() ?? `${span} columns`}.`);
  } catch {
    ui?.toast(`${title.value} can’t be that narrow with its tiles.`, 'error');
  }
}
</script>

<template>
  <template v-if="shown">
    <button
      ref="trigger"
      type="button"
      class="sh-menu"
      aria-haspopup="menu"
      :aria-expanded="open"
      :aria-label="`${title} section menu`"
      :title="`${title} section menu`"
      @click.stop="open ? close() : show()"
    >
      <svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="3" r="1.3" /><circle cx="8" cy="8" r="1.3" /><circle cx="8" cy="13" r="1.3" /></svg>
    </button>
    <Teleport to="body">
      <div v-if="open" ref="pop" class="tile-pop glass" role="presentation" @click.stop>
        <div v-if="view === 'menu'" role="menu" :aria-label="`${title} section menu`" @keydown="onMenuKey">
          <button type="button" role="menuitem" aria-haspopup="dialog" class="menu-item" tabindex="-1" @click="goto('settings')">
            <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M2.5 4.5h6M11.5 4.5h2M2.5 11.5h2M7.5 11.5h6M10 3v3M5.5 10v3" /></svg>
            <span>Section settings</span>
          </button>
          <button type="button" role="menuitem" aria-haspopup="menu" class="menu-item" tabindex="-1" @click="goto('resize')">
            <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M2.5 8h11M5 5.5 2.5 8 5 10.5M11 5.5 13.5 8 11 10.5" /></svg>
            <span>Resize</span>
            <span class="menu-val">{{ widths.find((w) => w.span === current)?.name ?? '' }}<svg viewBox="0 0 16 16" aria-hidden="true"><path d="m6 3.5 4.5 4.5L6 12.5" /></svg></span>
          </button>
          <button v-if="moves.length" type="button" role="menuitem" aria-haspopup="menu" class="menu-item" tabindex="-1" @click="goto('reorder')">
            <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M8 2.5v11M4.5 6 8 2.5 11.5 6M4.5 10 8 13.5 11.5 10" /></svg>
            <span>Reorder</span>
            <span class="menu-val">{{ index + 1 }} of {{ count }}<svg viewBox="0 0 16 16" aria-hidden="true"><path d="m6 3.5 4.5 4.5L6 12.5" /></svg></span>
          </button>
          <div class="menu-sep" role="separator"></div>
          <button type="button" role="menuitem" class="menu-item" tabindex="-1" @click="addNear(0)">
            <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M3 3h10M8 13V6.5M5 9.5h6" /></svg>
            <span>Add new section before</span>
          </button>
          <button type="button" role="menuitem" class="menu-item" tabindex="-1" @click="addNear(1)">
            <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M3 13h10M8 3v6.5M5 6.5h6" /></svg>
            <span>Add new section after</span>
          </button>
          <div class="menu-sep" role="separator"></div>
          <button type="button" role="menuitem" class="menu-item danger" tabindex="-1" @click="goto('remove')">
            <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.6 8h5.8l.6-8M7 7v3.5M9 7v3.5" /></svg>
            <span>Remove section</span>
          </button>
        </div>
        <form v-else-if="view === 'settings'" class="tile-confirm sec-settings" :aria-label="`${title} settings`" @submit.prevent="rename">
          <label for="sec-name">Name</label>
          <input id="sec-name" v-model="name" data-focus type="text" maxlength="80" autocomplete="off" placeholder="No name" />
          <div class="tile-confirm-acts">
            <button type="button" class="btn sm" @click="goto('menu')">Cancel</button>
            <button type="submit" class="btn sm">Save</button>
          </div>
        </form>
        <div v-else-if="view === 'remove'" role="alertdialog" :aria-label="`Remove ${title}?`" class="tile-confirm" @keydown="onMenuKey">
          <p>
            Remove <b>{{ title }}</b>{{ widgetCount ? ` and its ${widgetCount} ${widgetCount === 1 ? 'widget' : 'widgets'}` : '' }} from the board? Their settings go too.
          </p>
          <div class="tile-confirm-acts">
            <button type="button" class="btn sm" data-confirm @click="goto('menu')">Cancel</button>
            <button type="button" class="btn sm danger" data-confirm @click="remove">Remove</button>
          </div>
        </div>
        <div v-else-if="view === 'reorder'" role="menu" :aria-label="`Reorder ${title}`" @keydown="onMenuKey">
          <button v-for="m in moves" :key="m.id" type="button" role="menuitem" class="menu-item" tabindex="-1" @click="move(m.to, m.name)">
            <svg viewBox="0 0 16 16" aria-hidden="true"><path :d="m.d" /></svg>
            <span>{{ m.name }}</span>
          </button>
        </div>
        <div v-else role="menu" :aria-label="`Resize ${title}`" @keydown="onMenuKey">
          <button v-for="w in widths" :key="w.span" type="button" role="menuitemradio" class="menu-item" tabindex="-1" :aria-checked="w.span === current" @click="pick(w.span)">
            <svg viewBox="0 0 16 16" aria-hidden="true"><path v-if="w.span === current" d="m3.5 8.5 3 3 6-7" /></svg>
            <span>{{ w.name }}</span>
          </button>
        </div>
      </div>
    </Teleport>
  </template>
</template>

<style>
.sec-settings label {
  display: block;
  padding: 4px 6px 2px;
  font-size: var(--type-small);
  color: var(--text-2);
}
.sec-settings input {
  width: 100%;
  margin-bottom: var(--space-2);
  box-sizing: border-box;
}
</style>
