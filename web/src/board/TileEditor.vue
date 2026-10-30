<script setup lang="ts">
/**
 * Edit one tile, in edit mode (its own chunk, loaded on the first Edit): its
 * name, its link, and what it's bound to. A widget bound to one resource can
 * be pointed at another (the queue at the other Sonarr); a service tile takes
 * its status from a check, an integration or any resource. The media
 * calendar also has "Include unmonitored releases" (`options.unmonitored`;
 * saving it replaces the importer's per-source option). A widget that
 * declares settings of its own (WidgetSpec.options) gets them here, saved in
 * `options`; a value left at its default isn't stored. Everything else in its
 * config (icon, other options, where it came from) is kept as it is.
 *
 * It only describes the change; EditableBoard records it in the history, so
 * Undo takes it back, and saves it.
 */
import { computed, ref } from 'vue';
import type { Item } from '@chadash/layout';
import { WIDGETS, parseItemId } from '../model/widgets';
import { bindKind, candidateLabel, decodeBinding, encodeBinding, feedFor, serviceBindingGroups } from '../model/feeds';
import { byKind, label, outgoing } from '../model/graph';
import type { WidgetOption } from '../model/widgets';
import { optionValue, sizesValue, type OptionValue } from '../widgets/options';
import { useLive } from '../stores/live';
import { instanceName } from '../stores/integrations';
import { hrefOk, type TileConfig } from '../stores/tiles';
import './editDialog.css';
import UiButton from '../ui/Button.vue';
import UiDialog from '../ui/Dialog.vue';
import UiField from '../ui/Field.vue';

const { item, config } = defineProps<{ item: Pick<Item, 'id' | 'type'>; config?: TileConfig | undefined }>();
const emit = defineEmits<{ save: [config: TileConfig | undefined]; close: [] }>();

const live = useLive();

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

/** The widget's own settings, as edited. */
const widgetOptions = computed(() => spec.value?.options ?? []);
const values = ref<Record<string, OptionValue>>(Object.fromEntries((spec.value?.options ?? []).map((o) => [o.key, optionValue(o, config?.options?.[o.key])])));
/** A `resources` option's sizes by resource id, as edited, by the sizes' key. */
const sizes = ref<Record<string, Record<string, string>>>(
  Object.fromEntries((spec.value?.options ?? []).flatMap((o) => (o.kind === 'resources' && o.sizes ? [[o.sizes.key, sizesValue(o, config?.options?.[o.sizes.key])]] : []))),
);
/** What a `resources` option can pick: the target's related resources of that kind, else all of them; with `cap`, only those with it. */
function pickable(o: WidgetOption & { kind: 'resources' }) {
  const g = live.graph.value;
  const t = target.value ? g?.resources[target.value] : undefined;
  if (t?.kind === o.resourceKind) return [];
  const list = t && o.via ? outgoing(g, t.id, o.via).filter((r) => r.kind === o.resourceKind) : byKind(g, o.resourceKind);
  const out = list.filter((r) => !o.cap || r.caps?.includes(o.cap)).sort((a, b) => label(a).localeCompare(label(b)));
  if (!o.ordered) return out;
  // Ticked first, in their order; then the rest.
  const picked = values.value[o.key] as string[];
  const rank = (id: string) => (picked.includes(id) ? picked.indexOf(id) : picked.length);
  return out.map((r, i) => ({ r, i })).sort((a, b) => rank(a.r.id) - rank(b.r.id) || a.i - b.i).map((x) => x.r);
}
/** Move a ticked resource one place earlier (-1) or later (1) among the ticked. */
function move(key: string, id: string, by: -1 | 1): void {
  const cur = [...((values.value[key] as string[] | undefined) ?? [])];
  const i = cur.indexOf(id);
  const j = i + by;
  if (i < 0 || j < 0 || j >= cur.length) return;
  [cur[i], cur[j]] = [cur[j]!, cur[i]!];
  values.value = { ...values.value, [key]: cur };
}
function setSize(key: string, id: string, value: string, def: string): void {
  const cur = { ...sizes.value[key] };
  if (value === def) delete cur[id];
  else cur[id] = value;
  sizes.value = { ...sizes.value, [key]: cur };
}
function togglePick(key: string, id: string, on: boolean): void {
  const cur = (values.value[key] as string[] | undefined) ?? [];
  values.value = { ...values.value, [key]: on ? [...cur.filter((x) => x !== id), id] : cur.filter((x) => x !== id) };
}
/** Stored only when it differs from the default. */
function isDefault(o: WidgetOption, v: OptionValue): boolean {
  return o.kind === 'resources' ? !(v as string[]).length : v === o.default;
}

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
  const options = { ...config?.options };
  for (const o of widgetOptions.value) {
    const v = values.value[o.key] ?? optionValue(o, undefined);
    if (isDefault(o, v)) delete options[o.key];
    else options[o.key] = v;
    if (o.kind === 'resources' && o.sizes) {
      const sz = sizes.value[o.sizes.key] ?? {};
      if (Object.keys(sz).length) options[o.sizes.key] = sz;
      else delete options[o.sizes.key];
    }
  }
  if (isCalendar) {
    if (unmonitored.value) options['unmonitored'] = true;
    else delete options['unmonitored'];
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
  if (Object.keys(options).length) next.options = options;
  else delete next.options;
  if (Object.keys(bind).length) next.bind = bind;
  else delete next.bind;
  emit('save', Object.keys(next).length ? next : undefined);
}
</script>

