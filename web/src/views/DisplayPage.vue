<script setup lang="ts">
/**
 * A wall display (docs/plan/15-v0.5.md → Wave 33): one board, read-only, no
 * session and no chrome. No sign-in, no Briefing, no Inbox, no menus, no
 * command bar, no sheets and no edit mode; a tile's click opens nothing.
 *
 *   POST /api/display/claim  {token}  → 204, sets the display's cookie (Path=/api/display)
 *   GET  /api/display                 → {display, board, layout, zone, offset_minutes, now, icons?}
 *   GET  /api/display/ws              → the realtime socket, without `tasks`
 *   GET  /api/display/icons/{id}      → tile icons
 *
 * The link Settings → Displays shows carries the secret in the fragment
 * (`/display/<board>#cdc_…`, a claim code good once): it's taken and dropped from the address bar at
 * once (lib/router.ts → takeDisplaySecret), claimed, and never kept here.
 * A 401 from the hub means the display isn't signed in, or was revoked: the
 * page says so, calmly, and how to fix it.
 *
 * The calm (brightness, night tint, the incident that woke it, the burn-in
 * shift, how old the data is) comes from lib/display.ts, ticked by a timer.
 *
 * The display's style (wave 34) picks the rest:
 *   wall  the board at the simplified level (lib/level.ts), calm as above;
 *   tv    the same, larger, for 3 m away; a remote's arrow keys move focus
 *         between tiles (lib/spatial.ts), and the focused tile shows what
 *         the simplified level leaves out, so nothing hides behind a hover;
 *   ink   an e-ink screen: the ink level (one colour, no motion, absolute
 *         times), never dimmed, tinted or moved. The wake line and the
 *         data's age still show.
 *
 * Its own chunk: App.vue loads it only on /display/….
 */
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue';
import type { Board } from '@chadash/layout';
import StaticBoard from '../board/StaticBoard.vue';
import SectionHeader from '../board/SectionHeader.vue';
import WidgetHost from '../widgets/WidgetHost.vue';
import Glyph from '../ui/marks/Glyph.vue';
import UiButton from '../ui/Button.vue';
import { ApiError, api } from '../lib/api';
import { calm, changeKey, type QuietHours } from '../lib/display';
import { setDisplayTimeZone } from '../lib/format';
import { takeDisplaySecret } from '../lib/router';
import type { Level } from '../lib/level';
import { ARROWS, nextBox } from '../lib/spatial';
import { defaultBoard } from '../model/defaultLayout';
import type { GraphDoc, Incident } from '../model/types';
import type { WebSocketFactory } from '../realtime/client';
import { createBoardStore, provideBoard } from '../stores/board';
import { seedDisplayIcons, type IconMap } from '../stores/icons';
import { DISPLAY_TOPICS, createLive, provideLive } from '../stores/live';
import { createSession, provideSession } from '../stores/session';
import { tilesFromDoc } from '../stores/tiles';
import { createUi, provideUi } from '../stores/ui';

export type DisplayStyle = 'wall' | 'tv' | 'ink';

export interface DisplayInfo {
  display: { id: string; name: string; quiet: QuietHours | null; style?: DisplayStyle };
  board: { id: string; name: string };
  layout: { revision?: number; format_version?: number; layout: unknown; tiles?: unknown } | null;
  zone: string;
  offset_minutes: number;
  now: number;
  icons?: Partial<IconMap>;
}

const {
  board: routeBoard = '',
  createSocket,
  tickMs = 15_000,
  now: wallNow = () => Date.now(),
} = defineProps<{
  /** The board in the path; the display's own board wins. */
  board?: string;
  /** Tests: a fake socket. */
  createSocket?: WebSocketFactory | undefined;
  /** How often the calm is worked out again. */
  tickMs?: number;
  /** Tests: the browser's clock. */
  now?: () => number;
}>();

const phase = ref<'loading' | 'ready' | 'signed-out' | 'unreachable'>('loading');
const info = ref<DisplayInfo | undefined>();

// No session: the widgets see a household viewer, and the UI opens nothing.
const session = createSession();
session.state.phase = 'ready';
session.state.user = { id: '', username: 'display', role: 'household' };
provideSession(session);

