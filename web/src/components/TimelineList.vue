<script setup lang="ts">
/**
 * The timeline (GET /api/timeline): incidents, their causes, the actions
 * that ran, health changes and maintenance windows, newest first, grouped
 * by day. Shown on a resource's sheet (its Timeline tab) and on Settings →
 * Timeline (the whole home, or `?resource=`).
 *
 * The hub words each entry; "Copy as Markdown" puts the range, oldest
 * first, on the clipboard for a postmortem. Every state is drawn: loading,
 * the entries, nothing in the range, an error with Try again, and a note
 * when the hub keeps no health history or listed only the newest 1,000.
 */
import { computed, ref, useId, watch } from 'vue';
import { ApiError } from '../lib/api';
import { hhmm } from '../lib/format';
import { copyText } from '../settings/recipe';
import { byDay, fetchTimeline, markWord, TIMELINE_SPANS, timelineMarkdown, type Timeline, type TimelineEntry, type TimelineSpan } from '../model/timeline';
import { useUi } from '../stores/ui';
import Glyph, { type GlyphKind } from '../ui/marks/Glyph.vue';
import UiButton from '../ui/Button.vue';

const props = withDefaults(
  defineProps<{
    /** One resource's timeline; the whole home without it. */
    resource?: string | undefined;
    /** What it's the timeline of, for the Markdown's heading. */
    title: string;
    /** The range to start on. */
    span?: TimelineSpan;
    /** Offer the longer ranges (the page does; a sheet keeps to a week). */
    spans?: readonly TimelineSpan[];
    /** Name each entry's resource: the whole home's page does. */
    nameOf?: ((id: string) => string) | undefined;
  }>(),
  { span: '7d', spans: () => ['24h', '7d'] },
);
const emit = defineEmits<{ open: [resource: string] }>();

const ui = useUi();
const uid = useId();
const span = ref<TimelineSpan>(props.span);
const state = ref<'loading' | 'ready' | 'error'>('loading');
const error = ref('');
const tl = ref<Timeline>();
let seq = 0;

async function load(): Promise<void> {
  const mine = ++seq;
  state.value = 'loading';
  try {
    const got = await fetchTimeline(props.resource, span.value, Date.now());
    if (mine !== seq) return;
    tl.value = got;
    state.value = 'ready';
  } catch (err) {
    if (mine !== seq) return;
    error.value = err instanceof ApiError ? err.message : 'The timeline couldn’t be read.';
    state.value = 'error';
  }
}
watch([() => props.resource, span], () => void load(), { immediate: true });

const days = computed(() => (tl.value ? byDay(tl.value.entries) : []));
const spanName = computed(() => TIMELINE_SPANS.find((s) => s.id === span.value)?.name ?? '');
const offered = computed(() => TIMELINE_SPANS.filter((s) => props.spans.includes(s.id)));

function glyph(e: TimelineEntry): GlyphKind {
  switch (e.kind) {
    case 'window':
      return 'maint';
    case 'cause':
      return 'link';
    case 'action':
      return e.event === 'failed' ? 'cross' : 'check';
    case 'incident':
      if (e.event === 'resolved') return 'check';
      if (e.event === 'expected') return 'maint';
      break;
  }
  switch (e.severity) {
    case 'down':
      return 'down';
    case 'attention':
      return 'attn';
    case 'ok':
      return 'ok';
  }
  return 'off';
}

async function copy(): Promise<void> {
  if (!tl.value) return;
  const ok = await copyText(timelineMarkdown(tl.value, props.title));
  ui.toast(ok ? `Copied the last ${spanName.value} as Markdown.` : 'This browser can’t copy from the page.', ok ? 'ok' : 'error');
}
</script>

