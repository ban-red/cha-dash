<script setup lang="ts">
/**
 * Add a widget, in edit mode (its own chunk, loaded on the first "Add").
 * Lists the catalog (model/widgets.ts) generically, ready ones first: what
 * the Home Graph can feed (model/feeds.ts), in sizes that fit the section.
 * A widget bound to one resource asks which (the Sonarr for a queue); the
 * generic service tile asks for a name, a link and what gives it a status.
 *
 * It only describes the widget; EditableBoard places it (model/edit.ts) in
 * the first gap that fits, records it in the history and saves it.
 */
import { computed, ref, useId, useTemplateRef, watch } from 'vue';
import type { Board, SizeId } from '@chadash/layout';
import { WIDGETS, type WidgetSpec } from '../model/widgets';
import { bindKind, candidateLabel, decodeBinding, feedFor, serviceBindingGroups, type BindKind } from '../model/feeds';
import { newItemId, sizeName, sizesFor, type AddRequest } from '../model/edit';
import type { Resource } from '../model/types';
import { useLive } from '../stores/live';
import { hrefOk, type TileConfig, type TilesDoc } from '../stores/tiles';
import { useModal } from './useModal';

const { board, tiles, sectionId: initialSection } = defineProps<{ board: Board; tiles: TilesDoc; sectionId?: string | undefined }>();
const emit = defineEmits<{ add: [req: AddRequest]; close: [] }>();

const live = useLive();
const uid = useId();
const dlg = useTemplateRef<HTMLDialogElement>('dlg');
useModal(dlg);

const sectionId = ref(initialSection && board.sections.some((s) => s.id === initialSection) ? initialSection : (board.sections[0]?.id ?? ''));
const section = computed(() => board.sections.find((s) => s.id === sectionId.value));
const sectionTitle = (s: { id: string; title?: string | undefined } | undefined) => s?.title ?? s?.id ?? '';

interface Entry {
  spec: WidgetSpec;
  kind: BindKind;
  /** Resources it can still show here (bound widgets). */
  free: Resource[];
  fits: SizeId[];
  ready: boolean;
  status: string;
}

const sizesWords = (sizes: SizeId[]) => sizes.map((s) => sizeName(s)).join(' · ');

const entries = computed<Entry[]>(() => {
  const g = live.graph.value;
  const state = { board, tiles };
  const span = section.value?.span ?? 12;
  return Object.values(WIDGETS).map((spec): Entry => {
    const kind = bindKind(spec);
    const feed = feedFor(spec, g, live.now.value);
    const fits = sizesFor(spec, span);
    const free = kind === 'resource' ? feed.candidates.filter((r) => newItemId(state, spec, r.id)) : [];
    let status: string;
    let ready = false;
    if (!feed.available) status = 'Nothing on your Home Graph feeds it yet';
    else if (kind === 'none' && !newItemId(state, spec)) status = 'Already on this board';
    else if (kind === 'resource' && !free.length) status = 'Every one is on this board already';
    else if (!fits.length) status = `Too wide for ${sectionTitle(section.value)}`;
    else {
      ready = true;
      status =
        kind === 'resource'
          ? `${free.length === 1 ? candidateLabel(g, free[0]!) : `${free.length} to choose from`} · ${sizesWords(fits)}`
          : kind === 'service'
            ? `A name, a link and a status · ${sizesWords(fits)}`
            : sizesWords(fits);
    }
    return { spec, kind, free, fits, ready, status };
  });
});
const ready = computed(() => entries.value.filter((e) => e.ready));
const waiting = computed(() => entries.value.filter((e) => !e.ready));

const chosen = ref('');
const entry = computed(() => ready.value.find((e) => e.spec.type === chosen.value));
const size = ref<SizeId | ''>('');
const target = ref('');
const name = ref('');
const href = ref('');
const binding = ref('');
const groups = computed(() => serviceBindingGroups(live.graph.value));

watch(
  () => [entry.value?.spec.type, entry.value?.fits.join()] as const,
  () => {
    const e = entry.value;
    if (!e) return;
    if (!size.value || !e.fits.includes(size.value)) size.value = e.fits.includes(e.spec.size) ? e.spec.size : (e.fits[0] ?? '');
    if (e.kind === 'resource' && !e.free.some((r) => r.id === target.value)) target.value = e.free[0]?.id ?? '';
  },
);

function choose(type: string): void {
  if (chosen.value === type) return;
  chosen.value = type;
  size.value = '';
  target.value = '';
  name.value = '';
  href.value = '';
  binding.value = '';
}

const hrefBad = computed(() => href.value.trim() !== '' && !hrefOk(href.value.trim()));
const canAdd = computed(() => {
  const e = entry.value;
  if (!e || !size.value || !sectionId.value) return false;
  if (e.kind === 'resource' && !target.value) return false;
  if (e.kind === 'service' && (!name.value.trim() || hrefBad.value)) return false;
  return true;
});

function submit(): void {
  const e = entry.value;
  if (!e || !canAdd.value || !size.value) return;
  const req: AddRequest = { spec: e.spec, sectionId: sectionId.value, size: size.value };
  if (e.kind === 'resource') req.target = target.value;
  if (e.kind === 'service') {
    const config: TileConfig = { name: name.value.trim() };
    if (href.value.trim()) config.href = href.value.trim();
    const bind = decodeBinding(binding.value);
    if (bind) config.bind = bind;
    req.config = config;
  }
  emit('add', req);
}
</script>

