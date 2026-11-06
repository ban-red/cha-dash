<script setup lang="ts">
/**
 * Live logs (plan 16 · 4): follows one stream from the hub (lib/logstream.ts)
 * from the moment it mounts, for a container's sheet and a stack's page.
 *
 * Pause stops the view moving while lines keep arriving, and says how many
 * wait. Search highlights plain text (no patterns) and counts the matches.
 * stderr lines carry a visible "err" tag and "stderr:" for screen readers,
 * not colour alone; a stack's lines carry their service. The view keeps the
 * newest 10,000 lines, which Download saves as a .txt. When the stream ends
 * it says why and offers Follow again. Unmounting aborts the request, so
 * closing the sheet ends the stream. Nothing is stored.
 *
 * Its own chunk: the sheet and the stack page load it when logs open.
 */
import { computed, nextTick, onBeforeUnmount, ref, shallowRef, useId, useTemplateRef, watch } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import UiButton from '../ui/Button.vue';
import UiField from '../ui/Field.vue';
import { fmtInt, plural } from '../lib/format';
import { countMatches, follow, LineBuffer, logsFileName, logsText, pieces, type LogEnd, type LogLine } from '../lib/logstream';
import { saveText } from '../model/history';
import { useUi } from '../stores/ui';

const { url, name } = defineProps<{
  /** The stream to follow (containerLogsUrl or stackLogsUrl). */
  url: string;
  /** What the logs are of, for labels and the file name. */
  name: string;
}>();

const ui = useUi();
const uid = useId();
const box = useTemplateRef<HTMLElement>('box');

const buf = new LineBuffer();
/** Bumped when the buffer changes: the buffer itself isn't reactive, to keep 10,000 lines cheap. */
const version = shallowRef(0);
const state = ref<'connecting' | 'live' | 'ended'>('connecting');
const ended = ref<LogEnd | undefined>();
const paused = ref(false);
const frozen = shallowRef<LogLine[]>([]);
const pausedAt = ref(0);
const query = ref('');

let ctrl: AbortController | undefined;

function start(fresh: boolean): void {
  ctrl?.abort();
  const mine = new AbortController();
  ctrl = mine;
  if (fresh) {
    buf.clear();
    version.value++;
  }
  // Follow again starts with the tail again: skip the lines already here.
  const after = fresh ? undefined : Date.parse(buf.lastAt ?? '');
  let skipping = after !== undefined && !Number.isNaN(after);
  state.value = 'connecting';
  ended.value = undefined;
  void follow(url, mine.signal, {
    open: () => {
      if (ctrl === mine) state.value = 'live';
    },
    lines: (batch) => {
      if (ctrl !== mine) return;
      if (skipping) {
        const i = batch.findIndex((l) => !l.at || Date.parse(l.at) > after!);
        if (i < 0) return;
        skipping = false;
        batch = batch.slice(i);
      }
      buf.push(batch);
      version.value++;
    },
    end: (e) => {
      if (ctrl !== mine) return;
      state.value = 'ended';
      ended.value = e;
    },
  });
}

watch(() => url, () => start(true), { immediate: true });
onBeforeUnmount(() => {
  ctrl?.abort();
  ctrl = undefined;
});

/** What the view shows: the buffer, or what it held when Pause was pressed. */
const shown = computed<readonly LogLine[]>(() => {
  void version.value;
  return paused.value ? frozen.value : buf.lines.slice();
});
const waiting = computed(() => {
  void version.value;
  return paused.value ? buf.total - pausedAt.value : 0;
});
const matches = computed(() => countMatches(shown.value, query.value));

function pause(): void {
  frozen.value = buf.lines.slice();
  pausedAt.value = buf.total;
  paused.value = true;
}
function resume(): void {
  paused.value = false;
  frozen.value = [];
}

// Following: the newest line stays in view unless paused.
watch(shown, async () => {
  if (paused.value) return;
  await nextTick();
  const el = box.value;
  if (el) el.scrollTop = el.scrollHeight;
});

function download(): void {
  const file = logsFileName(name, Date.now());
  if (saveText(logsText(buf.lines), file, 'text/plain')) ui.toast(`Saved ${file}.`);
  else ui.toast('This browser can’t save files from the page.', 'error');
}