<template>
  <div class="tl" :aria-busy="state === 'loading' || undefined">
    <div class="tl-bar">
      <div v-if="offered.length > 1" class="tl-spans" role="group" :aria-label="`Range of ${title}'s timeline`">
        <button v-for="s in offered" :key="s.id" type="button" class="tl-span" :aria-pressed="span === s.id" @click="span = s.id">
          {{ s.name }}
        </button>
      </div>
      <UiButton size="sm" variant="ghost" :disabled="state !== 'ready'" @click="copy">Copy as Markdown</UiButton>
    </div>

    <div v-if="state === 'loading' && !tl" class="tl-skel" aria-hidden="true"><span class="skel"></span><span class="skel"></span><span class="skel"></span></div>
    <div v-else-if="state === 'error'" class="tl-note bad" role="alert">
      <Glyph kind="down" /><span>{{ error }}</span>
      <UiButton size="sm" @click="load">Try again</UiButton>
    </div>
    <template v-else-if="tl">
      <p v-if="!tl.entries.length" class="tl-empty">Nothing happened in the last {{ spanName }}.</p>
      <section v-for="d in days" :key="d.day" class="tl-day" :aria-labelledby="`${uid}-${d.day.replace(/\W+/g, '-')}`">
        <h4 :id="`${uid}-${d.day.replace(/\W+/g, '-')}`" class="tl-day-h">{{ d.day }}</h4>
        <ol class="tl-list">
          <li v-for="(e, i) in d.entries" :key="`${e.at}-${i}`" :class="['tl-item', `k-${e.kind}`]">
            <time :datetime="e.at" class="tl-time">{{ hhmm(e.at) }}</time>
            <Glyph :kind="glyph(e)" :label="markWord(e)" />
            <span class="tl-text">
              {{ e.text }}
              <button v-if="nameOf && e.resource && e.kind !== 'window'" type="button" class="lk tl-res" @click="emit('open', e.resource)">
                {{ nameOf(e.resource) }}
              </button>
            </span>
          </li>
        </ol>
      </section>
      <p v-if="tl.truncated" class="tl-hint">Only the newest 1,000 entries are shown. Pick a shorter range to see the rest.</p>
      <p v-if="tl.history === 'off'" class="tl-hint">Cha Dash keeps no health history here, so health changes aren’t on the timeline.</p>
    </template>
  </div>
</template>

<style scoped>
.tl {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.tl-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
}
.tl-spans {
  display: inline-flex;
  gap: 2px;
  padding: 2px;
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-sm);
}
.tl-span {
  min-height: 28px;
  padding: 0 var(--space-3);
  border: 0;
  border-radius: calc(var(--radius-sm) - 2px);
  background: none;
  font: var(--weight-medium) var(--type-caption) / 1 var(--font-ui);
  color: var(--text-2);
  cursor: pointer;
}
.tl-span[aria-pressed='true'] {
  background: var(--surface-2);
  color: var(--text);
}
.tl-span:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.tl-day {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.tl-day-h {
  margin: 0;
  font: var(--weight-medium) var(--type-caption) / 1.2 var(--font-ui);
  color: var(--text-3);
}
.tl-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  margin: 0;
  padding: 0 0 0 var(--space-1);
  list-style: none;
  border-left: var(--hairline) solid var(--line);
}
.tl-item {
  display: grid;
  grid-template-columns: 3.2em 14px minmax(0, 1fr);
  gap: var(--space-2);
  align-items: baseline;
  padding-left: var(--space-2);
  font-size: var(--type-small);
  line-height: 1.4;
}
.tl-time {
  font: var(--weight-regular) var(--type-caption) / 1.4 var(--font-num);
  color: var(--text-3);
}
.tl-text {
  color: var(--text);
  overflow-wrap: anywhere;
}
.k-cause .tl-text,
.k-window .tl-text {
  color: var(--text-2);
}
.tl-res {
  margin-left: var(--space-1);
  font-size: var(--type-caption);
}
.tl-empty,
.tl-hint {
  margin: 0;
  font-size: var(--type-small);
  color: var(--text-3);
}
.tl-note {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--type-small);
  color: var(--text-2);
}
.tl-skel {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
</style>
