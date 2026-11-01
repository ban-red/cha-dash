<script setup lang="ts">
/**
 * The Atlas as a Hero tile (8×4, large on a phone): the map with the layers
 * and the zoom it was saved with (Widget settings). Selecting a node opens
 * the Atlas lens centred on it, where its cause path lights up; "Open" opens
 * the lens as it is. Its own chunk, with the lens's map component.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import AtlasCanvas from '../atlas/AtlasCanvas.vue';
import UiButton from '../ui/Button.vue';
import { buildAtlas, LAYERS, type Layer } from '../model/atlas';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';
import Frame from './parts/Frame.vue';
import { useOptions } from './options';

const { size } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();
const ui = useUi();
const opts = useOptions('atlas');

const layers = computed<Layer[]>(() => LAYERS.map((l) => l.id).filter((id) => opts.value[id] === true));
const atlas = computed(() => buildAtlas(live.graph.value, { layers: layers.value }));
const zoom = computed<'fit' | number>(() => (opts.value['zoom'] === 'fit' ? 'fit' : Number(opts.value['zoom']) || 'fit'));
const trouble = computed(() => [...atlas.value.nodes.values()].filter((n) => n.health === 'down' || n.health === 'attention').length);
</script>

<template>
  <Frame :size="size" name="Atlas" mono="AT" :ok-tag="trouble ? '' : 'all fine'" :tag="trouble ? `${trouble} not fine` : undefined" :tag-glyph="trouble ? 'attn' : undefined">
    <div class="atlas-tile">
      <AtlasCanvas :atlas="atlas" :zoom="zoom" :wheel="false" label="Atlas map: select something to open it in the Atlas" @open="ui.openAtlas($event)" @select="() => {}" @expand="ui.openAtlas()" />
      <UiButton class="atlas-open" size="sm" @click="ui.openAtlas()">Open</UiButton>
    </div>
  </Frame>
</template>

<style>
.atlas-tile {
  position: relative;
  flex: 1;
  min-height: 0;
  height: 100%;
}
.atlas-tile .atlas-open {
  position: absolute;
  left: var(--space-2);
  top: var(--space-2);
}
</style>
