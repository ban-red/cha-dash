<script setup lang="ts">
/**
 * The Pulse strip (01 · Vision → The Briefing): the whole home's health over
 * the last 24 hours, a slot a quarter hour, coloured by the worst anything
 * was (GET /api/history/pulse). Quiet grey is all well, a gap is a time
 * nothing was watched.
 *
 * Hovering reads the strip alone. A click pins a moment, and the board peeks
 * into the past: the strip sets the page's time cursor (stores/cursor.ts),
 * so every board sparkline marks its value then, each tile shows its health
 * then, and the Peek banner says so. Dragging moves the pin; clicking the
 * pinned slot again, Esc, a click elsewhere or the banner's Back to now
 * lets go. A sparkline being scrubbed marks its moment here too.
 *
 * It's a slider for keyboards and assistive tech: arrows move a quarter
 * hour, Page Up and Down an hour, Home and End the ends, and each pins
 * where it lands; Esc lets go. The strip hides while the hub keeps no
 * history or has none for the day yet.
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

/** The slot under a pointer that only hovers: read here, not shared. */
const hover = ref<number | undefined>();
/** The slot pinned here, which the board is peeking at. */
const pinned = computed(() => (cursor.value?.source === 'pulse' ? pulseSlotAt(pulse.value, cursor.value.at) : undefined));
/** The slot hovered, else the one the cursor is on, whoever set it. */
const index = computed(() => hover.value ?? (cursor.value ? pulseSlotAt(pulse.value, cursor.value.at) : undefined));
/** Pressed: the pointer drags the pin. */
let pressed = false;
/** The press began on the pinned slot and hasn't moved off it: letting go unpins. */
let unpin = false;

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

const clamp = (i: number) => Math.min(slots.value.length - 1, Math.max(0, i));
function select(i: number): void {
  const p = pulse.value;
  if (!p || !p.slots.length) return;
  const k = clamp(i);
  setCursor(p.start + k * p.step + p.step / 2, 'pulse');
}
/** The slot under the pointer, or undefined off a strip with no size. */
function slotOf(e: PointerEvent): number | undefined {
  const box = el.value?.getBoundingClientRect();
  if (!box || box.width <= 0 || !slots.value.length) return undefined;
  return clamp(Math.floor(((e.clientX - box.left) / box.width) * slots.value.length));
}
function move(e: PointerEvent): void {
  const i = slotOf(e);
  if (i === undefined) return;
  hover.value = i;
  if (!pressed || i === pinned.value) return;
  unpin = false;
  select(i);
}
function press(e: PointerEvent): void {
  const i = slotOf(e);
  if (i === undefined) return;
  pressed = true;
  // Keep the drag ours when it strays off the strip.
  (e.currentTarget as HTMLElement).setPointerCapture?.(e.pointerId);
  unpin = i === pinned.value;
  hover.value = i;
  select(i);
}
function release(): void {
  if (!pressed) return;
  pressed = false;
  if (unpin) clearCursor('pulse');
  unpin = false;
}
/** The pointer left: the hover goes, the pin stays. */
function leave(): void {
  pressed = false;
  unpin = false;
  hover.value = undefined;
}
function key(e: KeyboardEvent): void {
  const n = slots.value.length;
  const now = pinned.value ?? n - 1;
  const to: Record<string, number> = { ArrowLeft: now - 1, ArrowDown: now - 1, ArrowRight: now + 1, ArrowUp: now + 1, PageDown: now - 4, PageUp: now + 4, Home: 0, End: n - 1 };
  if (e.key === 'Escape') {
    if (pinned.value === undefined) return;
    e.stopPropagation();
    clearCursor('pulse');
    return;
  }
  if (!(e.key in to)) return;
  e.preventDefault();
  hover.value = undefined;
  select(to[e.key]!);
}
</script>

<template>
  <div v-if="shown" :class="['pulse', { 'is-on': index !== undefined, 'is-pinned': pinned !== undefined }]">
    <div
      ref="el"
      class="pulse-bar"
      role="slider"
      tabindex="0"
      aria-label="Health over the last 24 hours"
      :aria-valuemin="0"
      :aria-valuemax="slots.length - 1"
      :aria-valuenow="pinned ?? slots.length - 1"
      :aria-valuetext="pinned === undefined ? summary : words(pinned)"
      @pointermove="move"
      @pointerdown="press"
      @pointerup="release"
      @pointerleave="leave"
      @pointercancel="leave"
      @keydown="key"
    >
      <span v-for="(t, i) in tones" :key="i" :class="['pulse-slot', `t-${t}`, { on: i === index, pin: i === pinned }]"></span>
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
.pulse-slot.pin {
  opacity: 1;
  box-shadow: inset 0 0 0 1.5px var(--accent);
  outline: 2px solid var(--accent);
  outline-offset: 1px;
}
.pulse.is-pinned .pulse-bar {
  overflow: visible;
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
