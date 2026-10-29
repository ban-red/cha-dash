<script setup lang="ts">
/**
 * Edit one tile, in edit mode (its own chunk, loaded on the first Edit): its
 * name, its link, and what it's bound to. A widget bound to one resource can
 * be pointed at another (the queue at the other Sonarr); a service tile takes
 * its status from a check, an integration or any resource. The media
 * calendar also has "Include unmonitored releases" (`options.unmonitored`;
 * saving it replaces the importer's per-source option). Everything else in
 * its config (icon, other options, where it came from) is kept as it is.
 *
 * It only describes the change; EditableBoard records it in the history, so
 * Undo takes it back, and saves it.
 */
import { computed, ref, useId, useTemplateRef } from 'vue';
import type { Item } from '@chadash/layout';
import { WIDGETS, parseItemId } from '../model/widgets';
import { bindKind, candidateLabel, decodeBinding, encodeBinding, feedFor, serviceBindingGroups } from '../model/feeds';
import { label } from '../model/graph';
import { useLive } from '../stores/live';
import { instanceName } from '../stores/integrations';
import { hrefOk, type TileConfig } from '../stores/tiles';
import { useModal } from './useModal';
import './editDialog.css';

const { item, config } = defineProps<{ item: Pick<Item, 'id' | 'type'>; config?: TileConfig | undefined }>();
const emit = defineEmits<{ save: [config: TileConfig | undefined]; close: [] }>();

const live = useLive();
const uid = useId();
const dlg = useTemplateRef<HTMLDialogElement>('dlg');
useModal(dlg);

const spec = computed(() => WIDGETS[item.type]);
const kind = computed(() => (spec.value ? bindKind(spec.value) : 'none'));
const idTarget = parseItemId(item.id).target;

const name = ref(config?.name ?? '');
const href = ref(config?.href ?? '');
const description = ref(config?.description ?? '');
const target = ref(config?.bind?.resource ?? idTarget ?? '');
const binding = ref(encodeBinding(config?.bind));
const isCalendar = item.type === 'calendar';
const unmonitored = ref(
  config?.options?.['unmonitored'] === true ||
    (config?.bind?.sources ?? []).some((s) => s.capability === 'media.calendar' && s.options?.['unmonitored'] === true),
);

const candidates = computed(() => {
  if (kind.value !== 'resource' || !spec.value) return [];
  const list = feedFor(spec.value, live.graph.value, live.now.value).candidates;
  // Keep the current one listed even if the graph doesn't have it right now.
  if (target.value && !list.some((r) => r.id === target.value)) return [{ id: target.value, kind: '', name: `${target.value} (not in the graph)` }, ...list];
  return list;
});
const groups = computed(() => serviceBindingGroups(live.graph.value));
const placeholder = computed(() => {
  const r = target.value ? live.graph.value?.resources[target.value] : undefined;
  return r ? label(r) : (spec.value?.name ?? item.type);
});

const boundName = computed(() => instanceName(config?.bind?.instance));
const hrefBad = computed(() => href.value.trim() !== '' && !hrefOk(href.value.trim()));

function submit(): void {
  if (hrefBad.value) return;
  const next: TileConfig = { ...config };
  const set = (k: 'name' | 'href' | 'description', v: string) => {
    if (v.trim()) next[k] = v.trim();
    else delete next[k];
  };
  set('name', name.value);
  set('href', href.value);
  if (kind.value === 'service') set('description', description.value);
  const bind = { ...config?.bind };
  if (kind.value === 'resource') {
    if (target.value && target.value !== idTarget) bind.resource = target.value;
    else delete bind.resource;
  } else if (kind.value === 'service') {
    delete bind.check;
    delete bind.instance;
    delete bind.resource;
    Object.assign(bind, decodeBinding(binding.value));
  }
  if (isCalendar) {
    const options = { ...config?.options };
    if (unmonitored.value) options['unmonitored'] = true;
    else delete options['unmonitored'];
    if (Object.keys(options).length) next.options = options;
    else delete next.options;
    // One answer for the tile: the sources' own option goes.
    if (bind.sources?.some((s) => s.options && 'unmonitored' in s.options)) {
      bind.sources = bind.sources.map((s) => {
        if (!s.options || !('unmonitored' in s.options)) return s;
        const { unmonitored: _drop, ...rest } = s.options;
        const out = { ...s };
        if (Object.keys(rest).length) out.options = rest;
        else delete out.options;
        return out;
      });
    }
  }
  if (Object.keys(bind).length) next.bind = bind;
  else delete next.bind;
  emit('save', Object.keys(next).length ? next : undefined);
}
</script>