async function stillSignedIn(): Promise<boolean> {
  try {
    await api('/api/display');
    return true;
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      phase.value = 'signed-out';
      return false;
    }
    return true; // the hub is away: keep retrying
  }
}

const live = createLive({
  path: '/api/display/ws',
  topics: DISPLAY_TOPICS,
  tick: false,
  now: wallNow,
  checkSession: stillSignedIn,
  ...(createSocket ? { createSocket } : {}),
});
const ui = createUi(live, { readOnly: true, role: () => 'household' });
provideLive(live);
provideUi(ui);
const store = createBoardStore({ waitForGraph: async () => live.graph.value, now: () => live.now.value });
provideBoard(store);
/** A board whose layout was never saved: the default, from the graph when it first arrives. */
const fallback = shallowRef<Board | undefined>();
watch(
  () => live.graph.value,
  (g: GraphDoc | undefined) => {
    if (g && !fallback.value && !store.board.value) fallback.value = defaultBoard(g, live.now.value);
  },
);
const shown = computed<Board | undefined>(() => store.board.value ?? (phase.value === 'ready' ? fallback.value : undefined));

function show(d: DisplayInfo): void {
  info.value = d;
  if (d.zone) setDisplayTimeZone(d.zone);
  if (d.now) live.setHubTime(new Date(d.now).toISOString());
  seedDisplayIcons(d.icons);
  const layout = d.layout?.layout;
  const seeded = layout !== null && layout !== undefined && store.seed([d.board], { id: d.board.id, name: d.board.name, revision: d.layout?.revision ?? 0, layout, tiles: d.layout?.tiles });
  if (!seeded) store.tiles.value = tilesFromDoc(d.layout?.tiles);
  // The address names the display's board, whichever the link said.
  if (d.board.id && d.board.id !== routeBoard) history.replaceState(history.state, '', `/display/${encodeURIComponent(d.board.id)}`);
  phase.value = 'ready';
}

async function load(): Promise<void> {
  phase.value = 'loading';
  try {
    show(await api<DisplayInfo>('/api/display'));
  } catch (err) {
    phase.value = err instanceof ApiError && err.status === 401 ? 'signed-out' : 'unreachable';
    return;
  }
  live.start();
}

async function start(): Promise<void> {
  const secret = takeDisplaySecret();
  if (secret) {
    try {
      await api('/api/display/claim', { method: 'POST', body: { token: secret } });
    } catch (err) {
      if (err instanceof ApiError && (err.status === 401 || err.status === 400)) {
        // A used-up or revoked link; a cookie from before may still be good.
        if (!(await stillSignedIn())) return;
      } else {
        phase.value = 'unreachable';
        return;
      }
    }
  }
  await load();
}

// ── calm ──
/** Incidents open when the page first saw the Inbox: they don't wake it. */
const seen = ref<ReadonlySet<string> | undefined>();
const lastChange = ref(live.now.value);
watch(
  () => changeKey(live.graph.value, live.inbox.value),
  (k, was) => {
    if (k === was) return;
    live.tick();
    lastChange.value = live.now.value;
  },
);
watch(
  () => live.inbox.value,
  (inbox) => {
    if (inbox && !seen.value) seen.value = new Set(Object.values(inbox.incidents ?? {}).filter((i) => i.status === 'open').map((i) => i.id));
  },
  { immediate: true },
);

const state = computed(() =>
  calm({
    now: live.now.value,
    zone: info.value?.zone ?? '',
    offsetMinutes: info.value?.offset_minutes ?? 0,
    incidents: Object.values(live.inbox.value?.incidents ?? {}) as Incident[],
    seen: seen.value ?? new Set(),
    lastChange: lastChange.value,
    live: !live.stale.value && live.conn.status === 'live',
    lastDataAt: live.offlineAt.value ?? 0,
    quiet: info.value?.display.quiet ?? null,
  }),
);
const style = computed<DisplayStyle>(() => info.value?.display.style ?? 'wall');
const level = computed<Level>(() => (style.value === 'ink' ? 'ink' : 'simplified'));
/** E-ink has no backlight and no burn-in: it's never dimmed, tinted or moved. */
const shown$ = computed(() => (style.value === 'ink' ? { ...state.value, brightness: 'full' as const, tint: false, shift: { x: 0, y: 0 } } : state.value));
const shift = computed(() => ({ transform: `translate(${shown$.value.shift.x}px, ${shown$.value.shift.y}px)` }));

