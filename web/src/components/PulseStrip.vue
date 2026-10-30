<script setup lang="ts">
/**
 * The Pulse strip (01 · Vision → The Briefing): the whole home's health over
 * the last 24 hours, a slot a quarter hour, coloured by the worst anything
 * was (GET /api/history/pulse). Quiet grey is all well, a gap is a time
 * nothing was watched.
 *
 * Scrubbing it time-travels the board: it sets the page's time cursor
 * (stores/cursor.ts), so every board sparkline marks its value then and
 * each tile shows its health then; the readout names what wasn't well.
 * A sparkline being scrubbed marks its moment here too.
 *
 * It's a slider for keyboards and assistive tech: arrows move a quarter
 * hour, Page Up and Down an hour, Home and End the ends, Esc lets go. The
 * strip hides while the hub keeps no history or has none for the day yet.
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';
import { hhmm, plural } from '../lib/format';
import { label } from '../model/graph';
import { fetchPulse, pulseSlotAt, pulseTone, stateWords, type PulseSlot } from '../model/history';
import { clearCursor, cursor, pulse, setCursor } from '../stores/cursor';
import { useLive } from '../stores/live';

/** How often the strip reads the hub again. */
const REFRESH_MS = 5 * 60_000;

const live = useLive();
const el = ref<HTMLElement | null>(null);
let timer: ReturnType<typeof setInterval> | undefined;

async function load(): Promise<void> {
  try {
    pulse.value = await fetchPulse(Date.now());
  } catch {
    // Keep what was shown; the next refresh tries again.
  }
}
onMounted(() => {
  void load();
  timer = setInterval(() => void load(), REFRESH_MS);
});
onBeforeUnmount(() => {
  clearInterval(timer);
  clearCursor('pulse');
});

const slots = computed(() => pulse.value?.slots ?? []);
const shown = computed(() => pulse.value?.level !== 'off' && slots.value.some((s) => pulseTone(s) !== 'none'));
const tones = computed(() => slots.value.map(pulseTone));

/** The slot the cursor is on, whoever set it. */
const index = computed(() => (cursor.value ? pulseSlotAt(pulse.value, cursor.value.at) : undefined));

/** "Plex down, Sonarr needs attention", worst first, at most three named. */
function issuesWords(s: PulseSlot): string {
  const rank: Record<string, number> = { down: 0, repair: 1, attention: 2 };
  const list = Object.entries(s.issues).sort((a, b) => (rank[a[1]] ?? 9) - (rank[b[1]] ?? 9));
  const named = list.slice(0, 3).map(([id, st]) => `${label(live.graph.value?.resources[id]) || id} ${stateWords(st).toLowerCase()}`);
  const rest = list.length - named.length;
  return named.join(', ') + (rest ? `, ${rest} more` : '');
}

function words(i: number): string {
  const p = pulse.value!;
  const s = p.slots[i]!;
  const when = `${hhmm(p.start + i * p.step)}–${hhmm(p.start + (i + 1) * p.step)}`;
  if (pulseTone(s) === 'none') return `${when}: nothing watched`;
  if (!Object.keys(s.issues).length) return `${when}: all ${plural(s.counts.ok ?? 0, 'resource')} healthy`;
  return `${when}: ${issuesWords(s)}`;
}
const readout = computed(() => (index.value === undefined ? '' : words(index.value)));

/** For assistive tech: how many quarter hours had something down or needing attention. */
const summary = computed(() => {
  const down = tones.value.filter((t) => t === 'down').length;
  const attn = tones.value.filter((t) => t === 'attention').length;
  if (!down && !attn) return 'Health over the last 24 hours: all healthy';
  return `Health over the last 24 hours: ${[down ? `something down in ${plural(down, 'quarter hour')}` : '', attn ? `something needing attention in ${plural(attn, 'quarter hour')}` : ''].filter(Boolean).join(', ')}`;
});

function select(i: number): void {
  const p = pulse.value;
  if (!p || !p.slots.length) return;
  const k = Math.min(p.slots.length - 1, Math.max(0, i));
  setCursor(p.start + k * p.step + p.step / 2, 'pulse');
}
function move(e: PointerEvent): void {
  const box = el.value?.getBoundingClientRect();
  if (!box || box.width <= 0) return;
  select(Math.floor(((e.clientX - box.left) / box.width) * slots.value.length));
}
const leave = () => clearCursor('pulse');
function key(e: KeyboardEvent): void {
  const n = slots.value.length;
  const now = index.value ?? n - 1;
  const to: Record<string, number> = { ArrowLeft: now - 1, ArrowDown: now - 1, ArrowRight: now + 1, ArrowUp: now + 1, PageDown: now - 4, PageUp: now + 4, Home: 0, End: n - 1 };
  if (e.key === 'Escape') {
    leave();
    return;
  }
  if (!(e.key in to)) return;
  e.preventDefault();
  select(to[e.key]!);
}
</script>

<template>
  <div v-if="shown" :class="['pulse', { 'is-on': index !== undefined }]">
    <div
      ref="el"
      class="pulse-bar"
      role="slider"
      tabindex="0"
      aria-label="Health over the last 24 hours"
      :aria-valuemin="0"
      :aria-valuemax="slots.length - 1"
      :aria-valuenow="index ?? slots.length - 1"
      :aria-valuetext="index === undefined ? summary : readout"
      @pointermove="move"
      @pointerdown="move"
      @pointerleave="leave"
      @pointercancel="leave"
      @focus="select(index ?? slots.length - 1)"
      @blur="leave"
      @keydown="key"
    >
      <span v-for="(t, i) in tones" :key="i" :class="['pulse-slot', `t-${t}`, { on: i === index }]"></span>
    </div>
    <div class="pulse-axis" aria-hidden="true">
      <span>24 h ago</span>
      <span v-if="readout" class="pulse-read">{{ readout }}</span>
      <span>Now</span>
    </div>
  </div>
</template>

<style>
.pulse {
  max-width: 70ch;
  margin-top: var(--space-3);
}
.pulse-bar {
  display: flex;
  gap: 1px;
  height: 10px;
  border-radius: var(--radius-sm, 3px);
  overflow: hidden;
  cursor: crosshair;
  touch-action: pan-y;
}
.pulse-bar:focus-visible {
  outline: 2px solid var(--focus, var(--text));
  outline-offset: 3px;
}
.pulse-slot {
  flex: 1 1 0;
  min-width: 0;
  background: color-mix(in oklch, var(--text) 16%, transparent);
}
.pulse-slot.t-attention {
  background: var(--attention);
}
.pulse-slot.t-down {
  background: var(--down);
}
.pulse-slot.t-none {
  background: repeating-linear-gradient(135deg, transparent 0 3px, color-mix(in oklch, var(--text) 9%, transparent) 3px 4px);
}
.pulse.is-on .pulse-slot:not(.on) {
  opacity: 0.55;
}
.pulse-slot.on {
  box-shadow: inset 0 0 0 1.5px var(--text);
}
.pulse-axis {
  display: flex;
  gap: var(--space-2);
  justify-content: space-between;
  margin-top: 4px;
  color: var(--text-3);
  font-size: var(--type-small);
  font-variant-numeric: tabular-nums;
}
.pulse-read {
  min-width: 0;
  overflow: hidden;
  color: var(--text);
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
