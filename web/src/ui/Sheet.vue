<script setup lang="ts">
/**
 * A modal sheet over the page: a glass panel at the top right on desktop
 * (over a clear scrim), a bottom card on a phone (under 640 px) that a flick
 * or a long drag down dismisses. motion-v plays it in and out, so the parent
 * mounts it once and toggles `open`; that keeps the exit.
 *
 * `from` is how it arrives on desktop: `top` drops in from the corner (the
 * Inbox), `right` slides in (a resource's sheet). `size`: `md` 460 px wide,
 * `lg` 520. Padding is the panel's own: set `--sheet-pad` on its class.
 *
 * It emits `close` on Esc (unless a layer above it, a confirm or a modal
 * <dialog>, takes the Esc), on the scrim and on the drag. Tab stays inside.
 * On open it emits `open` with the panel; focus goes to the panel unless that
 * handler moves it inside. On close, focus goes back to where it was.
 *
 * Class and other attributes fall through to the panel. Styles are in
 * sheet.css, which loads with the first sheet's chunk.
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { AnimatePresence, motion } from 'motion-v';
import { transition, useReducedMotion } from '../lib/motion';
import { layerAbove, trapTab } from '../lib/trap';
import './sheet.css';

defineOptions({ inheritAttrs: false });

const {
  open,
  from = 'top',
  size = 'md',
} = defineProps<{
  open: boolean;
  from?: 'top' | 'right';
  size?: 'md' | 'lg';
}>();
const emit = defineEmits<{ close: []; open: [panel: HTMLElement] }>();

const reduced = useReducedMotion();
const compact = ref(typeof window !== 'undefined' && window.innerWidth < 640);
const panel = ref<{ $el?: HTMLElement } | HTMLElement | null>(null);
const el = (): HTMLElement | undefined => (panel.value instanceof HTMLElement ? panel.value : panel.value?.$el);
let lastFocus: Element | null = null;

const away = computed(() => (from === 'right' ? { opacity: 0, x: 24 } : { opacity: 0, scale: 0.96, y: -8 }));
const leave = computed(() => (from === 'right' ? { opacity: 0, x: 16 } : { opacity: 0, scale: 0.97, y: -6 }));
const enter = computed(() => (reduced.value ? { opacity: 0 } : compact.value ? { y: '100%' } : away.value));
const exit = computed(() => (reduced.value ? { opacity: 0 } : compact.value ? { y: '100%' } : leave.value));

function onKey(e: KeyboardEvent): void {
  if (!open || e.defaultPrevented || layerAbove({})) return;
  if (e.key === 'Escape') emit('close');
  // It's modal (the scrim closes it), so Tab stays inside. Tab in a layer above it (the command bar) is that layer's.
  else if (e.key === 'Tab' && (e.target === document.body || el()?.contains(e.target as Node))) trapTab(e, el());
}
function onResize(): void {
  compact.value = window.innerWidth < 640;
}
/** A flick or a long drag down dismisses the card (momentum projects the release). */
function onDragEnd(_e: PointerEvent, info: { offset: { y: number }; velocity: { y: number } }): void {
  if (info.offset.y + info.velocity.y * 0.2 > 140) emit('close');
}

watch(
  () => open,
  async (now) => {
    if (!now) {
      if (lastFocus instanceof HTMLElement && lastFocus.isConnected) lastFocus.focus();
      lastFocus = null;
      return;
    }
    lastFocus = document.activeElement;
    onResize();
    await nextTick();
    const p = el();
    if (!p) return;
    emit('open', p);
    if (!p.contains(document.activeElement)) p.focus();
  },
  { immediate: true },
);
onMounted(() => {
  document.addEventListener('keydown', onKey);
  window.addEventListener('resize', onResize);
});
onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKey);
  window.removeEventListener('resize', onResize);
});
</script>

<template>
  <Teleport to="body">
    <div :class="['layer', { inert: !open }]">
      <AnimatePresence>
        <motion.div
          v-if="open"
          key="scrim"
          :class="['scrim', { clear: !compact }]"
          :initial="{ opacity: 0 }"
          :animate="{ opacity: 1 }"
          :exit="{ opacity: 0 }"
          :transition="transition('sheet', reduced)"
          @click="emit('close')"
        />
      </AnimatePresence>
      <AnimatePresence>
        <motion.div
          v-if="open"
          key="panel"
          ref="panel"
          v-bind="$attrs"
          :class="['sheet', `sheet-${size}`, 'glass', { compact }]"
          role="dialog"
          aria-modal="true"
          tabindex="-1"
          :initial="enter"
          :animate="{ opacity: 1, scale: 1, x: 0, y: 0 }"
          :exit="exit"
          :transition="transition('sheet', reduced)"
          :drag="compact ? 'y' : false"
          :drag-constraints="{ top: 0, bottom: 0 }"
          :drag-elastic="{ top: 0, bottom: 0.9 }"
          @drag-end="onDragEnd"
        >
          <div class="grab" aria-hidden="true"></div>
          <slot />
        </motion.div>
      </AnimatePresence>
    </div>
  </Teleport>
</template>
