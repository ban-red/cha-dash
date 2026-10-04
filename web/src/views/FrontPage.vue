<script setup lang="ts">
/**
 * The signed-in app: the front page plus its layers. Creates the live, board
 * and UI stores for everything below it. The Inbox and the confirm dialog are
 * loaded on first use (they carry motion-v), keeping the first paint small.
 */
import { defineAsyncComponent, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import Briefing from '../components/Briefing.vue';
import HubBanner from '../components/HubBanner.vue';
import NeedsAttention from '../components/NeedsAttention.vue';
import Toasts from '../components/Toasts.vue';
import BoardSurface from '../board/BoardSurface.vue';
import { api } from '../lib/api';
import { setDisplayTimeZone } from '../lib/format';
import type { GraphDoc } from '../model/types';
import { createBoardStore, provideBoard } from '../stores/board';
import { createLive, provideLive } from '../stores/live';
import { useSession } from '../stores/session';
import { createUi, provideUi } from '../stores/ui';

const InboxSheet = defineAsyncComponent(() => import('../components/InboxSheet.vue'));
const ConfirmDialog = defineAsyncComponent(() => import('../components/ConfirmDialog.vue'));

const session = useSession();
const live = createLive({ checkSession: session.check });
const ui = createUi(live);
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

const inboxLoaded = ref(false);
const confirmLoaded = ref(false);
watch(
  () => ui.state.inboxOpen,
  (v) => v && (inboxLoaded.value = true),
);
watch(
  () => ui.state.confirm,
  (v) => v && (confirmLoaded.value = true),
);

async function demo(): Promise<void> {
  try {
    const d = await api<{ enabled: boolean; now: string; seed: number }>('/api/demo');
    if (d?.enabled) {
      // The Demo Home keeps UTC as its clock, and its copy quotes UTC times.
      setDisplayTimeZone('UTC');
      live.setHubTime(d.now);
    }
  } catch {
    /* 404 outside demo mode: local time */
  }
}

async function signOut(): Promise<void> {
  live.stop();
  await session.logout();
}

onMounted(() => {
  live.start();
  void demo();
  void board.load();
});
onBeforeUnmount(() => live.stop());
</script>

<template>
  <div class="screen">
    <div class="frame">
      <main class="page" aria-label="Front page">
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
  <InboxSheet v-if="inboxLoaded" />
  <ConfirmDialog v-if="confirmLoaded" />
  <Toasts />
</template>
