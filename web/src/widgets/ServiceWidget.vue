<script setup lang="ts">
/**
 * A service: the generic tile every imported Homepage entry without a
 * catalog widget becomes (`service@homepage:<section>/<entry>`). Its name,
 * link and description come from the import record (stores/tiles.ts); its
 * status from the check or integration instance that watches it, when it has
 * one. Small: name, a line of description, the link. Wide: the whole
 * description and where it came from.
 *
 * Icons are monograms for now: the CSP forbids hot-linking a logo from
 * another origin, and the hub doesn't cache icons yet.
 */
import { computed, onMounted } from 'vue';
import type { SizeId } from '@chadash/layout';
import Tile from '../components/Tile.vue';
import { glyphFor } from '../model/status';
import { hostOf, serviceStatus } from '../model/service';
import { ensureTileMeta, tiles } from '../stores/tiles';
import { useLive } from '../stores/live';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const itemId = computed(() => `service@${target ?? ''}`);
const meta = computed(() => tiles.byItem[itemId.value]);
/** Without a record: the entry's slug ("homepage:servers/proxmoxpluto" → "proxmoxpluto"). */
const fallback = computed(() => (target ?? '').split('/').pop()?.replace(/^[a-z]+:/, '') || 'Service');
const name = computed(() => meta.value?.name ?? fallback.value);
const host = computed(() => hostOf(meta.value?.href));
const status = computed(() => serviceStatus(live.graph.value, meta.value));
const loading = computed(() => tiles.status === 'idle' || (tiles.status === 'loading' && !meta.value));

onMounted(() => void ensureTileMeta());
</script>

<template>
  <Tile
    :size="size"
    :name="name"
    :context="size === 'wide' ? host : ''"
    :href="meta?.href"
    :status="status?.status ?? 'ok'"
    :tag="status?.word"
    :tag-glyph="status ? (status.via === 'none' ? 'repair' : glyphFor(status.status)) : undefined"
    :loading="loading"
  >
    <p v-if="meta?.description" :class="['lab', 'svc-desc', { full: size === 'wide' }]">{{ meta.description }}</p>
    <p v-else-if="!meta && tiles.status !== 'loading'" class="lab">Imported from Homepage.</p>
    <div class="tf">
      <a v-if="meta?.href" class="svc-link" :href="meta.href" target="_blank" rel="noopener noreferrer">
        <span>{{ host || 'Open' }}</span><span aria-hidden="true"> ↗</span>
      </a>
      <span v-else>No link</span>
      <span v-if="size === 'wide' && meta?.origin" class="svc-origin">{{ meta.origin }}</span>
    </div>
  </Tile>
</template>

<style>
.svc-desc {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.svc-desc.full {
  -webkit-line-clamp: 3;
}
.svc-link {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-2);
  text-decoration: none;
  font-family: var(--font-num);
}
.svc-link:hover {
  color: var(--text);
  text-decoration: underline;
}
.svc-origin {
  flex: none;
  color: var(--text-3);
}
</style>
