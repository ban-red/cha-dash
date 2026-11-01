<script setup lang="ts">
/**
 * Settings → Integrations → Dozzle (`GET`/`PUT /api/settings/dozzle`): the
 * address of a Dozzle the household runs, so a container's sheet links to
 * its live logs there (lib/dozzle.ts). Cha Dash never talks to Dozzle; the
 * browser follows the link, and Dozzle asks for its own sign-in if it has
 * one. Admins and the owner; an empty address turns the links off.
 */
import { onMounted, ref } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import { api } from '../lib/api';
import { hubWords } from '../lib/actions';
import { loadDozzle } from '../lib/dozzle';
import { useUi } from '../stores/ui';
import UiButton from '../ui/Button.vue';
import UiField from '../ui/Field.vue';

const ui = useUi();
/** What the hub has. */
const saved = ref('');
const draft = ref('');
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');
const saving = ref(false);
const saveError = ref('');

async function load(): Promise<void> {
  try {
    const v = await api<{ url?: string }>('/api/settings/dozzle');
    saved.value = draft.value = typeof v?.url === 'string' ? v.url : '';
    status.value = 'ready';
  } catch (err) {
    status.value = 'error';
    loadError.value = hubWords(err, 'This setting couldn’t be loaded.');
  }
}

async function save(): Promise<void> {
  if (saving.value || draft.value.trim() === saved.value) return;
  saving.value = true;
  saveError.value = '';
  try {
    const v = await api<{ url?: string }>('/api/settings/dozzle', { method: 'PUT', body: { url: draft.value.trim() } });
    saved.value = draft.value = typeof v?.url === 'string' ? v.url : '';
    void loadDozzle(true);
    ui.toast(saved.value ? 'Containers now link to their logs in Dozzle.' : 'Dozzle links are off.');
  } catch (err) {
    saveError.value = hubWords(err, 'The address wasn’t saved.');
  } finally {
    saving.value = false;
  }
}

onMounted(load);
</script>

<template>
  <section class="st-section" aria-labelledby="dz-h">
    <header><h3 id="dz-h">Dozzle</h3></header>
    <p class="st-lede">
      If you run Dozzle, give its address and each container’s sheet links to its live logs there. Cha Dash doesn’t connect to Dozzle: your browser opens the
      link.
    </p>
    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert"><Glyph kind="down" /><span>{{ loadError }}</span></div>
    <form v-else @submit.prevent="save">
      <UiField v-slot="{ control }" label="Dozzle’s address" help="Such as https://logs.home.example. Leave it empty to turn the links off." :error="saveError" alert optional>
        <input v-bind="control" v-model="draft" type="url" inputmode="url" autocomplete="off" spellcheck="false" placeholder="https://logs.home.example" :disabled="saving" />
      </UiField>
      <div class="st-acts">
        <UiButton type="submit" size="sm" :aria-disabled="draft.trim() === saved || saving ? 'true' : undefined" :aria-busy="saving || undefined">Save</UiButton>
      </div>
    </form>
  </section>
</template>
