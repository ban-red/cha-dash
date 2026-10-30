<script setup lang="ts">
/**
 * The signed-in app: the front page plus its layers. Creates the live, board
 * and UI stores for everything below it, and owns what spans them:
 *
 * - the route (which board; Settings as a layer over the page; a
 *   notification's `?incident=` link opens the Inbox on it);
 * - ⌘K / Ctrl K and / for the command bar;
 * - the hub's first-paint snapshot, inlined into index.html: the page and
 *   its board render from it at once, and the socket supersedes it;
 * - the last-known snapshot: written while live, and the page opens on it
 *   when the hub was unreachable at start-up (session.state.offline);
 * - demo mode (GET /api/demo): the Demo Home's clock and zone, and "Demo" in the tab title;
 * - the viewer's role: what the page hides (model/roles.ts; the hub enforces);
 * - label discovery for the board on screen (stores/discovery.ts);
 * - the first-run wizard (onboarding/), when the hub says to show it (admin
 *   and owner: the wizard adds integrations).
 *
 * The Inbox, the confirm dialogs (hold, typed), step-up, the command bar and
 * Settings load on first use, keeping the first paint small.
 */
import { defineAsyncComponent, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import Briefing from '../components/Briefing.vue';
import HubBanner from '../components/HubBanner.vue';
import PeekBanner from '../components/PeekBanner.vue';
import NeedsAttention from '../components/NeedsAttention.vue';
import TaskPresence from '../components/TaskPresence.vue';
import Toasts from '../components/Toasts.vue';
import PageDrop from '../import/PageDrop.vue';
import BoardSurface from '../board/BoardSurface.vue';
import { api } from '../lib/api';
import { setDisplayTimeZone } from '../lib/format';
import { inboxLink, navigate, route, view } from '../lib/router';
import type { GraphDoc } from '../model/types';
import { createBoardStore, provideBoard } from '../stores/board';
import { createDiscovery, provideDiscovery } from '../stores/discovery';
import { createLive, provideLive } from '../stores/live';
import { persistSnapshots } from '../stores/persist';
import { useSession } from '../stores/session';
import { loadOnboarding, onboarding } from '../onboarding/state';
import { createUi, provideUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';

const InboxSheet = defineAsyncComponent(() => import('../components/InboxSheet.vue'));
const ResourceSheet = defineAsyncComponent(() => import('../components/ResourceSheet.vue'));
const ConfirmDialog = defineAsyncComponent(() => import('../components/ConfirmDialog.vue'));
const TypeConfirmDialog = defineAsyncComponent(() => import('../components/TypeConfirmDialog.vue'));
const StepUpDialog = defineAsyncComponent(() => import('../components/StepUpDialog.vue'));
const TaskLogDialog = defineAsyncComponent(() => import('../components/TaskLogDialog.vue'));
const NoteDialog = defineAsyncComponent(() => import('../components/NoteDialog.vue'));
const HomeAssistantPanel = defineAsyncComponent(() => import('../widgets/HomeAssistantPanel.vue'));
const CameraEventDialog = defineAsyncComponent(() => import('../widgets/CameraEventDialog.vue'));
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
provideDiscovery(createDiscovery(live, board, () => ui.can.value.editBoards));

// Signed in with the hub's first-paint snapshot: render from it now; the socket supersedes it.
const inline = session.takeInline();
let seeded = false;
if (inline) {
  if (inline.demo) setDisplayTimeZone(inline.time_zone || 'UTC', inline.zone_label);
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
/** The resource sheet mounts on first open (a deep link included) and stays, so its exit can play. */
const sheetLoaded = ref(false);
watch(
  () => ui.sheet.value,
  (v) => v && (sheetLoaded.value = true),
  { immediate: true },
);
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
    const d = await api<{ enabled: boolean; now: string; seed: number; time_zone?: string; zone_label?: string }>('/api/demo');
    live.demo.value = d?.enabled === true;
    if (d?.enabled) {
      // The Demo Home runs its own clock, shown in the zone the hub names
      // (the hub's local time by default), and its copy quotes it the same way.
      setDisplayTimeZone(d.time_zone || 'UTC', d.zone_label);
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
  // The wizard adds integrations: admin and owner only (the route is 403 below).
  if (!restored && ui.can.value.integrations) void loadOnboarding();
  const want = view.value.name === 'front' ? view.value.board : undefined;
  if (!seeded || (want !== undefined && want !== board.state.boardId)) void board.load(want);
  document.addEventListener('keydown', onKey);
  stopPersist = persistSnapshots(live, board, session);
  openFromLink();
});

/** A notification's link: `/?incident=<id>` opens the Inbox on it, `/?inbox=1` the Inbox. The query then goes. */
function openFromLink(): void {
  const link = inboxLink(route.value);
  if (!link) return;
  if (ui.can.value.inbox) ui.openInbox(link.incident);
  navigate(route.value.path, { replace: true });
}
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

// A demo tab says so in the tab strip too.
const baseTitle = document.title || 'Cha Dash';
watch(
  live.demo,
  (on) => {
    document.title = on ? `Demo · ${baseTitle}` : baseTitle;
  },
  { immediate: true },
);
onBeforeUnmount(() => {
  document.title = baseTitle;
});
</script>

<template>
  <div class="screen">
    <div class="frame">
      <main class="page" aria-label="Front page" :inert="(view.name === 'settings' && ui.can.value.settings) || onboarding.open || undefined">
        <Briefing @sign-out="signOut" />
        <HubBanner @sign-out="signOut" />
        <NeedsAttention />
        <BoardSurface v-if="board.board.value" :board="board.board.value" />
        <div v-else-if="board.state.status === 'error'" class="banner" role="alert">
          <span><b>{{ board.state.error }}</b></span>
          <UiButton size="sm" @click="board.load()">Try again</UiButton>
        </div>
      </main>
    </div>
  </div>
  <InboxSheet v-if="inboxLoaded && ui.can.value.inbox" />
  <ResourceSheet v-if="sheetLoaded" />
  <ConfirmDialog v-if="confirmLoaded" />
  <TypeConfirmDialog v-if="ui.state.confirm?.kind === 'type'" :key="ui.state.confirm.plan?.id ?? 0" />
  <OnboardingWizard v-if="onboarding.open" />
  <SettingsSheet v-if="view.name === 'settings' && ui.can.value.settings" />
  <CommandPalette v-if="ui.state.paletteOpen" />
  <StepUpDialog v-if="ui.state.stepUp" :key="stepUpKey" />
  <TaskLogDialog v-if="ui.state.taskLog" />
  <NoteDialog v-if="ui.state.note" />
  <CameraEventDialog v-if="ui.state.cameraEvent" :key="`${ui.state.cameraEvent.camera} ${ui.state.cameraEvent.event ?? ''}`" />
  <HomeAssistantPanel v-if="ui.state.homePanel" :key="ui.state.homePanel.app" />
  <PeekBanner />
  <TaskPresence />
  <Toasts />
  <PageDrop v-if="ui.can.value.settings" />
</template>
