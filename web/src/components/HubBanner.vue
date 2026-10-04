<script setup lang="ts">
/**
 * Hub offline / stale: the page keeps its last-known values under a clear
 * banner. Appears after a short grace period, so a quick reconnect doesn't
 * flash it.
 */
import { computed } from 'vue';
import Glyph from './marks/Glyph.vue';
import { hhmm } from '../lib/format';
import { useLive } from '../stores/live';

const live = useLive();
const GRACE_MS = 3000;

const wall = live.wall;
const mode = computed<'offline' | 'stale' | 'incompatible' | ''>(() => {
  const c = live.conn;
  if (c.status === 'incompatible') return 'incompatible';
  if (c.status === 'reconnecting' && c.offlineSince !== undefined) {
    return wall.value - c.offlineSince > GRACE_MS || c.attempt > 1 ? 'offline' : '';
  }
  if (c.status === 'live' && live.stale.value) return 'stale';
  return '';
});
const since = computed(() => hhmm(live.offlineAt.value));
const retryIn = computed(() => {
  const at = live.conn.nextRetryAt;
  if (!at) return '';
  const s = Math.max(0, Math.ceil((at - wall.value) / 1000));
  return s > 0 ? `Retrying in ${s} s` : 'Retrying now';
});
const reload = () => location.reload();
</script>

<template>
  <div v-if="mode === 'offline'" class="banner" role="status">
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
</template>
