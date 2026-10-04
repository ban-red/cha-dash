<!-- Our engine and renderer on the dogfood page. -->
<script setup lang="ts">
import { createBoard, sizeLabel, type BoardInput, type Breakpoint, type Item } from '@chadash/layout';
import { LayoutBoard, useLayoutHistory } from '@chadash/layout-vue';
import { computed } from 'vue';
import { DOGFOOD } from '../scenarios/index.ts';
import { NAMES, SIZE_NAMES } from './names.ts';

const props = defineProps<{ columns: 12 | 8 | 4; editing: boolean; motion: 'full' | 'reduced' }>();

const { board, commit, undo, canUndo } = useLayoutHistory(createBoard(DOGFOOD as unknown as BoardInput));
const breakpoint = computed<Breakpoint>(() => ({ 12: 'desktop', 8: 'tablet', 4: 'phone' } as const)[props.columns]);
const label = (item: Item) => NAMES[item.id] ?? item.id;

defineExpose({ undo, canUndo });
</script>

<template>
  <div class="frame" :data-columns="columns">
    <LayoutBoard :board="board" :editing="editing" :breakpoint="breakpoint" :motion="motion" :item-label="label" @commit="commit">
      <template #item="{ item, size, placed }">
        <div class="tile" :data-forced="placed.forced || undefined">
          <span class="tile-name">{{ label(item) }}</span>
          <span class="tile-size">{{ SIZE_NAMES[size] }} · {{ sizeLabel(size) }}</span>
        </div>
      </template>
    </LayoutBoard>
  </div>
</template>
