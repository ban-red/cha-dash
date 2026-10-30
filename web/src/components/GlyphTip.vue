<script setup lang="ts">
/**
 * A tile's warning glyph you can ask about: hover or focus shows the warning
 * in a tooltip, and a click (or Enter) opens it whole in NoteDialog. The
 * tooltip lives in the top layer (the Popover API), so the tile's clipped
 * edges don't cut it; it sits above the glyph, or below when there's no room.
 * Leaving the glyph closes it after a moment, so the pointer can cross to
 * the tooltip; it stays while hovered, and a click on it opens the note too.
 * With the upstream's page for it (`href`), the dialog offers to open that.
 * Without the Popover API (older engines, test DOMs) the click still works.
 */
import { nextTick, onBeforeUnmount, ref, useId, useTemplateRef, watch } from 'vue';
import Glyph, { type GlyphKind } from './marks/Glyph.vue';
import { useUi } from '../stores/ui';

const { kind, text, title, href } = defineProps<{ kind: GlyphKind; text: string; title: string; href?: string | undefined }>();

const ui = useUi();
const id = useId();
const btn = useTemplateRef<HTMLButtonElement>('btn');
const tip = useTemplateRef<HTMLDivElement>('tip');
const shown = ref(false);
let timer: ReturnType<typeof setTimeout> | undefined;

function place(): void {
  const b = btn.value?.getBoundingClientRect();
  const t = tip.value;
  if (!b || !t) return;
  const gap = 8;
  const w = t.offsetWidth;
  const h = t.offsetHeight;
  const left = Math.min(Math.max(8, b.right - w), window.innerWidth - w - 8);
  const top = b.top - h - gap >= 8 ? b.top - h - gap : b.bottom + gap;
  t.style.left = `${Math.round(left)}px`;
  t.style.top = `${Math.round(top)}px`;
}

/** How long the tooltip waits after the pointer leaves, to let it reach the tooltip. */
const GRACE_MS = 300;

/** The tooltip exists only while shown: rendered, then raised to the top layer and placed. */
function show(delay = 180): void {
  clearTimeout(timer);
  if (shown.value) return;
  timer = setTimeout(async () => {
    if (typeof HTMLElement.prototype.showPopover !== 'function') return;
    shown.value = true;
    await nextTick();
    const t = tip.value;
    if (!t) return;
    // Vue's types don't know the attribute yet; it's set here.
    t.setAttribute('popover', 'manual');
    t.showPopover();
    place();
  }, delay);
}

function hide(): void {
  clearTimeout(timer);
  shown.value = false;
}

function hideSoon(): void {
  clearTimeout(timer);
  timer = setTimeout(hide, GRACE_MS);
}

function stay(): void {
  clearTimeout(timer);
}

function open(): void {
  hide();
  ui.showNote({ title, text, glyph: kind, ...(href && { href }) });
}

// Esc dismisses a tooltip that hover opened too, wherever focus is (WCAG 1.4.13).
function onKey(e: KeyboardEvent): void {
  if (e.key === 'Escape') hide();
}
watch(shown, (on) => (on ? document.addEventListener('keydown', onKey) : document.removeEventListener('keydown', onKey)));

onBeforeUnmount(() => {
  hide();
  document.removeEventListener('keydown', onKey);
});
</script>

<template>
  <button
    ref="btn"
    type="button"
    class="tg-mark gtip-btn"
    :aria-label="`${text} Show all`"
    :aria-describedby="shown ? id : undefined"
    @mouseenter="show()"
    @mouseleave="hideSoon"
    @focus="show(0)"
    @blur="hideSoon"
    @keydown.esc="hide"
    @click.stop="open"
  >
    <Glyph :kind="kind" />
  </button>
  <Teleport to="body">
    <div v-if="shown" :id="id" ref="tip" role="tooltip" class="gtip" @mouseenter="stay" @mouseleave="hideSoon" @click="open">
      <div class="gtip-head"><Glyph :kind="kind" /><span>{{ title }}</span></div>
      <p class="gtip-text">{{ text }}</p>
      <p class="gtip-hint" aria-hidden="true">Click to read in full</p>
    </div>
  </Teleport>
</template>

<style>
.gtip-btn {
  display: inline-flex;
  /* A 24 px target around the 8 px glyph (WCAG 2.5.8), without moving it. */
  margin: -8px;
  padding: 8px;
  border: 0;
  border-radius: var(--radius-sm);
  background: none;
  color: inherit;
  cursor: pointer;
}
.gtip-btn:hover {
  background: color-mix(in oklch, var(--text) 8%, transparent);
}
.gtip-btn:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 0;
}
.gtip {
  position: fixed;
  inset: auto;
  width: max-content;
  max-width: min(320px, calc(100vw - 16px));
  margin: 0;
  padding: 10px 12px;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-md);
  background: var(--surface-2);
  color: var(--text);
  box-shadow: var(--shadow-overlay);
  cursor: pointer;
}
.gtip:hover .gtip-hint {
  color: var(--text);
}
.gtip-head {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 4px;
  font: var(--weight-medium) var(--type-small) / 1.2 var(--font-ui);
}
.gtip-head .g {
  width: 8px;
  height: 8px;
  top: 0;
}
.gtip-text {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 6;
  line-clamp: 6;
  overflow: hidden;
  margin: 0;
  font-size: var(--type-small);
  line-height: 1.4;
  color: var(--text-2);
  overflow-wrap: anywhere;
}
.gtip-hint {
  margin: 6px 0 0;
  font: var(--weight-regular) var(--type-caption) / 1.2 var(--font-ui);
  color: var(--text-2);
}
</style>
