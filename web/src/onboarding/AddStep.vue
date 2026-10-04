<script setup lang="ts">
/**
 * The wizard's "one by one" path: everything this hub can read, grouped the
 * way a home is, each with the honest label from its recipe (read-only,
 * limited, full access only). Picking one opens its add form in Settings,
 * with the recipe beside it; closing Settings comes back here.
 */
import { computed } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import { monogram } from '../lib/format';
import { navigate } from '../lib/router';
import { accessOf } from '../settings/recipe';
import { integrations } from '../stores/integrations';
import type { CatalogEntry } from '../model/integrations';

const GROUPS: readonly { title: string; ids: readonly string[] }[] = [
  { title: 'Servers and network', ids: ['proxmox', 'pbs', 'unraid', 'docker', 'opnsense', 'pihole', 'adguard', 'traefik', 'uptimekuma', 'checks'] },
  { title: 'Home', ids: ['homeassistant', 'frigate', 'weather'] },
  { title: 'Media and downloads', ids: ['plex', 'jellyfin', 'sonarr', 'radarr', 'prowlarr', 'qbittorrent', 'sabnzbd', 'tautulli', 'bazarr', 'seerr'] },
  { title: 'Apps', ids: ['immich', 'mealie', 'linkding', 'karakeep'] },
];

const groups = computed(() => {
  const byId = new Map(integrations.catalog.map((c) => [c.id, c]));
  const placed = new Set<string>();
  const out: { title: string; items: CatalogEntry[] }[] = [];
  for (const g of GROUPS) {
    const items = g.ids.flatMap((id) => (byId.has(id) ? [byId.get(id)!] : []));
    items.forEach((c) => placed.add(c.id));
    if (items.length) out.push({ title: g.title, items });
  }
  const rest = integrations.catalog.filter((c) => !placed.has(c.id));
  if (rest.length) out.push({ title: 'More', items: rest });
  return out;
});

const connected = (id: string) => integrations.instances.filter((x) => x.integration === id).length;
const addPath = (id: string) => `/settings/integrations/new/${encodeURIComponent(id)}`;
</script>

<template>
  <div class="ob-add">
    <div v-if="integrations.catalogStatus === 'loading' || integrations.catalogStatus === 'idle'" class="ob-skel" aria-busy="true">
      <span class="skel"></span><span class="skel"></span><span class="skel"></span>
    </div>
    <div v-else-if="integrations.catalogStatus === 'error'" class="form-error" role="alert"><Glyph kind="down" /><span>{{ integrations.error }}</span></div>
    <template v-else>
      <section v-for="g in groups" :key="g.title" class="ob-block" :aria-label="g.title">
        <h3 class="caps">{{ g.title }}</h3>
        <ul class="ob-grid">
          <li v-for="c in g.items" :key="c.id">
            <a class="ob-item" :href="addPath(c.id)" @click.prevent="navigate(addPath(c.id))">
              <span class="ob-badge" aria-hidden="true">{{ monogram(c.name) }}</span>
              <span class="ob-item-main">
                <b>{{ c.name }}</b>
                <span class="ob-m">
                  <template v-if="accessOf(c.recipe)">{{ accessOf(c.recipe)!.label }}</template>
                  <template v-if="connected(c.id)"> · {{ connected(c.id) }} connected</template>
                </span>
              </span>
              <span aria-hidden="true" class="ob-chev">›</span>
            </a>
          </li>
        </ul>
      </section>
    </template>
  </div>
</template>
