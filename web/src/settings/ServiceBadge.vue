<script setup lang="ts">
/**
 * An integration's mark in Settings lists: its vendor logo on a plate, or the
 * monogram while there is none (no bundled logo, or the image failed to
 * load). The bundled logo of its integration (lib/serviceIcons.ts) comes first,
 * then the icon the hub cached for the connection (stores/icons.ts). Logos drawn
 * in black or white get the opposite plate, as on tiles (lib/iconTone.ts).
 */
import { computed, ref, watch } from 'vue';
import { iconTone, type IconTone } from '../lib/iconTone';
import { monogram } from '../lib/format';
import { serviceIconByName, serviceIconUrl } from '../lib/serviceIcons';
import { iconMap, loadIcons } from '../stores/icons';
import { catalogEntry } from '../stores/integrations';

const { integration, name, instance } = defineProps<{
  /** The integration's id (the catalog entry). */
  integration: string;
  /** The name to take the monogram from. */
  name: string;
  /** A connection's id, for the icon the hub cached for it. */
  instance?: string | undefined;
}>();

void loadIcons();

// The bundled logo first: a connection's cached icon is often the favicon of
// whatever host it sits behind (an *arr on Unraid shows Unraid's).
const src = computed(
  () => serviceIconUrl(catalogEntry(integration)?.icon || integration) ?? (instance ? iconMap.value.by_instance[instance] : undefined) ?? serviceIconByName(name),
);
const broken = ref('');
const tone = ref<IconTone>('mid');
watch(src, () => (tone.value = 'mid'));
const shown = computed(() => (src.value && src.value !== broken.value ? src.value : undefined));
</script>

<template>
  <span :class="['mono-badge', { 'has-icon': shown, [`tone-${tone}`]: shown }]" aria-hidden="true">
    <img v-if="shown" class="mono-badge-img" :src="shown" alt="" @load="tone = iconTone($event.target as HTMLImageElement)" @error="broken = shown" />
    <template v-else>{{ monogram(name) }}</template>
  </span>
</template>
