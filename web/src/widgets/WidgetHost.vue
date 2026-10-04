<script setup lang="ts">
/**
 * Renders one layout item at the size shown at the current breakpoint: picks
 * the component for its type and binds the target resource from its id
 * (`type@resource`). This is what goes in the board renderer's `#item` slot,
 * for StaticBoard and @chadash/layout-vue's LayoutBoard alike.
 */
import { computed } from 'vue';
import type { Item, SizeId } from '@chadash/layout';
import Tile from '../components/Tile.vue';
import { parseItemId, WIDGETS } from '../model/widgets';
import { WIDGET_COMPONENTS } from './registry';

const { item, size } = defineProps<{ item: Pick<Item, 'id' | 'type'>; size: SizeId }>();

const component = computed(() => WIDGET_COMPONENTS[item.type]);
const target = computed(() => parseItemId(item.id).target);
</script>

<template>
  <component :is="component" v-if="component" :size="size" :target="target" />
  <Tile v-else :size="size" :name="WIDGETS[item.type]?.name ?? item.type" status="unknown">
    <div class="v sm unknown">—</div>
    <p class="lab">This widget isn't available in this version of Cha Dash.</p>
  </Tile>
</template>
