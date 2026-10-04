<!-- The smallest editable front page on our engine: board, edit toggle, undo/redo. -->
<script setup lang="ts">
import { createBoard, type BoardInput } from '@chadash/layout';
import { LayoutBoard, useLayoutHistory } from '@chadash/layout-vue';
import { shallowRef } from 'vue';
import { DOGFOOD } from '../../scenarios/index.ts';

const { board, commit, undo, redo } = useLayoutHistory(createBoard(DOGFOOD as unknown as BoardInput));
const editing = shallowRef(false);
</script>

<template>
  <button type="button" @click="editing = !editing">Edit</button>
  <button type="button" @click="undo">Undo</button>
  <button type="button" @click="redo">Redo</button>
  <LayoutBoard :board="board" :editing="editing" @commit="commit">
    <template #item="{ item, size }">
      <div class="tile">{{ item.id }} · {{ size }}</div>
    </template>
  </LayoutBoard>
</template>
