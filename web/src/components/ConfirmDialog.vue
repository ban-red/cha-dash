<script setup lang="ts">
/**
 * The T2 confirm: what the action does (the plan's steps), what it touches
 * (the plan's impact, built up while you hold), and the hold-to-confirm ring.
 * Esc, Cancel or the scrim cancels; Tab stays inside.
 *
 * It also shows a plan that changed under a T0 or T1 action (`review`): the
 * same list, with a plain Run button instead of the ring. T3 and T4 use the
 * typed confirmation (TypeConfirmDialog.vue).
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { AnimatePresence, motion } from 'motion-v';
import Glyph from './marks/Glyph.vue';
import HoldToConfirm from './HoldToConfirm.vue';
import { HUB_WARNING, hitsHub, hubFirst, relationWords, impactName } from '../model/words';
import { isHub } from '../model/hub';
import { transition, useReducedMotion } from '../lib/motion';
import { trapTab } from '../lib/trap';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';

const live = useLive();
const ui = useUi();
const reduced = useReducedMotion();
const progress = ref(0);
const panel = ref<{ $el?: HTMLElement } | HTMLElement | null>(null);
let lastFocus: Element | null = null;

const req = computed(() => (ui.state.confirm && ui.state.confirm.kind !== 'type' ? ui.state.confirm : undefined));
const review = computed(() => req.value?.kind === 'review');
const impacts = computed(() => {
  const c = req.value;
  if (!c) return [];
  return hubFirst(live.graph.value, c.plan?.impact ?? c.action.impact ?? c.incident?.blast_radius ?? []).slice(0, 12);
});
const takesHub = computed(() => hitsHub(live.graph.value, impacts.value.map((x) => x.resource)));
const steps = computed(() => req.value?.plan?.steps ?? []);
const verb = computed(() => (req.value?.action.label.split(' ')[0] ?? 'confirm').toLowerCase());
const compact = computed(() => typeof window !== 'undefined' && window.innerWidth < 640);
const el = () => (panel.value instanceof HTMLElement ? panel.value : panel.value?.$el);

function onKey(e: KeyboardEvent) {
  if (!req.value) return;
  if (e.key === 'Escape') {
    e.stopPropagation();
    ui.cancelConfirm();
  } else if (e.key === 'Tab') trapTab(e, el());
}
// Mounted once, on first use, so AnimatePresence can play the exit.
watch(
  req,
  async (r, before) => {
    if (r && r !== before) {
      progress.value = 0;
      if (!before) lastFocus = document.activeElement;
      await nextTick();
      el()?.querySelector<HTMLElement>(review.value ? '.btn.primary' : '.hold')?.focus();
    } else if (!r && before && !ui.state.confirm && !ui.state.stepUp && lastFocus instanceof HTMLElement && lastFocus.isConnected) {
      lastFocus.focus();
    }
  },
  { immediate: true },
);
onMounted(() => document.addEventListener('keydown', onKey, true));
onBeforeUnmount(() => document.removeEventListener('keydown', onKey, true));
const lit = (i: number) => review.value || progress.value >= ((i + 0.5) / Math.max(1, impacts.value.length)) * 0.92;
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
          aria-describedby="confirm-d"
          :initial="reduced ? { opacity: 0, x: '-50%' } : { opacity: 0, scale: 0.96, x: '-50%' }"
          :animate="{ opacity: 1, scale: 1, x: '-50%' }"
          :exit="{ opacity: 0, x: '-50%' }"
          :transition="transition('sheet', reduced)"
        >
          <h3 id="confirm-h">{{ req.action.label }} <span class="chip">{{ req.action.tier }}</span></h3>
          <p v-if="req.note" class="confirm-note" role="note"><Glyph kind="attn" />{{ req.note }}</p>
          <p v-if="takesHub" class="confirm-note is-self" role="alert"><Glyph kind="attn" />{{ HUB_WARNING }}</p>
          <p id="confirm-d">{{ req.action.detail ? `${req.action.detail} ` : '' }}{{ review ? 'Check the plan, then run it.' : 'Hold to confirm. Let go to cancel.' }}</p>
          <ol v-if="steps.length" class="confirm-steps" aria-label="What Cha Dash will do">
            <li v-for="(s, i) in steps" :key="i">{{ s.label }}</li>
          </ol>
          <ul class="blast" aria-label="What this affects">
            <li v-for="(x, i) in impacts" :key="x.resource" :class="{ on: lit(i), 'is-self': isHub(live.graph.value, x.resource) }">
              <Glyph :kind="x.depth <= 1 ? 'attn' : 'ok'" />
              <span>{{ impactName(live.graph.value, x.resource) }}</span>
              <span class="via">{{ relationWords(x.via) }}</span>
            </li>
            <li :class="{ on: review || progress >= 0.95 }"><Glyph kind="check" /><span>Audited, and shown in the Inbox until it settles</span></li>
          </ul>
          <div class="foot">
            <template v-if="review">
              <button type="button" class="btn primary" @click="ui.confirmed()">Run it</button>
            </template>
            <template v-else>
              <HoldToConfirm :label="verb" @progress="progress = $event" @confirm="ui.confirmed()" />
              <span id="hold-hint" class="hint">hold · or hold Space</span>
            </template>
            <button type="button" class="btn ghost sm" @click="ui.cancelConfirm()">Cancel</button>
          </div>
        </motion.div>
      </AnimatePresence>
    </div>
  </Teleport>
</template>

<style>
.confirm h3 .chip {
  vertical-align: 3px;
}
.confirm .confirm-note {
  display: flex;
  gap: var(--space-2);
  align-items: flex-start;
  padding: 8px 10px;
  border: var(--hairline) solid color-mix(in oklch, var(--attention) 55%, var(--line));
  border-radius: var(--radius-md);
  background: var(--attention-bg);
  color: var(--text);
}
.confirm .confirm-note .g {
  margin-top: 3px;
}
.confirm-steps {
  margin: 0 0 var(--space-3);
  padding-left: 1.4em;
  font-size: var(--type-small);
  line-height: 1.5;
  color: var(--text-2);
}
</style>
