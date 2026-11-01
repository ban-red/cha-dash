<script setup lang="ts">
/**
 * The Atlas lens (`?atlas=`, ⌘K "Open the Atlas", a sheet's "Show on the
 * Atlas"): the whole home as a map, full screen, with the same nodes as a
 * list beside it. Layers toggle what's drawn; each viewer's choice is
 * remembered on this device. Selecting a node that isn't ok lights the
 * path to its first cause and dims the rest, and the bar under the map
 * says why in words. The causes topic leaves paths out, so the lens asks
 * `GET /api/causes` for the selection's. Lazy: its own chunk, reading the
 * graph and causes the page already has.
 *
 * "Open its sheet" leaves the lens for the sheet (`sheetPath` drops
 * `?atlas=`): the lens is a modal dialog, so a sheet under it couldn't be
 * reached. Back comes back to the lens.
 */
import { computed, ref, watch } from 'vue';
import UiDialog from '../ui/Dialog.vue';
import UiButton from '../ui/Button.vue';
import Glyph from '../ui/marks/Glyph.vue';
import { glyphFor } from '../model/status';
import { api } from '../lib/api';
import { atlasPath, navigate, route } from '../lib/router';
import { buildAtlas, DEFAULT_LAYERS, drawnAt, KIND_WORDS, LAYERS, lit, type Layer } from '../model/atlas';
import type { CausesResponse, CauseStep } from '../model/types';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import AtlasCanvas from './AtlasCanvas.vue';
import AtlasList from './AtlasList.vue';
import './atlas.css';

const live = useLive();
const ui = useUi();

const STORE = 'chadash.atlas.layers';
function loadLayers(): Layer[] {
  try {
    const v = JSON.parse(localStorage.getItem(STORE) ?? 'null') as unknown;
    if (Array.isArray(v)) return LAYERS.map((l) => l.id).filter((id) => v.includes(id));
  } catch {
    // no storage here: the defaults
  }
  return [...DEFAULT_LAYERS];
}
const layers = ref<Layer[]>(loadLayers());
watch(layers, (v) => {
  try {
    localStorage.setItem(STORE, JSON.stringify(v));
  } catch {
    // a private window: not remembered
  }
});
function toggle(id: Layer, on: boolean): void {
  layers.value = LAYERS.map((l) => l.id).filter((l) => (l === id ? on : layers.value.includes(l)));
}

const expanded = ref(new Set<string>());
const atlas = computed(() => buildAtlas(live.graph.value, { layers: layers.value, expanded: expanded.value }));
function expand(group: string): void {
  expanded.value = new Set([...expanded.value, group.replace(/^group:/, '')]);
}

/** The node selected: the route's focus at first (a folded one shows its group). */
const selected = ref<string | undefined>();
// Again when the graph arrives: a link opened cold has no nodes yet.
watch(
  () => [ui.atlas.value, atlas.value.nodes.size > 0] as const,
  ([focus], old) => {
    if (focus && (focus !== old?.[0] || !selected.value)) selected.value = drawnAt(atlas.value, focus);
  },
  { immediate: true },
);
const node = computed(() => (selected.value ? atlas.value.nodes.get(selected.value) : undefined));
const hints = computed(() => (node.value && node.value.health !== 'ok' ? live.causesOf(node.value.id) : []));

/** The first cause's path, from the hint when it has one, else asked of the hub. */
const fetched = ref<{ id: string; key: string; path: CauseStep[] } | undefined>();
const hintKey = computed(() => {
  const h = hints.value[0];
  return h ? `${h.rule} ${h.resource}` : '';
});
watch(
  () => [node.value?.id, hintKey.value] as const,
  async ([id, key]) => {
    if (!id || !key || hints.value[0]!.path || (fetched.value?.id === id && fetched.value.key === key)) return;
    try {
      const r = await api<CausesResponse>(`/api/causes?resource=${encodeURIComponent(id)}`);
      fetched.value = { id, key, path: r.causes[0]?.path ?? [] };
    } catch {
      // no path: the selection stays unlit, its causes still in words
    }
  },
  { immediate: true },
);
const firstPath = computed(() => {
  const h = hints.value[0];
  if (!h) return undefined;
  if (h.path) return h.path;
  const f = fetched.value;
  return f && f.id === node.value?.id && f.key === hintKey.value ? f.path : undefined;
});
const path = computed(() => (node.value && firstPath.value ? lit(atlas.value, node.value.id, firstPath.value) : undefined));

