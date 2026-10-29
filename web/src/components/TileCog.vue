<script setup lang="ts">
/**
 * A tile's settings cog, top-right, shown on hover or focus (on a touch
 * screen, once the tile has focus). It opens the settings of the integration that feeds the
 * tile (model/integrations.ts → tileInstance), else the tile's Edit. It's
 * absent outside the app, in edit mode, and for a role that can open neither.
 */
import { computed } from 'vue';
import { tileInstance } from '../model/integrations';
import { useLive } from '../stores/live';
import { useTile } from '../stores/tiles';
import { useUiIfAny } from '../stores/ui';

const { name } = defineProps<{ name: string }>();
const live = useLive();
const tile = useTile();
const ui = useUiIfAny();

const instance = computed(() => tileInstance(live.graph.value, tile.type, tile.config.value, tile.target));
const where = computed(() => (ui && tile.itemId && !ui.state.editing ? ui.tileSettings(instance.value) : undefined));
const label = computed(() => (where.value === 'tile' ? `Edit ${name}` : `${name} settings`));
</script>

<template>
  <button v-if="where" type="button" class="tile-cog" :aria-label="label" :title="label" @click.stop="ui?.openTileSettings(tile.itemId, instance)">
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M6.9 1.9h2.2l.4 1.7 1.2.7 1.7-.5 1.1 1.9-1.3 1.2v1.4l1.3 1.2-1.1 1.9-1.7-.5-1.2.7-.4 1.7H6.9l-.4-1.7-1.2-.7-1.7.5-1.1-1.9 1.3-1.2V7.3L2.5 6.1l1.1-1.9 1.7.5 1.2-.7z" />
      <circle cx="8" cy="8" r="1.9" />
    </svg>
  </button>
</template>