// ── TV: arrow keys move between tiles ──
const content = ref<HTMLElement | null>(null);
function cells(): HTMLElement[] {
  return [...(content.value?.querySelectorAll<HTMLElement>('.cell') ?? [])];
}
function onKey(e: KeyboardEvent): void {
  const dir = ARROWS[e.key];
  if (style.value !== 'tv' || !dir) return;
  const list = cells();
  if (!list.length) return;
  const from = list.findIndex((c) => c === document.activeElement || c.contains(document.activeElement));
  const boxes = list.map((c) => {
    const r = c.getBoundingClientRect();
    return { x: r.left, y: r.top, w: r.width, h: r.height };
  });
  const to = from < 0 ? 0 : nextBox(boxes, from, dir);
  if (to < 0) return;
  e.preventDefault();
  for (const c of list) c.tabIndex = -1;
  list[to]!.tabIndex = 0;
  list[to]!.focus();
}
// The first tile takes focus, so the first arrow has somewhere to start.
watch(
  () => [style.value, shown.value] as const,
  async ([st]) => {
    if (st !== 'tv') return;
    await new Promise((r) => setTimeout(r));
    const list = cells();
    list.forEach((c, i) => (c.tabIndex = i === 0 ? 0 : -1));
  },
);

/** A tile's link would leave the display: links don't open here. */
function noLinks(e: MouseEvent): void {
  if (e.target instanceof Element && e.target.closest('a[href]')) e.preventDefault();
}

let timer: ReturnType<typeof setInterval> | undefined;
onMounted(() => {
  timer = setInterval(() => live.tick(), tickMs);
  window.addEventListener('keydown', onKey);
  void start();
});
onBeforeUnmount(() => {
  clearInterval(timer);
  window.removeEventListener('keydown', onKey);
  live.stop();
});
</script>

<template>
  <main v-if="phase === 'loading'" class="door" aria-busy="true"><span class="caps">Cha Dash</span></main>
  <main v-else-if="phase === 'signed-out'" class="door">
    <div class="door-card" role="alert">
      <div class="wordmark-row"><span class="brand">Cha Dash</span><span class="caps">Display</span></div>
      <h1>This display isn’t signed in</h1>
      <p class="lede">Its link was revoked or never claimed. On a signed-in device, open Settings → Displays, make a new link for this screen, and open it here.</p>
    </div>
  </main>
  <main v-else-if="phase === 'unreachable'" class="door">
    <div class="door-card" role="alert">
      <div class="wordmark-row"><span class="brand">Cha Dash</span><span class="caps">Hub unreachable</span></div>
      <h1><Glyph kind="down" label="unreachable" />Can’t reach the hub</h1>
      <p class="lede">The display tries again by itself.</p>
      <UiButton variant="primary" @click="start()">Try again</UiButton>
    </div>
  </main>
  <main
    v-else
    class="dsp"
    :aria-label="info?.display.name || 'Display'"
    :data-style="style"
    :data-brightness="shown$.brightness"
    :data-tint="shown$.tint || undefined"
    :data-stale="state.stale ? '' : undefined"
  >
    <h1 class="sr">{{ info?.board.name }}</h1>
    <div ref="content" class="dsp-content" :style="shift" @click.capture="noLinks">
      <p v-if="state.wake" class="dsp-wake" :data-severity="state.wake.severity" role="status">
        <Glyph :kind="state.wake.severity === 'down' ? 'down' : 'attn'" :label="state.wake.severity" />
        <span>{{ state.wake.text }}</span>
      </p>
      <ul v-if="state.acknowledged.length" class="dsp-acked" aria-label="Someone is on these">
        <li v-for="a in state.acknowledged" :key="a.id">
          <Glyph :kind="a.severity === 'down' ? 'down' : 'attn'" :label="a.severity" />
          <span>{{ a.text }}</span> <span class="dsp-by">{{ a.by }} is on it</span>
        </li>
      </ul>
      <StaticBoard v-if="shown" :board="shown" :level="level">
        <template #section="{ section }"><SectionHeader :section="section" /></template>
        <template #item="{ item, size }"><WidgetHost :item="item" :size="size" /></template>
      </StaticBoard>
    </div>
    <div class="dsp-veil" aria-hidden="true"></div>
    <div class="dsp-tint" aria-hidden="true"></div>
    <p v-if="state.stale" class="dsp-stale" role="status">{{ state.stale.text }}</p>
  </main>
