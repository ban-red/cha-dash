<script setup lang="ts">
/**
 * Settings → Timeline: what happened in the home, and when (GET
 * /api/timeline). Incidents and their causes, the actions that ran, health
 * changes and maintenance windows on one time axis, newest first, for the
 * whole home or one resource (`?resource=<id>`, as a sheet's "Open the
 * timeline" links). Every role has it; household sees what its boards show
 * and its own actions, which the hub decides.
 *
 * Its own chunk, loaded with the pane.
 */
import { computed } from 'vue';
import { label } from '../model/graph';
import { navigate, route, settingsPath } from '../lib/router';
import { useLive } from '../stores/live';
import TimelineList from '../components/TimelineList.vue';
import UiButton from '../ui/Button.vue';
import SettingsHelp from './SettingsHelp.vue';

const live = useLive();
const resource = computed(() => route.value.query['resource'] || undefined);
const nameOf = (id: string) => label(live.graph.value?.resources[id]) || id;
const title = computed(() => (resource.value ? nameOf(resource.value) : 'the whole home'));

function wholeHome(): void {
  navigate(settingsPath('timeline'), { replace: true });
}
function openResource(id: string): void {
  navigate(`/?r=${encodeURIComponent(id)}`);
}
</script>

<template>
  <section class="st-section" aria-labelledby="tl-h">
    <header>
      <h3 id="tl-h">{{ resource ? `Timeline · ${title}` : 'Timeline' }}</h3>
      <UiButton v-if="resource" size="sm" variant="ghost" @click="wholeHome">Show the whole home</UiButton>
    </header>
    <SettingsHelp summary="Incidents, the actions that ran, health changes and maintenance windows, on one time axis.">
      Cha Dash records every incident as it opens, changes, gains a cause and resolves, and keeps them for 90 days unless Settings → History says otherwise.
      Actions come from the audit log and health changes from the metrics history, so with history off there are none. Copy as Markdown puts the range on
      the clipboard, oldest first, ready for a postmortem.
    </SettingsHelp>
    <TimelineList :resource="resource" :title="title" :spans="['24h', '7d', '30d', '90d']" :name-of="resource ? undefined : nameOf" @open="openResource" />
  </section>
</template>