const statusWords = computed(() => {
  if (state.value === 'ended') return ended.value?.message ?? 'The log ended.';
  if (state.value === 'connecting') return 'Connecting…';
  return paused.value ? 'Paused.' : 'Following live.';
});
const matchWords = computed(() => (query.value ? (matches.value ? `${plural(matches.value, 'match', 'matches')}` : 'No matches') : ''));
</script>

<template>
  <div class="lv">
    <div class="lv-bar">
      <UiButton size="sm" :aria-pressed="paused" :disabled="state === 'ended' && !paused" @click="paused ? resume() : pause()">{{ paused ? 'Resume' : 'Pause' }}</UiButton>
      <UiField v-slot="{ control }" class="lv-find" label="Search the log" hide-label>
        <input v-bind="control" v-model="query" type="search" placeholder="Search" autocomplete="off" spellcheck="false" :aria-describedby="`${uid}-n`" />
      </UiField>
      <span :id="`${uid}-n`" class="lv-n">{{ matchWords }}</span>
      <UiButton size="sm" variant="ghost" :disabled="!buf.total" @click="download">Download</UiButton>
    </div>
    <p class="lv-state" role="status">
      <Glyph v-if="state === 'ended'" :kind="ended?.reason === 'error' ? 'down' : 'off'" />
      <span>{{ statusWords }}</span>
    </p>
    <p v-if="paused && waiting" class="lv-wait">{{ plural(waiting, 'new line') }} waiting.</p>
    <div ref="box" class="lv-box" tabindex="0" role="region" :aria-label="`${name}’s log`" :aria-busy="state === 'connecting' || undefined">
      <p v-if="!shown.length" class="lv-empty">{{ state === 'connecting' ? 'Waiting for the first lines…' : 'No lines.' }}</p>
      <pre v-else class="lv-pre"><span v-for="l in shown" :key="l.n" :class="['lv-l', { err: l.stream === 'stderr' }]"><span v-if="l.stream === 'stderr'" class="lv-tag" aria-hidden="true">err</span><span v-if="l.stream === 'stderr'" class="sr">stderr: </span><span v-if="l.service" class="lv-svc">{{ l.service }}</span><template v-for="(p, i) in pieces(l.line, query)" :key="i"><mark v-if="p.hit">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template>{{ '\n' }}</span></pre>
    </div>
    <p class="lv-foot">
      <span>{{ buf.total > buf.lines.length ? `The newest ${fmtInt(buf.lines.length)} lines.` : plural(buf.lines.length, 'line') + '.' }} Cha Dash doesn’t keep them.</span>
      <UiButton v-if="state === 'ended'" size="sm" @click="start(false)">Follow again</UiButton>
    </p>
  </div>
</template>

<style>
.lv {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.lv-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
}
.lv-find {
  flex: 1 1 10rem;
  min-width: 0;
}
.lv-find input {
  width: 100%;
}
.lv-n,
.lv-wait,
.lv-foot,
.lv-empty {
  font-size: var(--type-small);
  color: var(--text-2);
}
.lv-state,
.lv-wait,
.lv-empty {
  margin: 0;
}
.lv-state {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--type-small);
}
.lv-foot {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
  margin: 0;
}
.lv-box {
  max-height: min(55vh, 32rem);
  overflow: auto;
  padding: 8px 10px;
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-md);
}
.lv-pre {
  margin: 0;
  font: var(--weight-regular) var(--type-caption) / 1.55 var(--font-num);
  color: var(--text);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.lv-l.err {
  box-shadow: inset 2px 0 0 var(--down-line);
  padding-left: 6px;
}
.lv-tag {
  margin-right: 6px;
  padding: 0 4px;
  border-radius: var(--radius-sm);
  color: var(--down-text);
  background: var(--down-bg);
}
.lv-svc {
  margin-right: 6px;
  color: var(--text-2);
}
.lv-svc::after {
  content: ' |';
  color: var(--text-3);
}
.lv-pre mark {
  color: var(--text);
  background: var(--attention-bg);
  border-radius: 2px;
}
</style>
