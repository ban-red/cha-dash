<script setup lang="ts">
/**
 * One of the correlator's questions in the Inbox: two reports that look
 * like one thing (an app and the container it runs in, two instances of one
 * service). "Merge" (or "Link them") and "Keep separate" answer it on the
 * hub, which remembers the answer; the item then resolves live. The hub
 * merges on its own only when it's certain: the same ID, the same address.
 */
import { ref } from 'vue';
import Glyph from './marks/Glyph.vue';
import type { Incident } from '../model/types';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';

const { incident } = defineProps<{ incident: Incident }>();
const live = useLive();
const ui = useUi();
const busy = ref(false);

async function answer(verdict: 'confirm' | 'reject'): Promise<void> {
  busy.value = true;
  try {
    await ui.answer(incident, verdict);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <div class="ib open sugg">
    <Glyph kind="link" label="suggestion" />
    <div>
      <div class="ib-title">{{ incident.title }}</div>
      <div class="m">{{ incident.summary }}</div>
      <p v-if="incident.suggested?.detail" class="sub sugg-detail">{{ incident.suggested.detail }}</p>
      <div class="sugg-acts">
        <button type="button" class="btn primary sm" :disabled="busy || live.stale.value" @click="answer('confirm')">
          {{ incident.suggested?.label ?? 'Merge' }}
        </button>
        <button type="button" class="btn ghost sm" :disabled="busy || live.stale.value" @click="answer('reject')">
          {{ incident.alternatives?.[0]?.label ?? 'Keep separate' }}
        </button>
      </div>
    </div>
    <span class="when"><span class="ib-kind">Suggestion</span></span>
  </div>
</template>

<style>
.sugg-detail {
  margin: var(--space-2) 0 0;
}
.sugg-acts {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  margin-top: var(--space-3);
}
</style>
