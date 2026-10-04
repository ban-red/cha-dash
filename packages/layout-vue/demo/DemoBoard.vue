<!--
  A minimal usage page for readers. It isn't served or built: drop it into
  any Vite + Vue app that loads `@chadash/tokens/tokens.css` to try it.
-->
<script setup lang="ts">
import { createBoard, sizeLabel, type Item } from '@chadash/layout';
import { shallowRef } from 'vue';
import { LayoutBoard, useLayoutHistory } from '../src/index.ts';

const NAMES: Record<string, string> = { pve: 'Proxmox', nas: 'NAS', dns: 'Pi-hole', plex: 'Plex', clock: 'Clock', cams: 'Cameras' };

const { board, commit, undo, redo, canUndo, canRedo } = useLayoutHistory(
  createBoard({
    sections: [
      {
        id: 'infra',
        title: 'Infrastructure',
        span: 8,
        items: [
          { id: 'pve', type: 'proxmox.node', size: 'large', supportedSizes: ['wide', 'large'] },
          { id: 'nas', type: 'unraid.array', size: 'wide', supportedSizes: ['small', 'wide', 'large'] },
          { id: 'dns', type: 'pihole', size: 'small', supportedSizes: ['small', 'wide'] },
          { id: 'plex', type: 'plex', size: 'small', supportedSizes: ['small', 'wide', 'large'] },
        ],
      },
      { id: 'today', title: 'Today', span: 4, items: [{ id: 'clock', type: 'clock', size: 'small', supportedSizes: ['small', 'wide'] }] },
      { id: 'media', title: 'Media', span: 12, items: [{ id: 'cams', type: 'frigate', size: 'hero', supportedSizes: ['large', 'hero'] }] },
    ],
  }),
);

const editing = shallowRef(false);
const label = (item: Item): string => NAMES[item.id] ?? item.id;
</script>

<template>
  <main class="demo">
    <header class="demo-bar">
      <button type="button" @click="editing = !editing">{{ editing ? 'Done' : 'Edit layout' }}</button>
      <button type="button" :disabled="!canUndo" @click="undo">Undo</button>
      <button type="button" :disabled="!canRedo" @click="redo">Redo</button>
    </header>

    <!-- Previews stay inside the board; only @commit reaches history. -->
    <LayoutBoard :board="board" :editing="editing" :item-label="label" @commit="commit">
      <template #item="{ item, size, dragging }">
        <article class="demo-tile" :class="{ 'is-dragging': dragging }">
          <span class="demo-name">{{ label(item) }}</span>
          <span class="demo-size">{{ size }} · {{ sizeLabel(size) }}</span>
        </article>
      </template>
    </LayoutBoard>
  </main>
</template>

<style>
.demo {
  max-width: var(--board-max);
  margin: 0 auto;
  padding: var(--space-6);
  background: var(--bg);
  color: var(--text);
  font-family: var(--font-ui);
}

.demo-bar {
  display: flex;
  gap: var(--space-2);
  margin-bottom: var(--space-6);
}

.demo-tile {
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  height: 100%;
  box-sizing: border-box;
  padding: var(--tile-pad-y) var(--tile-pad-inline);
  border: var(--tile-border-width) solid var(--tile-border-color);
  border-radius: var(--tile-radius);
  background: var(--tile-bg);
}

.demo-name {
  font-weight: var(--weight-medium);
}

.demo-size {
  font: var(--type-small) / 1 var(--font-num);
  color: var(--text-3);
}
</style>
