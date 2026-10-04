<script setup lang="ts">
/**
 * The Inbox, grouped by status: open (problems that need you, down first,
 * then integration repairs), being fixed (a fix is running, or it's
 * recovering by itself), and resolved. Real and demo hubs fill it the same
 * way, from the `inbox` topic. A glass panel on desktop; a bottom sheet on a
 * phone that a flick dismisses. An item that resolves while you watch shows
 * its done row in place for a moment, then moves to Resolved.
 */
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue';
import { AnimatePresence, motion } from 'motion-v';
import Glyph from './marks/Glyph.vue';
import IncidentItem from './IncidentItem.vue';
import { hhmm } from '../lib/format';
import { transition, useReducedMotion } from '../lib/motion';
import { groupInbox, incidents, type InboxGroups } from '../model/inbox';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import type { Incident } from '../model/types';

const live = useLive();
const ui = useUi();
const reduced = useReducedMotion();
const panel = ref<{ $el?: HTMLElement } | HTMLElement | null>(null);
const expanded = reactive(new Set<string>());
const lingering = reactive(new Map<string, number>());
const compact = ref(typeof window !== 'undefined' && window.innerWidth < 640);
let lastFocus: Element | null = null;

const LINGER_MS = 2600;

const all = computed(() => incidents(live.inbox.value));
const isLingering = (id: string) => lingering.has(id);
const groups = computed(() => groupInbox(live.inbox.value, live.fixes));

/**
 * Lists hold still while you read them: an item keeps the group it had when
 * the Inbox opened (or when it arrived), even if a fix moves it to "being
 * fixed" meanwhile. The groups re-sort the next time the Inbox opens.
 */
type Place = 'open' | 'fixing';
const placed = reactive(new Map<string, Place>());
function place(g: InboxGroups, reset = false) {
  if (reset) {
    for (const id of [...placed.keys()]) if (!isLingering(id)) placed.delete(id);
  }
  for (const i of g.open) if (!placed.has(i.id)) placed.set(i.id, 'open');
  for (const i of g.fixing) if (!placed.has(i.id)) placed.set(i.id, 'fixing');
}
watch(groups, (g) => place(g), { immediate: true, flush: 'sync' });

const inGroup = (where: Place): Incident[] =>
  [...groups.value.open, ...groups.value.fixing, ...groups.value.resolved.filter((i) => isLingering(i.id))].filter((i) => placed.get(i.id) === where);
const open = computed(() => inGroup('open'));
const fixing = computed(() => inGroup('fixing'));
const openNow = (list: Incident[]) => list.filter((i) => i.status === 'open').length;
const done = computed(() => groups.value.resolved.filter((i) => !isLingering(i.id)));
const openCount = computed(() => openNow(open.value));

// Keep a just-resolved item in place briefly, expanded on its done row.
watch(
  () => all.value.map((i) => `${i.id}:${i.status}`).join('|'),
  (_now, before) => {
    if (!before) return;
    const was = new Map(before.split('|').map((s) => s.split(':') as [string, string]));
    for (const i of all.value) {
      if (i.status === 'resolved' && was.get(i.id) === 'open') {
        lingering.set(i.id, Date.now());
        if (!placed.has(i.id)) placed.set(i.id, 'open');
        expanded.add(i.id);
        setTimeout(() => {
          lingering.delete(i.id);
          placed.delete(i.id);
        }, LINGER_MS);
      }
    }
  },
);

watch(
  () => ui.state.inboxFocus,
  (id) => {
    if (id) expanded.add(id);
  },
  { immediate: true },
);

function toggle(id: string) {
  if (expanded.has(id)) expanded.delete(id);
  else expanded.add(id);
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && ui.state.inboxOpen && !ui.state.confirm) ui.closeInbox();
}
function onResize() {
  compact.value = window.innerWidth < 640;
}