</template>

<style>
.dsp {
  position: relative;
  min-height: 100vh;
  min-height: 100dvh;
  padding: var(--space-6) clamp(16px, 3vw, 40px);
  overflow: hidden;
}
.dsp-content {
  max-width: var(--board-max);
  margin: 0 auto;
  /* Slow on purpose: a display moves and dims over seconds, not a press. */
  transition: transform calc(var(--motion-move-duration) * 4) ease-in-out;
}
.dsp-content .clickable {
  cursor: default;
}
.dsp-wake {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  margin: 0 0 var(--space-5);
  font: var(--weight-medium) var(--type-h2, 1.5rem) / 1.25 var(--font-ui);
}
.dsp-wake .g {
  width: 1em;
  height: 1em;
}
.dsp-acked {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2) var(--space-5);
  margin: 0 0 var(--space-4);
  padding: 0;
  list-style: none;
  color: var(--text-3);
  opacity: 0.7;
}
.dsp-acked li {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
}
.dsp-by {
  font-size: var(--type-small);
}
/* Dim and dark: a veil over the page, under the wake line and the stale notice. */
.dsp-veil,
.dsp-tint {
  position: fixed;
  inset: 0;
  z-index: 2;
  pointer-events: none;
  opacity: 0;
  transition: opacity calc(var(--motion-move-duration) * 6) ease-in-out;
}
.dsp-veil {
  background: #000;
}
.dsp[data-brightness='dim'] .dsp-veil {
  opacity: 0.5;
}
.dsp[data-brightness='dark'] .dsp-veil {
  opacity: 0.94;
}
.dsp-tint {
  background: #ff8a2a;
  mix-blend-mode: multiply;
}
.dsp[data-tint] .dsp-tint {
  opacity: 0.16;
}
.dsp-stale {
  position: fixed;
  right: var(--space-4);
  bottom: var(--space-4);
  z-index: 4;
  margin: 0;
  padding: var(--space-2) var(--space-3);
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-md);
  font-size: var(--type-small);
  color: var(--text-2);
  background: var(--bg);
}
/* TV: read from 3 m. The board is drawn larger, and the focused tile shows
   what the simplified level leaves out, so nothing needs a hover. */
.dsp[data-style='tv'] .dsp-content {
  zoom: 1.5;
}
.dsp[data-style='tv'] .cell:focus {
  outline: none;
}
.dsp[data-style='tv'] .cell:focus > .tile {
  box-shadow:
    0 0 0 3px var(--bg),
    0 0 0 6px var(--focus);
}
.dsp[data-style='tv'] .cell:focus .tile.lvl-simplified :is(.kv, .tf, .sub ~ .sub) {
  display: revert;
}
.dsp[data-style='tv'] .cell:focus .tile.lvl-simplified .list > .li {
  display: revert;
}
.dsp[data-style='ink'] .dsp-veil,
.dsp[data-style='ink'] .dsp-tint {
  display: none;
}
@media (prefers-reduced-motion: reduce) {
  html:not([data-motion='full']) .dsp-content,
  html:not([data-motion='full']) .dsp-veil,
  html:not([data-motion='full']) .dsp-tint {
    transition: none;
  }
}
:root[data-motion='reduced'] .dsp-content,
:root[data-motion='none'] .dsp-content,
:root[data-motion='reduced'] .dsp-veil,
:root[data-motion='none'] .dsp-veil,
:root[data-motion='reduced'] .dsp-tint,
:root[data-motion='none'] .dsp-tint {
  transition: none;
}
</style>
