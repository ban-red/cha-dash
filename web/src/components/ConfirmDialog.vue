<script setup lang="ts">
/**
 * The T2 confirm: what the action does, what it touches (the blast radius,
 * built up while you hold), and the hold-to-confirm ring. Esc or the scrim
 * cancels.
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { AnimatePresence, motion } from 'motion-v';
import Glyph from './marks/Glyph.vue';
import HoldToConfirm from './HoldToConfirm.vue';
import { relationWords, impactName } from '../model/words';
import { transition, useReducedMotion } from '../lib/motion';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';

const live = useLive();
const ui = useUi();
const reduced = useReducedMotion();
const progress = ref(0);
const panel = ref<{ $el?: HTMLElement } | HTMLElement | null>(null);
let lastFocus: Element | null = null;

const req = computed(() => ui.state.confirm);
const impacts = computed(() => {
  const c = req.value;
  if (!c) return [];
  return (c.action.impact ?? c.incident.blast_radius ?? []).slice(0, 12);
});
const verb = computed(() => (req.value?.action.label.split(' ')[0] ?? 'confirm').toLowerCase());
const compact = computed(() => typeof window !== 'undefined' && window.innerWidth < 640);

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && ui.state.confirm) {
    e.stopPropagation();
    ui.cancelConfirm();
  }
}
// Mounted once, on first use, so AnimatePresence can play the exit.
watch(
  req,
  async (r, before) => {
    if (r && !before) {
      progress.value = 0;
      lastFocus = document.activeElement;
      await nextTick();
      const el = panel.value instanceof HTMLElement ? panel.value : panel.value?.$el;
      el?.querySelector<HTMLElement>('.hold')?.focus();
    } else if (!r && before && lastFocus instanceof HTMLElement && lastFocus.isConnected) {
      lastFocus.focus();
    }
  },
  { immediate: true },
);
onMounted(() => document.addEventListener('keydown', onKey, true));
onBeforeUnmount(() => document.removeEventListener('keydown', onKey, true));
const lit = (i: number) => progress.value >= ((i + 0.5) / Math.max(1, impacts.value.length)) * 0.92;
</script>

<template>
  <Teleport to="body">
    <div :class="['layer', 'layer-top', { inert: !req }]">
      <AnimatePresence>
        <motion.div
          v-if="req"
          key="scrim"
          class="scrim"
          :initial="{ opacity: 0 }"
          :animate="{ opacity: 1 }"
          :exit="{ opacity: 0 }"
          :transition="transition('sheet', reduced)"
          @click="ui.cancelConfirm()"
        />
      </AnimatePresence>
      <AnimatePresence>
        <motion.div
          v-if="req"
          key="panel"
          ref="panel"
          :class="['confirm', 'glass', { compact }]"
          role="alertdialog"
          aria-modal="true"
          aria-labelledby="confirm-h"
          :initial="reduced ? { opacity: 0, x: '-50%' } : { opacity: 0, scale: 0.96, x: '-50%' }"
          :animate="{ opacity: 1, scale: 1, x: '-50%' }"
          :exit="{ opacity: 0, x: '-50%' }"
          :transition="transition('sheet', reduced)"
        >
          <h3 id="confirm-h">{{ req.action.label }}</h3>
          <p>{{ req.action.detail ?? '' }} Hold to confirm. Let go to cancel.</p>
          <ul class="blast" aria-label="What this affects">
            <li v-for="(x, i) in impacts" :key="x.resource" :class="{ on: lit(i) }">
              <Glyph :kind="x.depth <= 1 ? 'attn' : 'ok'" />
              <span>{{ impactName(live.graph.value, x.resource) }}</span>
              <span class="via">{{ relationWords(x.via) }}</span>
            </li>
            <li :class="{ on: progress >= 0.95 }"><Glyph kind="check" /><span>Audited, and shown in the Inbox until it settles</span></li>
          </ul>
          <div class="foot">
            <HoldToConfirm :label="verb" @progress="progress = $event" @confirm="ui.confirmed()" />
            <span id="hold-hint" class="hint">hold · or hold Space</span>
          </div>
        </motion.div>
      </AnimatePresence>
    </div>
  </Teleport>
</template>