// Mounted once, on first open, so AnimatePresence can play the exit.
watch(
  () => ui.state.inboxOpen,
  async (open) => {
    if (!open) {
      if (lastFocus instanceof HTMLElement && lastFocus.isConnected) lastFocus.focus();
      return;
    }
    lastFocus = document.activeElement;
    place(groups.value, true);
    onResize();
    await nextTick();
    const el = panel.value instanceof HTMLElement ? panel.value : panel.value?.$el;
    const id = ui.state.inboxFocus;
    const focus = id ? el?.querySelector<HTMLElement>(`[aria-controls="ib-${CSS.escape(id)}"]`) : null;
    (focus ?? el)?.focus();
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

/** A flick or a long drag down dismisses the sheet (momentum projects the release). */
function onDragEnd(_e: PointerEvent, info: { offset: { y: number }; velocity: { y: number } }) {
  const projected = info.offset.y + info.velocity.y * 0.2;
  if (projected > 140) ui.closeInbox();
}

const enter = computed(() =>
  reduced.value ? { opacity: 0 } : compact.value ? { y: '100%' } : { opacity: 0, scale: 0.96, y: -8 },
);
const exit = computed(() => (reduced.value ? { opacity: 0 } : compact.value ? { y: '100%' } : { opacity: 0, scale: 0.97, y: -6 }));
</script>

<template>
  <Teleport to="body">
    <div :class="['layer', { inert: !ui.state.inboxOpen }]">
      <AnimatePresence>
        <motion.div
          v-if="ui.state.inboxOpen"
          key="scrim"
          :class="['scrim', { clear: !compact }]"
          :initial="{ opacity: 0 }"
          :animate="{ opacity: 1 }"
          :exit="{ opacity: 0 }"
          :transition="transition('sheet', reduced)"
          @click="ui.closeInbox()"
        />
      </AnimatePresence>
      <AnimatePresence>
        <motion.div
          v-if="ui.state.inboxOpen"
          key="panel"
          ref="panel"
          :class="['inbox', 'glass', { compact }]"
          role="dialog"
          aria-modal="true"
          aria-labelledby="inbox-h"
          tabindex="-1"
          :initial="enter"
          :animate="{ opacity: 1, scale: 1, y: 0 }"
          :exit="exit"
          :transition="transition('sheet', reduced)"
          :drag="compact ? 'y' : false"
          :drag-constraints="{ top: 0, bottom: 0 }"
          :drag-elastic="{ top: 0, bottom: 0.9 }"
          @drag-end="onDragEnd"
        >
          <div class="grab" aria-hidden="true"></div>
          <div class="ib-head">
            <h2 id="inbox-h">Inbox</h2>
            <span class="n">{{ openCount ? `${openCount} need${openCount === 1 ? 's' : ''} you` : 'nothing open' }}</span>
            <button type="button" class="x" aria-label="Close Inbox" @click="ui.closeInbox()">
              <svg viewBox="0 0 10 10" aria-hidden="true"><path d="m2.5 2.5 5 5m0-5-5 5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
            </button>
          </div>

          <div v-if="live.stale.value" class="ib">
            <Glyph kind="down" label="unreachable" />
            <div>
              <div class="ib-title">The hub is unreachable</div>
              <div class="m">This is the Inbox as of {{ hhmm(live.offlineAt.value) }}. Actions come back when the hub does.</div>
            </div>
          </div>

          <template v-if="open.length">
            <div class="ib-grp">Open · {{ openNow(open) }}</div>
            <IncidentItem v-for="i in open" :key="i.id" :incident="i" :expanded="expanded.has(i.id)" @toggle="toggle(i.id)" />
          </template>

          <template v-if="fixing.length">
            <div class="ib-grp">Being fixed · {{ openNow(fixing) }}</div>
            <IncidentItem v-for="i in fixing" :key="i.id" :incident="i" :expanded="expanded.has(i.id)" @toggle="toggle(i.id)" />
          </template>

          <div v-if="!open.length && !fixing.length" class="ib-empty">
            <Glyph kind="check" />
            <div class="t">Nothing needs you</div>
            <div class="m">Every integration is reading normally.{{ done[0] ? ` The last fix was “${done[0].title}”, at ${hhmm(done[0].resolved_at)}.` : '' }}</div>
          </div>

          <template v-if="done.length">
            <div class="ib-grp">Resolved · {{ done.length }}</div>
            <IncidentItem v-for="i in done" :key="i.id" :incident="i" :expanded="expanded.has(i.id)" @toggle="toggle(i.id)" />
          </template>
        </motion.div>
      </AnimatePresence>
    </div>
  </Teleport>
</template>