<template>
  <UiDialog as="form" class="edit-dlg editor" size="sm" :title="`Edit ${config?.name || placeholder}`" @close="emit('close')" @submit="submit">
    <template #head><span class="edit-dlg-kind">{{ spec?.name ?? item.type }}</span></template>

    <UiField label="Name" :help="boundName !== undefined ? 'Named after its integration. Rename that in Settings → Integrations.' : 'Empty uses the widget’s own name.'" v-slot="{ control }">
      <input v-bind="control" v-model="name" type="text" autocomplete="off" maxlength="200" :placeholder="boundName ?? placeholder" :disabled="boundName !== undefined" />
    </UiField>
    <UiField label="Link" :error="hrefBad && 'Links start with http:// or https://.'" v-slot="{ control }">
      <input v-bind="control" v-model="href" type="url" inputmode="url" autocomplete="off" placeholder="https://" />
    </UiField>
    <UiField v-if="kind === 'service'" label="Description" v-slot="{ control }">
      <input v-bind="control" v-model="description" type="text" autocomplete="off" maxlength="500" />
    </UiField>

    <UiField v-if="kind === 'resource'" label="Shows" v-slot="{ control }">
      <select v-bind="control" v-model="target">
        <option v-for="r in candidates" :key="r.id" :value="r.id">{{ r.kind ? candidateLabel(live.graph.value, r) : r.name }}</option>
      </select>
    </UiField>
    <UiField v-else-if="kind === 'service'" label="Status from" v-slot="{ control }">
      <select v-bind="control" v-model="binding">
        <option value="">Nothing (no status)</option>
        <option v-if="binding && !groups.some((g) => g.choices.some((c) => c.value === binding))" :value="binding">{{ binding.replace(/^[a-z]+:/, '') }} (not in the graph)</option>
        <optgroup v-for="gr in groups" :key="gr.label" :label="gr.label">
          <option v-for="c in gr.choices" :key="c.value" :value="c.value">{{ c.label }}</option>
        </optgroup>
      </select>
    </UiField>

    <UiField v-if="isCalendar" check label="Include unmonitored releases" help="Lists what Sonarr, Radarr and Lidarr know about but aren’t monitoring, marked as such." v-slot="{ control }">
      <input v-bind="control" v-model="unmonitored" type="checkbox" />
    </UiField>

    <template v-for="o in widgetOptions" :key="o.key">
      <UiField v-if="o.kind === 'boolean'" check :label="o.label" :help="o.help" v-slot="{ control }">
        <input v-bind="control" v-model="values[o.key]" type="checkbox" />
      </UiField>
      <UiField v-else-if="o.kind === 'choice'" :label="o.label" :help="o.help" v-slot="{ control }">
        <select v-bind="control" v-model="values[o.key]">
          <option v-for="c in o.choices" :key="c.value" :value="c.value">{{ c.label }}</option>
        </select>
      </UiField>
      <fieldset v-else-if="o.kind === 'resources' && pickable(o).length" class="field picks">
        <legend>{{ o.label }}</legend>
        <div v-for="r in pickable(o)" :key="r.id" class="pick">
          <label class="pick-name">
            <input type="checkbox" :checked="(values[o.key] as string[]).includes(r.id)" @change="togglePick(o.key, r.id, ($event.target as HTMLInputElement).checked)" />
            {{ label(r) }}
          </label>
          <select
            v-if="o.sizes"
            class="pick-size"
            :aria-label="`${o.sizes.label} of ${label(r)}`"
            :value="sizes[o.sizes.key]?.[r.id] ?? o.sizes.default"
            @change="setSize(o.sizes.key, r.id, ($event.target as HTMLSelectElement).value, o.sizes.default)"
          >
            <option v-for="c in o.sizes.choices" :key="c.value" :value="c.value">{{ c.label }}</option>
          </select>
          <template v-if="o.ordered && (values[o.key] as string[]).includes(r.id)">
            <button type="button" class="pick-mv" :aria-label="`Move ${label(r)} earlier`" :disabled="(values[o.key] as string[]).indexOf(r.id) === 0" @click="move(o.key, r.id, -1)">↑</button>
            <button type="button" class="pick-mv" :aria-label="`Move ${label(r)} later`" :disabled="(values[o.key] as string[]).indexOf(r.id) === (values[o.key] as string[]).length - 1" @click="move(o.key, r.id, 1)">↓</button>
          </template>
        </div>
        <span v-if="o.help" class="help">{{ o.help }}</span>
      </fieldset>
    </template>

    <template #foot>
      <UiButton variant="ghost" @click="emit('close')">Cancel</UiButton>
      <UiButton variant="primary" type="submit" :disabled="hrefBad">Save</UiButton>
    </template>
  </UiDialog>
</template>
