<script setup lang="ts">
/**
 * A dropdown menu: a trigger button and a panel in the top layer (the
 * Popover API), so a tile's clipped edges don't cut it. Without that API
 * (older engines, test DOMs) the panel is a fixed-position box. It opens
 * below the trigger's right edge, or above it when there's no room.
 *
 * The panel has views. It opens on `menu`; `goto(view)` shows another (a
 * submenu, a confirm, a small form) and Escape steps back to `menu`, then
 * closes. The default slot gets `{ view, goto, close }` and renders the
 * current view: a `role="menu"` of `.menu-item`s, a `.menu-confirm`, or
 * anything else. `close(true)` returns focus to the trigger.
 *
 * Focus: on each view, the `[data-focus]` element (an input is selected),
 * else the checked item, else the first. Back on `menu`, the item that
 * left it. Arrow keys, Home and End move between items (`[role^="menuitem"]`
 * and `[data-confirm]` buttons); Tab from an item closes the menu.
 *
 * `label` names the trigger and the panel. `#trigger` is the icon (three
 * dots by default). Class and other attributes fall through to the trigger
 * button; `popClass` goes on the panel. `v-model:view` and `close()` /
 * `goto()` through a template ref are for code outside the slot.
 */
import { nextTick, onBeforeUnmount, ref, useTemplateRef, watch } from 'vue';

defineOptions({ inheritAttrs: false });

const { label, popClass } = defineProps<{ label: string; popClass?: string | undefined }>();
const view = defineModel<string>('view', { default: 'menu' });
defineSlots<{
  trigger?(): unknown;
  default?(p: { view: string; goto: (v: string) => Promise<void>; close: (refocus?: boolean) => void }): unknown;
}>();

const open = ref(false);
const trigger = useTemplateRef<HTMLButtonElement>('trigger');
const pop = useTemplateRef<HTMLElement>('pop');
/** Which item of `menu` opened the current view, to focus it on the way back. */
let from = -1;
/** The item last clicked (or pressed with Enter or Space). Safari doesn't focus a clicked button. */
let picked = -1;

const ITEM = '[role^="menuitem"], [data-confirm]';
const items = (): HTMLElement[] => [...(pop.value?.querySelectorAll<HTMLElement>(ITEM) ?? [])];

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

async function focusFirst(back = -1): Promise<void> {
  await nextTick();
  place();
  const field = pop.value?.querySelector<HTMLElement>('[data-focus]');
  if (field) {
    field.focus();
    if (field instanceof HTMLInputElement) field.select();
    return;
  }
  const all = items();
  (all[back] ?? all.find((el) => el.getAttribute('aria-checked') === 'true') ?? all[0])?.focus();
}

async function show(): Promise<void> {
  view.value = 'menu';
  from = -1;
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

async function goto(v: string): Promise<void> {
  const back = v === 'menu' ? from : -1;
  if (view.value === 'menu') from = picked >= 0 ? picked : items().indexOf(document.activeElement as HTMLElement);
  view.value = v;
  await focusFirst(back);
}

function onDoc(e: Event): void {
  const t = e.target as Node;
  if (!pop.value?.contains(t) && !trigger.value?.contains(t)) close();
}
function onEscape(e: KeyboardEvent): void {
  if (e.key !== 'Escape') return;
  e.stopPropagation();
  if (view.value !== 'menu') void goto('menu');
  else close(true);
}
function listen(): void {
  document.addEventListener('pointerdown', onDoc);
  document.addEventListener('keydown', onEscape, true);
  // Native, not @click.capture: a Vue listener earlier in the path makes Vue skip handlers attached in the same tick.
  document.addEventListener('click', onPick, true);
}
function unlisten(): void {
  document.removeEventListener('pointerdown', onDoc);
  document.removeEventListener('keydown', onEscape, true);
  document.removeEventListener('click', onPick, true);
}
watch(open, (v) => (v ? listen() : unlisten()));
onBeforeUnmount(unlisten);

function onPick(e: Event): void {
  const el = (e.target as Element).closest<HTMLElement>(ITEM);
  picked = el ? items().indexOf(el) : -1;
}

function onKey(e: KeyboardEvent): void {
  const all = items();
  const i = all.indexOf(e.target as HTMLElement);
  // Keys in a form field, or on a control that isn't an item, are its own.
  if (i < 0) return;
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

defineExpose({ close, goto });
</script>

<template>
  <button
    v-bind="$attrs"
    ref="trigger"
    type="button"
    aria-haspopup="menu"
    :aria-expanded="open"
    :aria-label="label"
    :title="label"
    @click.stop="open ? close() : show()"
  >
    <slot name="trigger">
      <svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="3" r="1.3" /><circle cx="8" cy="8" r="1.3" /><circle cx="8" cy="13" r="1.3" /></svg>
    </slot>
  </button>
  <Teleport to="body">
    <div v-if="open" ref="pop" :class="['menu-pop', 'glass', popClass]" role="presentation" @click.stop @keydown="onKey">
      <slot :view="view" :goto="goto" :close="close" />
    </div>
  </Teleport>
</template>
