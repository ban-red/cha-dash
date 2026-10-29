<script setup lang="ts">
/**
 * Renders one layout item at the size shown at the current breakpoint: picks
 * the component for its type and binds its target resource. This is what
 * goes in the board renderer's `#item` slot, for StaticBoard and
 * @chadash/layout-vue's LayoutBoard alike.
 *
 * The item's tile config (stores/tiles.ts) is provided to everything inside:
 * Tile.vue takes its name, link and chromeless from it, and widgets read the
 * rest with `useTile()`. The target is the config's bound resource, else the
 * one the item id carries (`type@resource`).
 */
import { computed } from 'vue';
import type { Item, SizeId } from '@chadash/layout';
import Tile from '../components/Tile.vue';
import { parseItemId, WIDGETS } from '../model/widgets';
import { useBoard } from '../stores/board';
import { provideTile } from '../stores/tiles';
import { WIDGET_COMPONENTS } from './registry';

const { item, size } = defineProps<{ item: Pick<Item, 'id' | 'type'>; size: SizeId }>();
const board = useBoard();

const component = computed(() => WIDGET_COMPONENTS[item.type]);
const config = computed(() => board.tiles.value[item.id]);
const target = computed(() => config.value?.bind?.resource ?? parseItemId(item.id).target);

provideTile({
  get itemId() {
    return item.id;
  },
  get type() {
    return item.type;
  },
  get target() {
    return target.value;
  },
  config,
});
</script>

<template>
  <component :is="component" v-if="component" :size="size" :target="target" />
  <Tile v-else :size="size" :name="WIDGETS[item.type]?.name ?? item.type" status="unknown">
    <div class="v sm unknown">—</div>
    <p class="lab">This widget isn't available in this version of Cha Dash.</p>
  </Tile>
</template>
