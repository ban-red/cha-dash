<script setup lang="ts">
/**
 * A stack's logs on its page (plan 16 · 4): every service interleaved, each
 * line tagged with its service, from `GET /api/stacks/{node}/{stack}/logs`.
 * Nothing streams until "Follow the logs"; Stop, leaving the page or another
 * stack ends the stream. Operator and up (`can.readData`): StackEditor.vue
 * shows it only then, for a managed stack that exists.
 */
import { computed, defineAsyncComponent, ref, useId, watch } from 'vue';
import UiButton from '../ui/Button.vue';
import { stackLogsUrl } from '../lib/logstream';

const LogView = defineAsyncComponent(() => import('../components/LogView.vue'));

const { node, stack } = defineProps<{ node: string; stack: string }>();

const uid = useId();
const following = ref(false);
const url = computed(() => stackLogsUrl(node, stack));
watch(url, () => (following.value = false));
</script>

<template>
  <section class="sk-block sk-logs" :aria-labelledby="`${uid}-h`">
    <h4 :id="`${uid}-h`" class="sk-sub">Logs</h4>
    <p class="st-meta">All its services together, newest at the bottom, each line with its service. Following stops after 30 minutes.</p>
    <div class="st-acts">
      <UiButton v-if="!following" size="sm" @click="following = true">Follow the logs</UiButton>
      <UiButton v-else size="sm" variant="ghost" @click="following = false">Stop following</UiButton>
    </div>
    <LogView v-if="following" :key="url" :url="url" :name="stack" />
  </section>
</template>