const WORDS: Record<string, string> = { ok: 'ok', attention: 'needs attention', down: 'down', maintenance: 'in maintenance', repair: 'needs repair', unknown: 'no reading yet' };

function select(id: string): void {
  selected.value = id;
}

/** Leave for the sheet, keeping the selection in this entry so Back returns to it. */
function openSheet(id: string): void {
  navigate(atlasPath(route.value, id), { replace: true });
  ui.openSheet(id);
}
</script>

<template>
  <UiDialog size="full" class="atlas-lens" title="Atlas" close-label="Close the Atlas" @close="ui.closeAtlas()">
    <template #head>
      <fieldset class="atlas-layers">
        <legend class="sr">Layers</legend>
        <label v-for="l in LAYERS" :key="l.id" :title="l.about">
          <input type="checkbox" :checked="layers.includes(l.id)" @change="toggle(l.id, ($event.target as HTMLInputElement).checked)" />
          {{ l.name }}
        </label>
      </fieldset>
    </template>
    <div class="atlas-panes">
      <div class="atlas-main">
        <AtlasCanvas
          :atlas="atlas"
          :selected="selected"
          :lit="path"
          label="Atlas map: use the arrow keys to move, Enter to select. The list beside it has the same nodes."
          @open="select"
          @select="select"
          @expand="expand"
        />
        <div class="atlas-bar" aria-live="polite">
          <template v-if="node">
            <Glyph :kind="glyphFor(node.health)" />
            <div class="atlas-bar-text">
              <b>{{ node.label }}</b>
              <span class="muted"> · {{ KIND_WORDS[node.kind] ?? node.kind }} · {{ WORDS[node.health] ?? node.health }}<template v-if="node.reason && node.health !== 'ok'">: {{ node.reason }}</template></span>
              <ul v-if="hints.length" class="atlas-causes">
                <li v-for="c in hints" :key="`${c.rule} ${c.resource}`">Because {{ c.text.charAt(0).toLowerCase() + c.text.slice(1) }}</li>
              </ul>
            </div>
            <UiButton v-if="ui.hasSheet(node.id)" size="sm" @click="openSheet(node.id)">Open its sheet</UiButton>
          </template>
          <span v-else class="muted">Select something to see what it needs and why it isn’t fine.</span>
        </div>
      </div>
      <AtlasList :atlas="atlas" :selected="selected" :causes-of="live.causesOf" @open="select" @expand="expand" />
    </div>
  </UiDialog>
</template>

<style>
.atlas-lens .dlg-body {
  gap: var(--space-3);
}
.atlas-layers {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-1) var(--space-3);
  margin: 0 0 0 var(--space-3);
  padding: 0;
  border: 0;
  font-size: var(--type-small);
}
.atlas-layers label {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--text-2);
  cursor: pointer;
}
.atlas-panes {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 300px;
  gap: var(--space-3);
  flex: 1;
  min-height: 0;
}
.atlas-main {
  display: grid;
  grid-template-rows: minmax(0, 1fr) auto;
  gap: var(--space-2);
  min-height: 0;
}
.atlas-bar {
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  min-height: 40px;
  font-size: var(--type-small);
}
.atlas-bar > .g {
  width: 12px;
  height: 12px;
  margin-top: 3px;
}
.atlas-bar-text {
  flex: 1;
  min-width: 0;
}
.atlas-causes {
  margin: 2px 0 0;
  padding: 0;
  list-style: none;
  color: var(--text-2);
}
@media (max-width: 760px) {
  .atlas-panes {
    grid-template-columns: minmax(0, 1fr);
    grid-template-rows: minmax(0, 3fr) minmax(0, 2fr);
  }
}
</style>