<template>
  <dialog ref="dlg" class="edit-dlg picker" :aria-labelledby="`${uid}-h`" @cancel.prevent="emit('close')">
    <form class="edit-dlg-body" novalidate @submit.prevent="submit">
      <header class="edit-dlg-head">
        <h2 :id="`${uid}-h`">Add a widget</h2>
        <label class="pk-to">
          <span>to</span>
          <select v-model="sectionId" aria-label="Section">
            <option v-for="s in board.sections" :key="s.id" :value="s.id">{{ sectionTitle(s) }}</option>
          </select>
        </label>
        <button type="button" class="x" aria-label="Close" @click="emit('close')">
          <svg viewBox="0 0 10 10" aria-hidden="true"><path d="m2.5 2.5 5 5m0-5-5 5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
        </button>
      </header>

      <div class="pk-cols">
        <div class="pk-list" role="group" aria-label="Widgets">
          <button
            v-for="e in ready"
            :key="e.spec.type"
            type="button"
            class="pk-item"
            :aria-pressed="chosen === e.spec.type"
            :data-type="e.spec.type"
            @click="choose(e.spec.type)"
          >
            <span class="pk-name">{{ e.spec.name }}</span>
            <span class="pk-sub">{{ e.status }}</span>
          </button>
          <p v-if="!ready.length" class="pk-hint">Nothing more fits {{ sectionTitle(section) }} from what the Home Graph has right now.</p>
          <details v-if="waiting.length" class="pk-waiting">
            <summary>Not available here · {{ waiting.length }}</summary>
            <div v-for="e in waiting" :key="e.spec.type" class="pk-item off" :data-type="e.spec.type">
              <span class="pk-name">{{ e.spec.name }}</span>
              <span class="pk-sub">{{ e.status }}</span>
            </div>
          </details>
        </div>

        <div v-if="entry" class="pk-detail">
          <fieldset class="pk-sizes">
            <legend>Size</legend>
            <label v-for="s in entry.fits" :key="s" class="pk-size">
              <input v-model="size" type="radio" :name="`${uid}-size`" :value="s" />
              <span>{{ sizeName(s) }}</span>
            </label>
          </fieldset>

          <div v-if="entry.kind === 'resource'" class="field">
            <label :for="`${uid}-target`">Shows</label>
            <select :id="`${uid}-target`" v-model="target">
              <option v-for="r in entry.free" :key="r.id" :value="r.id">{{ candidateLabel(live.graph.value, r) }}</option>
            </select>
          </div>

          <template v-if="entry.kind === 'service'">
            <div class="field">
              <label :for="`${uid}-name`">Name</label>
              <input :id="`${uid}-name`" v-model="name" type="text" autocomplete="off" maxlength="200" required />
            </div>
            <div class="field">
              <label :for="`${uid}-href`">Link</label>
              <input :id="`${uid}-href`" v-model="href" type="url" inputmode="url" autocomplete="off" placeholder="https://" :aria-invalid="hrefBad || undefined" />
              <span v-if="hrefBad" class="help bad">Links start with http:// or https://.</span>
            </div>
            <div class="field">
              <label :for="`${uid}-bind`">Status from</label>
              <select :id="`${uid}-bind`" v-model="binding">
                <option value="">Nothing (no status)</option>
                <optgroup v-for="gr in groups" :key="gr.label" :label="gr.label">
                  <option v-for="c in gr.choices" :key="c.value" :value="c.value">{{ c.label }}</option>
                </optgroup>
              </select>
            </div>
          </template>

          <div class="edit-dlg-foot">
            <button type="button" class="btn ghost" @click="emit('close')">Cancel</button>
            <button type="submit" class="btn primary" :disabled="!canAdd">Add to {{ sectionTitle(section) }}</button>
          </div>
        </div>
        <p v-else class="pk-hint pk-detail">Pick a widget. It goes into the first gap in {{ sectionTitle(section) }} where it fits; drag it anywhere after.</p>
      </div>
    </form>
  </dialog>
</template>

<style>
.pk-to {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--type-small);
  color: var(--text-3);
}
.pk-cols {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: var(--space-4);
  min-height: 0;
}
@media (max-width: 640px) {
  .pk-cols {
    grid-template-columns: minmax(0, 1fr);
  }
}
.pk-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: min(60vh, 520px);
  overflow: auto;
  overscroll-behavior: contain;
}
.pk-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: var(--space-2) var(--space-3);
  border: var(--hairline) solid transparent;
  border-radius: var(--radius-md);
  background: none;
  text-align: left;
}
button.pk-item:hover {
  background: color-mix(in oklch, var(--text) 5%, transparent);
}
.pk-item[aria-pressed='true'] {
  border-color: var(--line-2);
  background: var(--surface);
}
.pk-item.off {
  opacity: 0.6;
}
.pk-name {
  font: var(--weight-medium) var(--type-body) / 1.3 var(--font-ui);
  color: var(--text);
}
.pk-sub {
  font-size: var(--type-caption);
  line-height: 1.35;
  color: var(--text-3);
}
.pk-waiting {
  margin-top: var(--space-2);
}
.pk-waiting summary {
  padding: var(--space-2) var(--space-3);
  font-size: var(--type-small);
  color: var(--text-3);
  cursor: pointer;
}
.pk-detail {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}
.pk-hint {
  margin: 0;
  font-size: var(--type-small);
  color: var(--text-3);
}
.pk-sizes {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  margin: 0;
  padding: 0;
  border: 0;
}
.pk-sizes legend {
  margin-bottom: var(--space-2);
  font: var(--weight-medium) var(--type-small) / 1.2 var(--font-ui);
  color: var(--text-2);
}
.pk-size {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 6px 10px;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-md);
  font: var(--weight-regular) var(--type-small) / 1.2 var(--font-num);
}
.pk-size:has(input:checked) {
  border-color: var(--focus);
}
</style>
