<script setup lang="ts">
/**
 * A small, quiet presence while tasks run (from the `tasks` topic): the
 * oldest running task's label, its current step and progress, and how many
 * more. Nothing shows when nothing runs. Pressing it opens the Inbox on the
 * incident the task works on, when there is one. A task whose action allows
 * it (`cancellable`) has a Cancel button beside it: the hub stops it, and it
 * ends as cancelled.
 *
 * It is also the one place task progress is announced: a single polite live
 * region that speaks when a task's step changes and when it ends (unless
 * this tab started it, when a toast says so). Percentages stay visual; the
 * Inbox and the stack editor show progress without live regions of their own.
 */
import { computed, nextTick, ref, watch } from 'vue';
import { isDone } from '../lib/actions';
import { incidents } from '../model/inbox';
import type { Task } from '../model/types';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';

const live = useLive();
const ui = useUi();
const first = computed<Task | undefined>(() => ui.running.value[0]);
const more = computed(() => ui.running.value.length - 1);
const pct = (t: Task) => (typeof t.progress === 'number' ? Math.round(Math.min(1, Math.max(0, t.progress)) * 100) : undefined);
const C = 2 * Math.PI * 5;

/** The hub only sends tasks this viewer may see, and lets them cancel those whose action allows it. */
const cancellable = computed(() => !!first.value?.cancellable && !live.stale.value);

const incident = computed(() => {
  const t = first.value;
  if (!t || !ui.can.value.inbox) return undefined;
  return incidents(live.inbox.value).find((i) => i.status === 'open' && ui.taskFor(i)?.id === t.id);
});

const stepOf = (t: Task) => (t.state === 'queued' ? 'waiting its turn' : t.state === 'verifying' ? 'checking it worked' : t.step || 'working');
const words = computed(() => {
  const t = first.value;
  if (!t) return '';
  const p = pct(t);
  return `${t.label} · ${stepOf(t)}${p !== undefined ? ` · ${p}%` : ''}`;
});

// ── the announcer ──
/** What was last said about each task: its state and step, or 'done'. */
const said = new Map<string, string>();
const announcement = ref('');
watch(
  () => Object.values(ui.tasks.value).map((t) => `${t.id}|${t.state}|${t.step ?? ''}`).join('\n'),
  async () => {
    const lines: string[] = [];
    for (const t of Object.values(ui.tasks.value)) {
      const before = said.get(t.id);
      if (isDone(t)) {
        if (before !== undefined && before !== 'done' && !ui.startedHere(t.id)) {
          lines.push(t.state === 'succeeded' ? `Done: ${t.label}.` : t.state === 'cancelled' ? `Cancelled: ${t.label}.` : t.error?.message || `${t.label} didn’t work.`);
        }
        said.set(t.id, 'done');
        continue;
      }
      const key = `${t.state}|${t.step ?? ''}`;
      if (key === before) continue;
      said.set(t.id, key);
      // A task first seen is its start: the "Started" toast, or not news (it was running when the page opened).
      if (before !== undefined) lines.push(`${t.label}: ${stepOf(t)}.`);
    }
    if (!lines.length) return;
    announcement.value = '';
    await nextTick();
    announcement.value = lines.join(' ');
  },
  { immediate: true },
);
</script>

<template>
  <div class="task-presence">
    <span class="sr" role="status" aria-live="polite" data-task-announcer>{{ announcement }}</span>
    <div v-if="first" class="tp-row">
      <button type="button" class="tp glass" :disabled="!incident" :title="incident ? 'Show it in the Inbox' : undefined" @click="incident && ui.openInbox(incident.id)">
        <svg class="prog" viewBox="0 0 12 12" aria-hidden="true">
          <circle class="track" cx="6" cy="6" r="5" />
          <circle class="arc" cx="6" cy="6" r="5" :stroke-dasharray="C" :stroke-dashoffset="C * (1 - (pct(first) ?? 35) / 100)" />
        </svg>
        <span class="tp-t">{{ words }}</span>
        <span v-if="more > 0" class="tp-more">+{{ more }} more</span>
      </button>
      <button
        v-if="cancellable"
        type="button"
        class="tp-cancel glass"
        :disabled="ui.cancelling[first.id]"
        :aria-label="`Cancel ${first.label}`"
        @click="ui.cancelTask(first)"
      >
        {{ ui.cancelling[first.id] ? 'Cancelling…' : 'Cancel' }}
      </button>
    </div>
  </div>
</template>

<style>
.task-presence {
  position: fixed;
  z-index: 60;
  left: calc(16px + env(safe-area-inset-left));
  bottom: calc(16px + env(safe-area-inset-bottom));
  pointer-events: none;
}
.tp {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  max-width: min(420px, calc(100vw - 32px));
  min-height: 32px;
  padding: 6px 12px 6px 10px;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-full);
  font: var(--weight-medium) var(--type-caption) / 1.2 var(--font-ui);
  color: var(--text);
  pointer-events: auto;
}
.tp:disabled {
  cursor: default;
}
.tp-row {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  max-width: min(520px, calc(100vw - 32px));
}
.tp-cancel {
  flex: none;
  min-height: 32px;
  padding: 6px 12px;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-full);
  font: var(--weight-medium) var(--type-caption) / 1.2 var(--font-ui);
  color: var(--text);
  pointer-events: auto;
}
.tp-cancel:disabled {
  cursor: default;
  color: var(--text-2);
}
.tp .prog {
  flex: none;
  width: 14px;
  height: 14px;
}
.tp .prog circle {
  fill: none;
  stroke-width: 1.6;
}
.tp .prog .track {
  stroke: var(--line-2);
}
.tp .prog .arc {
  stroke: var(--text-2);
  transform: rotate(-90deg);
  transform-origin: center;
  transition: stroke-dashoffset var(--motion-move-duration) var(--motion-move-easing);
}
.tp-t {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tp-more {
  flex: none;
  font-family: var(--font-num);
  color: var(--text-2);
}
</style>
