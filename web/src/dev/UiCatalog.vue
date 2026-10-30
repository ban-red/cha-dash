<script setup lang="ts">
/**
 * Every UI primitive in every variant and state, on one page (ADR-0032):
 * /dev/ui on the Vite dev server, no sign-in. Check a change to ui/ here in
 * each theme before it ships. Production bundles carry none of it.
 */
import { ref } from 'vue';
import { THEMES, applyTheme, currentTheme, isThemeId } from '../lib/theme';
import UiButton, { type ButtonSize, type ButtonVariant } from '../ui/Button.vue';
import UiCloseButton from '../ui/CloseButton.vue';
import UiDialog from '../ui/Dialog.vue';
import UiField from '../ui/Field.vue';
import Glyph, { type GlyphKind } from '../ui/marks/Glyph.vue';
import Meter from '../ui/marks/Meter.vue';
import MiniGraph from '../ui/marks/MiniGraph.vue';

const VARIANTS: ButtonVariant[] = ['default', 'primary', 'ghost', 'danger', 'danger-solid'];
const SIZES: ButtonSize[] = ['md', 'sm'];
const GLYPHS: GlyphKind[] = ['attn', 'down', 'ok', 'off', 'prog', 'maint', 'repair', 'link', 'lock', 'check', 'cross'];
const SERIES = [12, 18, 15, 22, 30, 27, 41, 38, 35, 52, 48, 44, 61, 58, 50];

const name = ref('');
const port = ref('80x');
const kind = ref('docker');
const notes = ref('');
const agree = ref(true);
const dialog = ref<'' | 'plain' | 'form'>('');

function pick(id: string): void {
  if (isThemeId(id)) applyTheme(id);
}
</script>

<template>
  <main class="cat">
    <header class="cat-head">
      <h1>UI primitives</h1>
      <p class="muted">web/src/ui, as features use them. Dev server only.</p>
      <UiField class="cat-theme" label="Theme" v-slot="{ control }">
        <select v-bind="control" :value="currentTheme" @change="pick(($event.target as HTMLSelectElement).value)">
          <option v-for="t in THEMES" :key="t.id" :value="t.id">{{ t.name }}</option>
        </select>
      </UiField>
    </header>

    <section aria-labelledby="cat-btn">
      <h2 id="cat-btn" class="caps">UiButton</h2>
      <div v-for="s in SIZES" :key="s" class="cat-row">
        <UiButton v-for="v in VARIANTS" :key="v" :variant="v" :size="s">{{ v }}</UiButton>
        <UiButton :size="s" disabled>disabled</UiButton>
        <UiButton :size="s" variant="primary" aria-busy="true">Working…</UiButton>
        <UiButton :size="s" variant="danger-solid">Delete<span class="tier">T3</span></UiButton>
        <UiButton :size="s" href="#cat-btn">a link</UiButton>
      </div>
    </section>

    <section aria-labelledby="cat-x">
      <h2 id="cat-x" class="caps">UiCloseButton</h2>
      <div class="cat-row"><UiCloseButton label="Close the example" /></div>
    </section>

    <section aria-labelledby="cat-field">
      <h2 id="cat-field" class="caps">UiField</h2>
      <div class="cat-grid">
        <UiField label="Name" help="What you call it on the board." v-slot="{ control }">
          <input v-bind="control" v-model="name" type="text" autocomplete="off" />
        </UiField>
        <UiField label="Port" :error="!/^\d+$/.test(port) && 'A port is a number, such as 8080.'" v-slot="{ control }">
          <input v-bind="control" v-model="port" type="text" class="mono-input" autocomplete="off" />
        </UiField>
        <UiField label="Kind" optional v-slot="{ control }">
          <select v-bind="control" v-model="kind">
            <option value="docker">Docker</option>
            <option value="proxmox">Proxmox</option>
          </select>
        </UiField>
        <UiField label="Token" help="Write-only.">
          <template #aside><span class="chip"><Glyph kind="lock" />set</span></template>
          <template #default="{ control }">
            <input v-bind="control" type="password" autocomplete="off" />
          </template>
        </UiField>
        <UiField label="Password" help="At least 6 characters." :invalid="true" v-slot="{ control }">
          <input v-bind="control" type="password" autocomplete="off" />
        </UiField>
        <UiField label="Search" hide-label v-slot="{ control }">
          <input v-bind="control" type="search" placeholder="A filter with a hidden label" autocomplete="off" />
        </UiField>
        <UiField label="Notes" v-slot="{ control }">
          <textarea v-bind="control" v-model="notes" rows="3"></textarea>
        </UiField>
        <UiField check label="Include unmonitored releases" help="A checkbox: the control sits inside its label." v-slot="{ control }">
          <input v-bind="control" v-model="agree" type="checkbox" />
        </UiField>
      </div>
    </section>

    <section aria-labelledby="cat-dlg">
      <h2 id="cat-dlg" class="caps">UiDialog</h2>
      <div class="cat-row">
        <UiButton @click="dialog = 'plain'">Open a dialog</UiButton>
        <UiButton @click="dialog = 'form'">Open a form dialog</UiButton>
      </div>
      <UiDialog v-if="dialog === 'plain'" title="Dismissed services" @close="dialog = ''">
        <p class="muted">A dialog's body is whatever the feature puts in it.</p>
        <template #foot><UiButton variant="primary" @click="dialog = ''">Done</UiButton></template>
      </UiDialog>
      <UiDialog v-else-if="dialog === 'form'" as="form" size="sm" title="Delete snapshot" close-label="Cancel" @close="dialog = ''" @submit="dialog = ''">
        <template #lead><Glyph kind="lock" /></template>
        <template #head><span class="chip">T3</span></template>
        <UiField label="Type sandbox to confirm" v-slot="{ control }">
          <input v-bind="control" type="text" autocomplete="off" />
        </UiField>
        <template #foot>
          <UiButton variant="ghost" @click="dialog = ''">Cancel</UiButton>
          <UiButton variant="danger-solid" type="submit">Delete<span class="tier">T3</span></UiButton>
        </template>
      </UiDialog>
    </section>

    <section aria-labelledby="cat-marks">
      <h2 id="cat-marks" class="caps">Marks</h2>
      <div class="cat-row">
        <span v-for="g in GLYPHS" :key="g" class="cat-glyph"><Glyph :kind="g" /> {{ g }}</span>
      </div>
      <div class="cat-meters">
        <Meter :value="32" label="Disk, healthy" />
        <Meter :value="81" :warn="75" :crit="90" label="Disk, warning" />
        <Meter :value="96" :warn="75" :crit="90" label="Disk, critical" />
        <Meter :value="null" label="Disk, unknown" />
      </div>
      <MiniGraph :values="SERIES" label="Requests a minute" />
    </section>
  </main>
</template>

<style>
.cat {
  display: grid;
  gap: var(--space-6);
  max-width: 960px;
  margin: 0 auto;
  padding: var(--space-5) 16px;
}
.cat section {
  display: grid;
  gap: var(--space-3);
}
.cat-head {
  display: flex;
  flex-wrap: wrap;
  align-items: end;
  gap: var(--space-2) var(--space-5);
}
.cat-head h1 {
  font: var(--weight-semibold) var(--type-title) / 1.2 var(--font-ui);
}
.cat-theme {
  margin-left: auto;
  min-width: 12rem;
}
.cat-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
}
.cat-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
  gap: var(--space-4);
}
.cat-glyph {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: var(--type-small);
  color: var(--text-2);
}
.cat-meters {
  display: grid;
  gap: var(--space-2);
  max-width: 320px;
}
</style>
