<script setup lang="ts">
/**
 * The signed-in app: the front page plus its layers. Creates the live, board
 * and UI stores for everything below it, and owns what spans them:
 *
 * - the route (which board; Settings as a layer over the page);
 * - ⌘K / Ctrl K and / for the command bar;
 * - the hub's first-paint snapshot, inlined into index.html: the page and
 *   its board render from it at once, and the socket supersedes it;
 * - the last-known snapshot: written while live, and the page opens on it
 *   when the hub was unreachable at start-up (session.state.offline);
 * - demo mode (GET /api/demo): UTC times, and the demo Fix fallback;
 * - the viewer's role: what the page hides (model/roles.ts; the hub enforces);
 * - label discovery for the board on screen (stores/discovery.ts);
 * - the first-run wizard (onboarding/), when the hub says to show it.
 *
 * The Inbox, the confirm dialogs (hold, typed), step-up, the command bar and
 * Settings load on first use, keeping the first paint small.
 */
import { defineAsyncComponent, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import Briefing from '../components/Briefing.vue';
import HubBanner from '../components/HubBanner.vue';
import NeedsAttention from '../components/NeedsAttention.vue';
import TaskPresence from '../components/TaskPresence.vue';
import Toasts from '../components/Toasts.vue';
import BoardSurface from '../board/BoardSurface.vue';
import { api } from '../lib/api';
import { setDisplayTimeZone } from '../lib/format';
import { view } from '../lib/router';
import type { GraphDoc } from '../model/types';
import { createBoardStore, provideBoard } from '../stores/board';
import { createDiscovery, provideDiscovery } from '../stores/discovery';
import { createLive, provideLive } from '../stores/live';
import { persistSnapshots } from '../stores/persist';
import { useSession } from '../stores/session';
import { loadOnboarding, onboarding } from '../onboarding/state';
import { createUi, provideUi } from '../stores/ui';

const InboxSheet = defineAsyncComponent(() => import('../components/InboxSheet.vue'));
const ConfirmDialog = defineAsyncComponent(() => import('../components/ConfirmDialog.vue'));
const TypeConfirmDialog = defineAsyncComponent(() => import('../components/TypeConfirmDialog.vue'));
const StepUpDialog = defineAsyncComponent(() => import('../components/StepUpDialog.vue'));
const CommandPalette = defineAsyncComponent(() => import('../components/CommandPalette.vue'));
const SettingsSheet = defineAsyncComponent(() => import('../settings/SettingsSheet.vue'));
const OnboardingWizard = defineAsyncComponent(() => import('../onboarding/OnboardingWizard.vue'));

const session = useSession();
const live = createLive({ checkSession: session.check });
const ui = createUi(live, { signOut, role: () => session.state.user?.role });
provideLive(live);
provideUi(ui);

/** The default layout needs the graph; wait for it, but not forever. */
function waitForGraph(): Promise<GraphDoc | undefined> {
  if (live.graph.value) return Promise.resolve(live.graph.value);
  return new Promise((resolve) => {
    const stop = watch(live.graph, (g) => {
      if (g) {
        stop();
        clearTimeout(timer);
        resolve(g);
      }
    });
    const timer = setTimeout(() => {
      stop();
      resolve(live.graph.value);
    }, 6000);
  });
}
const board = createBoardStore({ waitForGraph, now: () => live.now.value });
provideBoard(board);
provideDiscovery(createDiscovery(live, board));

// Signed in with the hub's first-paint snapshot: render from it now; the socket supersedes it.
const inline = session.takeInline();
let seeded = false;
if (inline) {
  if (inline.demo) setDisplayTimeZone('UTC');
  live.demo.value = inline.demo === true;
  live.hydrate(inline);
  if (inline.board && inline.board.layout != null) seeded = board.seed(inline.boards, inline.board);
}

// Opened while the hub was unreachable: show the last-known page.
const restored = session.takeRestored();
if (restored) {
  if (restored.timeZone) setDisplayTimeZone(restored.timeZone);
  live.demo.value = restored.demo === true;
  live.restore(restored);
  board.restore(restored.board);
}

const inboxLoaded = ref(false);
const confirmLoaded = ref(false);
watch(
  () => ui.state.inboxOpen,
  (v) => v && (inboxLoaded.value = true),
);
watch(
  () => ui.state.confirm,
  (v) => v && v.kind !== 'type' && (confirmLoaded.value = true),
);
/** Each step-up request mounts a fresh dialog (it asks the hub anew). */
let stepUps = 0;
const stepUpKey = ref(0);
watch(
  () => ui.state.stepUp,
  (v) => v && (stepUpKey.value = ++stepUps),
);

// The board follows the route: /boards/{id}, or the first board at /.
watch(
  () => (view.value.name === 'front' ? (view.value.board ?? '') : undefined),
  (want) => {
    if (want === undefined || board.state.status === 'loading' || board.state.status === 'idle') return;
    const first = board.state.boards[0]?.id ?? '';
    if ((want || first) !== board.state.boardId) void board.load(want || undefined);
  },
);

// Back from an offline start: confirm the session with the hub.
watch(
  () => live.conn.status,
  (s) => {
    if (s === 'live' && session.state.offline) {
      void session.confirm();
      if (board.state.lastKnown) void board.load(board.state.boardId || undefined);
    }
  },
);

async function demo(): Promise<void> {
  try {
    const d = await api<{ enabled: boolean; now: string; seed: number }>('/api/demo');
    live.demo.value = d?.enabled === true;
    if (d?.enabled) {
      // The Demo Home keeps UTC as its clock, and its copy quotes UTC times.
      setDisplayTimeZone('UTC');
      live.setHubTime(d.now);
    }
  } catch {
    /* 404 outside demo mode: local time, and actions wait for the action engine */
  }
}

async function signOut(): Promise<void> {
  live.stop();
  await session.logout();
}

const isTyping = (t: EventTarget | null) =>
  t instanceof HTMLElement && (t.isContentEditable || t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT');

function onKey(e: KeyboardEvent): void {
  if ((e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === 'k') {
    e.preventDefault();
    if (ui.state.paletteOpen) ui.closePalette();
    else ui.openPalette();
    return;
  }
  if (e.key === '/' && !e.metaKey && !e.ctrlKey && !e.altKey && !isTyping(e.target) && !ui.state.confirm && !ui.state.paletteOpen) {
    e.preventDefault();
    ui.openPalette();
  }
}

let stopPersist: (() => void) | undefined;

onMounted(() => {
  live.start();
  if (!restored) void demo();
  if (!restored) void loadOnboarding();
  const want = view.value.name === 'front' ? view.value.board : undefined;
  if (!seeded || (want !== undefined && want !== board.state.boardId)) void board.load(want);
  document.addEventListener('keydown', onKey);
  stopPersist = persistSnapshots(live, board, session);
});
onBeforeUnmount(() => {
  live.stop();
  stopPersist?.();
  document.removeEventListener('keydown', onKey);
});

// The demo flag comes from the hub once it's reachable again.
watch(
  () => session.state.offline,
  (offline, was) => {
    if (was && !offline) void demo();
  },
);
</script>

<template>
  <div class="screen">
    <div class="frame">
      <main class="page" aria-label="Front page" :inert="(view.name === 'settings' && ui.can.value.settings) || onboarding.open || undefined">
        <Briefing @sign-out="signOut" />
        <HubBanner />
        <NeedsAttention />
        <BoardSurface v-if="board.board.value" :board="board.board.value" />
        <div v-else-if="board.state.status === 'error'" class="banner" role="alert">
          <span><b>{{ board.state.error }}</b></span>
          <button type="button" class="btn sm" @click="board.load()">Try again</button>
        </div>
      </main>
    </div>
  </div>
  <InboxSheet v-if="inboxLoaded && ui.can.value.inbox" />
  <ConfirmDialog v-if="confirmLoaded" />
  <TypeConfirmDialog v-if="ui.state.confirm?.kind === 'type'" :key="ui.state.confirm.plan?.id ?? 0" />
  <OnboardingWizard v-if="onboarding.open" />
  <SettingsSheet v-if="view.name === 'settings' && ui.can.value.settings" />
  <CommandPalette v-if="ui.state.paletteOpen" />
  <StepUpDialog v-if="ui.state.stepUp" :key="stepUpKey" />
  <TaskPresence />
  <Toasts />
</template>