<template>
  <dialog ref="dlg" class="edit-dlg editor" :aria-labelledby="`${uid}-h`" @cancel.prevent="emit('close')">
    <form class="edit-dlg-body" novalidate @submit.prevent="submit">
      <header class="edit-dlg-head">
        <h2 :id="`${uid}-h`">Edit {{ config?.name || placeholder }}</h2>
        <span class="edit-dlg-kind">{{ spec?.name ?? item.type }}</span>
        <button type="button" class="x" aria-label="Close" @click="emit('close')">
          <svg viewBox="0 0 10 10" aria-hidden="true"><path d="m2.5 2.5 5 5m0-5-5 5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
        </button>
      </header>

      <div class="field">
        <label :for="`${uid}-name`">Name</label>
        <input :id="`${uid}-name`" v-model="name" type="text" autocomplete="off" maxlength="200" :placeholder="boundName ?? placeholder" :disabled="boundName !== undefined" />
        <span v-if="boundName !== undefined" class="help">Named after its integration. Rename that in Settings → Integrations.</span>
        <span v-else class="help">Empty uses the widget’s own name.</span>
      </div>
      <div class="field">
        <label :for="`${uid}-href`">Link</label>
        <input :id="`${uid}-href`" v-model="href" type="url" inputmode="url" autocomplete="off" placeholder="https://" :aria-invalid="hrefBad || undefined" />
        <span v-if="hrefBad" class="help bad">Links start with http:// or https://.</span>
      </div>
      <div v-if="kind === 'service'" class="field">
        <label :for="`${uid}-desc`">Description</label>
        <input :id="`${uid}-desc`" v-model="description" type="text" autocomplete="off" maxlength="500" />
      </div>

      <div v-if="kind === 'resource'" class="field">
        <label :for="`${uid}-target`">Shows</label>
        <select :id="`${uid}-target`" v-model="target">
          <option v-for="r in candidates" :key="r.id" :value="r.id">{{ r.kind ? candidateLabel(live.graph.value, r) : r.name }}</option>
        </select>
      </div>
      <div v-else-if="kind === 'service'" class="field">
        <label :for="`${uid}-bind`">Status from</label>
        <select :id="`${uid}-bind`" v-model="binding">
          <option value="">Nothing (no status)</option>
          <option v-if="binding && !groups.some((g) => g.choices.some((c) => c.value === binding))" :value="binding">{{ binding.replace(/^[a-z]+:/, '') }} (not in the graph)</option>
          <optgroup v-for="gr in groups" :key="gr.label" :label="gr.label">
            <option v-for="c in gr.choices" :key="c.value" :value="c.value">{{ c.label }}</option>
          </optgroup>
        </select>
      </div>

      <div v-if="isCalendar" class="field check">
        <label :for="`${uid}-unmon`">
          <input :id="`${uid}-unmon`" v-model="unmonitored" type="checkbox" :aria-describedby="`${uid}-unmon-help`" />
          Include unmonitored releases
        </label>
        <span :id="`${uid}-unmon-help`" class="help">Lists what Sonarr, Radarr and Lidarr know about but aren’t monitoring, marked as such.</span>
      </div>

      <div class="edit-dlg-foot">
        <button type="button" class="btn ghost" @click="emit('close')">Cancel</button>
        <button type="submit" class="btn primary" :disabled="hrefBad">Save</button>
      </div>
    </form>
  </dialog>
</template>
