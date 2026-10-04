<script setup lang="ts">
/**
 * Needs attention: a system section that exists only while something needs
 * you. Problems are mirrored here as strips (their tiles stay put), down
 * first. When the last one clears, the section collapses on the `move`
 * spring (the CSS token is the spring, sampled into linear()).
 */
import { computed, ref, watch } from 'vue';
import Glyph from './marks/Glyph.vue';
import Meter from './marks/Meter.vue';
import Sparkline from './marks/Sparkline.vue';
import { fmtPct, hhmm } from '../lib/format';
import { series } from '../model/graph';
import { incidentKicker } from '../model/inbox';
import type { Incident } from '../model/types';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';

const live = useLive();
const ui = useUi();
const all = computed(() => live.problems.value);
/** Down first, so the first few are the worst; the rest wait behind "Show all". */
const LIMIT = 3;
const expanded = ref(false);
const items = computed(() => (expanded.value ? all.value : all.value.slice(0, LIMIT)));
const hidden = computed(() => all.value.length - LIMIT);
watch(hidden, (n) => {
  if (n <= 0) expanded.value = false;
});

/**
 * The mark that shows the symptom, chosen from what the incident's facts
 * quote: a fill level as a bar with its threshold, memory as 24 h of history.
 * Nothing when no series tells the story (a failed backup).
 */
function viz(i: Incident) {
  const m = live.metrics.value;
  const f = i.facts ?? {};
  if ('used_pct' in f) {
    const used = series(m, i.target, 'used.pct');
    if (used) return { kind: 'meter' as const, s: used };
    const mem = series(m, i.target, 'mem.pct');
    if (mem) return { kind: 'spark' as const, s: mem };
  }
  if ('limit_mib' in f) {
    const mem = series(m, i.target, 'mem.mib');
    if (mem) return { kind: 'spark' as const, s: mem };
  }
  return undefined;
}

/** Run a height transition and call done once, even if transitionend never fires. */
function animateHeight(node: HTMLElement, from: number, to: number, done: () => void, cleanup: () => void) {
  let finished = false;
  const finish = () => {
    if (finished) return;
    finished = true;
    node.removeEventListener('transitionend', onEnd);
    cleanup();
    done();
  };
  const onEnd = (e: TransitionEvent) => {
    if (e.target === node && e.propertyName === 'height') finish();
  };
  node.style.height = `${from}px`;
  node.style.opacity = to ? '0' : '1';
  node.addEventListener('transitionend', onEnd);
  requestAnimationFrame(() => {
    node.style.height = `${to}px`;
    node.style.opacity = to ? '1' : '0';
  });
  setTimeout(finish, 900); // the move token settles in 520 ms
}
function onEnter(el: Element, done: () => void) {
  const node = el as HTMLElement;
  animateHeight(node, 0, node.scrollHeight, done, () => {
    node.style.height = '';
    node.style.opacity = '';
  });
}
function onLeave(el: Element, done: () => void) {
  const node = el as HTMLElement;
  animateHeight(node, node.scrollHeight, 0, done, () => undefined);
}
const fixState = (id: string) => live.fixes[id]?.state;
/** What the task working on it is doing now, else the incident's phase. */
function workingWords(i: Incident): string {
  const t = ui.taskFor(i);
  if (t && !['succeeded', 'failed', 'cancelled'].includes(t.state)) {
    const p = typeof t.progress === 'number' ? ` · ${Math.round(t.progress * 100)}%` : '';
    return `${t.state === 'verifying' ? 'checking it worked' : t.step || 'working'}${p}`;
  }
  return i.phase ?? 'working';
}
</script>

<template>
  <Transition :css="false" @enter="onEnter" @leave="onLeave">
    <section v-if="all.length && ui.can.value.inbox" class="attn-sec collapse" aria-label="Needs attention">
      <header class="sh">
        <span>Needs attention · {{ all.length }}</span>
        <span class="sh-rule" aria-hidden="true"></span>
        <span class="sh-m">mirrored from their sections; your layout doesn't move</span>
      </header>
      <TransitionGroup tag="div" name="strip" class="strips">
        <article
          v-for="i in items"
          :key="i.id"
          :class="['strip', { down: i.severity === 'down', 'is-stale': live.stale.value }]"
          :aria-label="`${i.title}, needs attention`"
        >
          <Glyph :kind="i.severity === 'down' ? 'down' : 'attn'" :label="i.severity === 'down' ? 'down' : 'needs attention'" />
          <div>
            <div class="k">{{ incidentKicker(i, live.graph.value) }} · since {{ hhmm(i.opened_at) }}</div>
            <div class="t">{{ i.title }}</div>
            <div class="m">{{ i.summary }}</div>
          </div>
          <div class="viz">
            <template v-if="viz(i)?.kind === 'meter'">
              <Meter
                :value="viz(i)!.s.value"
                :warn="viz(i)!.s.warn"
                :crit="viz(i)!.s.crit"
                :tick="viz(i)!.s.warn"
                :label="`${i.title}: ${fmtPct(viz(i)!.s.value)}%`"
              />
            </template>
            <Sparkline
              v-else-if="viz(i)?.kind === 'spark'"
              :values="viz(i)!.s.history"
              :band="viz(i)!.s.band"
              :tone="i.severity === 'down' ? 'down' : 'attn'"
              :height="34"
              :label="i.title"
            />
          </div>
          <div class="acts">
            <button type="button" class="btn" @click="ui.openInbox(i.id)">Details</button>
            <button
              v-if="i.suggested && fixState(i.id) !== 'accepted' && !ui.canRun(i).hidden"
              type="button"
              class="btn"
              :disabled="!ui.canRun(i).ok"
              :title="ui.canRun(i).why"
              @click="ui.act(i)"
            >
              {{ i.suggested.label }} <span class="tier">{{ i.suggested.tier }}</span>
            </button>
            <span v-else-if="fixState(i.id) === 'accepted'" class="working"><Glyph kind="prog" label="in progress" />Working · {{ workingWords(i) }}</span>
          </div>
        </article>
      </TransitionGroup>
      <button v-if="hidden > 0" type="button" class="btn ghost attn-more" :aria-expanded="expanded" @click="expanded = !expanded">
        {{ expanded ? 'Show fewer' : `Show all ${all.length}` }}
      </button>
    </section>
  </Transition>
</template>

<style>
.collapse {
  overflow: hidden;
  transition:
    height var(--motion-move-duration) var(--motion-move-easing),
    opacity var(--motion-move-duration) var(--motion-move-easing);
}
.strip-move,
.strip-enter-active,
.strip-leave-active {
  transition:
    transform var(--motion-move-duration) var(--motion-move-easing),
    opacity var(--motion-fade-duration) var(--motion-fade-easing);
}
.strip-enter-from,
.strip-leave-to {
  opacity: 0;
}
.strip-leave-active {
  position: absolute;
  left: 0;
  right: 0;
}
.strips {
  position: relative;
}
.attn-more {
  margin-top: var(--space-2);
}
</style>
