<script setup lang="ts">
/**
 * Settings → Security → Security level: how much security findings (a socket
 * proxy that's too broad, secrets in container labels, credentials over plain
 * http) interrupt you. The hub keeps it (`GET`/`PUT /api/settings/security`)
 * and re-publishes every open finding when it changes, so the Inbox, the
 * Briefing and Needs attention follow at once over the realtime feed.
 *
 * A native radio group: arrows move between the levels, and choosing one
 * saves it. A failed save puts the choice back and says why.
 */
import { onMounted, ref, useId } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import { api } from '../lib/api';
import { errorWords } from '../stores/integrations';
import { useUi } from '../stores/ui';

export interface SecurityLevelInfo {
  id: string;
  label: string;
  description: string;
  default?: boolean;
}
export interface SecurityView {
  posture: string;
  levels: SecurityLevelInfo[];
}

const ui = useUi();
const name = useId();
const view = ref<SecurityView | null>(null);
const status = ref<'loading' | 'ready' | 'error'>('loading');
const loadError = ref('');
const saving = ref(false);
const saveError = ref('');

async function load(): Promise<void> {
  try {
    view.value = await api<SecurityView>('/api/settings/security');
    status.value = 'ready';
  } catch (err) {
    status.value = 'error';
    loadError.value = errorWords(err, 'The security level couldn’t be loaded.');
  }
}

async function choose(level: SecurityLevelInfo): Promise<void> {
  const v = view.value;
  if (!v || v.posture === level.id) return;
  const before = v.posture;
  v.posture = level.id;
  saving.value = true;
  saveError.value = '';
  try {
    view.value = await api<SecurityView>('/api/settings/security', { method: 'PUT', body: { posture: level.id } });
    ui.toast(`Security level: ${level.label.split(' · ')[0]}.`);
  } catch (err) {
    v.posture = before;
    saveError.value = errorWords(err, 'The security level wasn’t changed.');
  } finally {
    saving.value = false;
  }
}

onMounted(load);
</script>

<template>
  <section class="st-section" aria-labelledby="sl-h">
    <header><h3 id="sl-h">Security level</h3></header>
    <p class="st-lede">
      How much security findings interrupt you. At any level they’re listed in the Inbox; above Relaxed, the serious ones also need you in the Briefing and Needs
      attention.
    </p>
    <div v-if="status === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span></div>
    <div v-else-if="status === 'error'" class="st-note bad" role="alert"><Glyph kind="down" /><span>{{ loadError }}</span></div>
    <fieldset v-else-if="view" class="sl-group" :aria-busy="saving">
      <legend class="sr">Security level</legend>
      <label v-for="l in view.levels" :key="l.id" :class="['sl-card', { on: view.posture === l.id }]">
        <input type="radio" :name="name" :value="l.id" :checked="view.posture === l.id" :aria-describedby="`${name}-${l.id}`" @change="choose(l)" />
        <span class="sl-text">
          <span class="sl-label">{{ l.label }}<span v-if="l.default" class="chip">default</span></span>
          <span :id="`${name}-${l.id}`" class="sl-desc">{{ l.description }}</span>
        </span>
      </label>
    </fieldset>
    <p v-if="saveError" class="err" role="alert">{{ saveError }}</p>
  </section>
</template>

<style>
.sl-group {
  display: grid;
  gap: var(--space-2);
  margin: 0;
  padding: 0;
  border: 0;
  min-width: 0;
}
.sl-card {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  align-items: start;
  gap: var(--space-3);
  padding: var(--space-3) var(--space-4);
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-lg);
  background: var(--surface);
  color: var(--text);
  cursor: pointer;
  transition: background-color var(--motion-press-duration) var(--motion-press-easing);
}
.sl-card:hover {
  background: var(--surface-hover);
}
.sl-card.on {
  border-color: var(--text-3);
}
.sl-card:has(input:focus-visible) {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
}
.sl-card input {
  width: 16px;
  height: 16px;
  margin: 2px 0 0;
  accent-color: var(--text);
}
.sl-text {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.sl-label {
  font: var(--weight-medium) var(--type-callout) / 1.3 var(--font-ui);
}
.sl-label .chip {
  margin-left: 6px;
  vertical-align: 1px;
}
.sl-desc {
  font-size: var(--type-small);
  line-height: var(--leading-relaxed);
  color: var(--text-2);
}
</style>
