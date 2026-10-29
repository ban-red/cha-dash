<script setup lang="ts">
/**
 * Hub offline / stale: the page keeps its last-known values under a clear
 * banner. Appears after a short grace period, so a quick reconnect doesn't
 * flash it. Otherwise, in demo mode, a standing note that the page is the
 * Demo Home on its own clock.
 */
import { computed } from 'vue';
import Glyph from './marks/Glyph.vue';
import { hhmm, shortDate } from '../lib/format';
import { useLive } from '../stores/live';

const live = useLive();
const GRACE_MS = 3000;

const wall = live.wall;
/** Opened on a stored snapshot and nothing live has arrived since. */
const restored = computed(() => live.restoredAt.value !== undefined && !live.conn.lastFrameAt);
const mode = computed<'offline' | 'stale' | 'incompatible' | ''>(() => {
  const c = live.conn;
  if (c.status === 'incompatible') return 'incompatible';
  // Last-known values from storage: say so straight away, no grace period.
  if (restored.value && c.status !== 'live') return 'offline';
  if (c.status === 'reconnecting' && c.offlineSince !== undefined) {
    return wall.value - c.offlineSince > GRACE_MS || c.attempt > 1 ? 'offline' : '';
  }
  if (c.status === 'live' && live.stale.value) return 'stale';
  return '';
});
const since = computed(() => hhmm(live.offlineAt.value));
const asOf = computed(() => {
  const t = live.offlineAt.value;
  if (t === undefined) return '';
  return live.now.value - t > 20 * 3_600_000 ? `${shortDate(t)}, ${hhmm(t)}` : hhmm(t);
});
const retryIn = computed(() => {
  const at = live.conn.nextRetryAt;
  if (!at) return '';
  const s = Math.max(0, Math.ceil((at - wall.value) / 1000));
  return s > 0 ? `Retrying in ${s} s` : 'Retrying now';
});
const reload = () => location.reload();
</script>

<template>
  <div v-if="mode === 'offline' && restored" class="banner" role="status">
    <Glyph kind="down" label="unreachable" />
    <span><b>Cha Dash hub unreachable.</b> You're looking at the last-known page, as of <span class="n nb">{{ asOf }}</span>. <span class="n nb">{{ retryIn }}</span></span>
    <button type="button" class="btn sm" @click="live.retry()">Retry now</button>
  </div>
  <div v-else-if="mode === 'offline'" class="banner" role="status">
    <Glyph kind="down" label="unreachable" />
    <span><b>Cha Dash hub unreachable since {{ since }}.</b> You're looking at the last-known page. <span class="n nb">{{ retryIn }}</span></span>
    <button type="button" class="btn sm" @click="live.retry()">Retry now</button>
  </div>
  <div v-else-if="mode === 'stale'" class="banner" role="status">
    <Glyph kind="repair" label="stale" />
    <span><b>No word from the hub since {{ since }}.</b> Values may be out of date while Cha Dash checks the connection.</span>
    <button type="button" class="btn sm" @click="live.retry()">Reconnect</button>
  </div>
  <div v-else-if="mode === 'incompatible'" class="banner" role="alert">
    <Glyph kind="attn" label="update" />
    <span><b>The hub was updated.</b> Reload to get the matching version of this page.</span>
    <button type="button" class="btn sm" @click="reload">Reload</button>
  </div>
  <div v-else-if="live.demo.value" class="banner demo" role="note" aria-label="Demo mode">
    <span class="demo-tag">Demo</span>
    <span><b>You're looking at the Demo Home</b>, a simulated lab running a scripted day on its own clock, in UTC. Its hosts, incidents and fixes are all made up.</span>
  </div>
</template>
