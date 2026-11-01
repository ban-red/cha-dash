<script setup lang="ts">
/**
 * One container's release notes (Settings → Updates, with release notes on):
 * the GitHub releases newer than the version it runs, read by the hub.
 *
 *   GET /api/updates/{resource}/notes → ReleaseNotes
 *
 * The text is someone else's: shown as plain text, with links only into the
 * same repository (the hub removed the rest; noteParts checks again).
 */
import { onMounted, ref } from 'vue';
import { api } from '../lib/api';
import { hubWords } from '../lib/actions';
import { shortDate } from '../lib/format';
import Glyph from '../ui/marks/Glyph.vue';
import UiButton from '../ui/Button.vue';
import { noteParts, type ReleaseNotes } from './updates';

const { resource, name } = defineProps<{ resource: string; name: string }>();

const notes = ref<ReleaseNotes>();
const status = ref<'loading' | 'ready' | 'error'>('loading');
const error = ref('');

async function load(): Promise<void> {
  status.value = 'loading';
  try {
    notes.value = await api<ReleaseNotes>(`/api/updates/${encodeURIComponent(resource)}/notes`);
    status.value = 'ready';
  } catch (err) {
    error.value = hubWords(err, 'Couldn’t read the release notes.');
    status.value = 'error';
  }
}
onMounted(() => void load());

const repoName = (repo: string) => repo.replace(/^https:\/\/github\.com\//, '');
</script>

<template>
  <div class="up-notes" :aria-label="`Release notes for ${name}`" role="region">
    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert">
      <Glyph kind="down" /><span>{{ error }}</span>
      <UiButton size="sm" @click="load">Try again</UiButton>
    </div>
    <template v-else-if="notes">
      <p class="m">
        From <a :href="notes.repo" target="_blank" rel="noopener noreferrer">{{ repoName(notes.repo) }}</a> on GitHub.
        <template v-if="notes.newer && notes.from">Releases newer than {{ notes.from }}, which it runs.</template>
        <template v-else>It doesn’t say which version it runs, so these are the newest.</template>
        The text is the project’s own.
      </p>
      <p v-if="!notes.releases.length" class="m">No newer releases on GitHub.</p>
      <article v-for="r in notes.releases" :key="r.tag" class="up-release">
        <h4>
          <a v-if="r.url" :href="r.url" target="_blank" rel="noopener noreferrer">{{ r.name || r.tag }}</a>
          <template v-else>{{ r.name || r.tag }}</template>
          <span class="m"><template v-if="r.name && r.name !== r.tag"> · {{ r.tag }}</template><template v-if="r.published_at"> · {{ shortDate(r.published_at) }}</template></span>
        </h4>
        <p class="up-note-text">
          <template v-for="(p, i) in noteParts(r.notes, notes.repo)" :key="i">
            <a v-if="p.href" :href="p.href" target="_blank" rel="noopener noreferrer">{{ p.text }}</a>
            <template v-else>{{ p.text }}</template>
          </template>
        </p>
        <p v-if="r.truncated" class="m">Cut short. The rest is on GitHub.</p>
      </article>
    </template>
  </div>
</template>

<style>
.up-notes {
  display: grid;
  gap: 8px;
  padding: 8px 0 4px;
}
.up-notes .m {
  color: var(--text-2);
  margin: 0;
}
.up-release h4 {
  margin: 0 0 4px;
  font-size: inherit;
}
.up-note-text {
  margin: 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
