<script setup lang="ts">
/**
 * Bookmarks: the services the Home Graph discovered, each with its link and
 * a monogram (icons from other origins can't load under the CSP). A link
 * whose service or check is down gets ■ on its row; links don't need you
 * otherwise. Linkding and Karakeep publish counts, not bookmarks, so they
 * appear as sources: when their sync breaks, the list keeps what it had and
 * says so. Manual links wait for per-tile config.
 *
 * Target: a group (`bookmarks@Media`, matching `labels['chadash.group']` or
 * `homepage.group`), else everything. Links labelled
 * `chadash.visibility: admin` are hidden from other roles and counted.
 *
 * Glyph: one link. Small: four. Wide: six to eight in two columns. Tall: a
 * list of up to ten.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph from '../components/marks/Glyph.vue';
import { monogram } from '../lib/format';
import { attr, distinct, label, resolve } from '../model/graph';
import { hostOf } from '../model/service';
import { normalizeHealth, resourceLinks, safeUrl, type Resource } from '../model/types';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';
import { checkFor } from './read';
import { canRepair, statusOf, subjectOf } from './subject';
import { useRole } from './use';

const { size, target } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();
const role = useRole();

const group = computed(() => (target && !resolve(live.graph.value, target) ? target.toLowerCase() : ''));
interface Link {
  id: string;
  name: string;
  url: string;
  host: string;
  down: boolean;
}
const candidates = computed(() =>
  distinct(live.graph.value)
    .filter((r) => r.kind === 'app' && attr(r, 'self') !== true && resourceLinks(r).some((l) => safeUrl(l.url)))
    .filter((r) => !group.value || [r.labels?.['chadash.group'], r.labels?.['homepage.group']].some((gname) => gname?.toLowerCase() === group.value))
    .sort((a, b) => label(a).localeCompare(label(b))),
);
const adminOnly = (r: Resource) => r.labels?.['chadash.visibility'] === 'admin';
const visible = computed(() => (canRepair(role()) ? candidates.value : candidates.value.filter((r) => !adminOnly(r))));
const hidden = computed(() => candidates.value.length - visible.value.length);
const links = computed<Link[]>(() =>
  visible.value.map((r) => {
    const url = resourceLinks(r).find((l) => safeUrl(l.url))!.url;
    const check = checkFor(live.graph.value, live.metrics.value, r);
    const checkDown = check?.resource ? normalizeHealth(check.resource).status === 'down' : check?.series ? check.series.value === null && !check.series.stale : false;
    return { id: r.id, name: label(r), url, host: hostOf(url), down: statusOf(r) === 'down' || checkDown };
  }),
);
const limit = computed(() => (size === 'glyph' ? 1 : size === 'small' ? 4 : size === 'wide' ? 8 : 10));
const shown = computed(() => links.value.slice(0, limit.value));
const downs = computed(() => links.value.filter((l) => l.down));
/** Linkding and Karakeep: the bookmark sources, and whether their sync works. */
const sources = computed(() =>
  ['linkding', 'karakeep']
    .flatMap((t) => distinct(live.graph.value).filter((r) => r.kind === 'app' && (attr(r, 'type') === t || r.id === `app:${t}`)))
    .map((r) => subjectOf(live.graph.value, live.problems.value, live.repairs.value, r.id)),
);
const broken = computed(() => sources.value.filter((x) => x.needsRepair));
const name = computed(() => (group.value ? `Links · ${target}` : 'Bookmarks'));
</script>

<template>
  <Frame
    :size="size"
    :name="size === 'glyph' && shown[0] ? shown[0].name : name"
    :mono="size === 'glyph' && shown[0] ? monogram(shown[0].name) : 'BM'"
    :context="size === 'glyph' ? '' : broken.length ? '' : `${links.length} links`"
    :status="downs.length ? 'down' : 'ok'"
    :tag="downs.length ? `${downs.length === 1 ? downs[0]!.name : downs.length} down` : broken.length ? 'sync broken' : ''"
    :tag-glyph="downs.length ? 'down' : broken.length ? 'repair' : undefined"
    :incident="broken[0]?.repair"
  >
    <template v-if="!links.length">
      <div :class="size === 'glyph' ? 'gv unknown' : 'v sm word'">{{ size === 'glyph' ? '—' : 'No bookmarks yet' }}</div>
      <p v-if="size !== 'glyph'" class="lab">{{ hidden ? `${hidden} links are for admins only.` : 'Pin a service or import from Linkding.' }}</p>
    </template>

    <template v-else-if="size === 'glyph'">
      <a class="gv bm-glyph" :href="shown[0]!.url" target="_blank" rel="noopener noreferrer" :aria-label="`Open ${shown[0]!.name}`" @click.stop>
        <Glyph :kind="shown[0]!.down ? 'down' : 'link'" />
      </a>
      <div class="gl">{{ shown[0]!.host }}</div>
    </template>

    <template v-else>
      <ul :class="['bm', { two: size === 'wide' }]" :aria-label="name">
        <li v-for="l in shown" :key="l.id">
          <a :href="l.url" target="_blank" rel="noopener noreferrer" :class="{ down: l.down }" @click.stop>
            <span class="mono" aria-hidden="true">{{ monogram(l.name) }}</span>
            <span class="bm-t">{{ l.name }}<small v-if="size !== 'small'">{{ l.host }}</small></span>
            <Glyph v-if="l.down" kind="down" label="down" />
          </a>
        </li>
      </ul>
      <div v-if="broken.length" class="note w-note n-repair">
        <Glyph kind="repair" />
        <span>{{ broken.map((b) => label(b.resource)).join(' and ') }} sync broken; the list keeps what it had.</span>
      </div>
      <div v-if="links.length > shown.length || hidden" class="tf">
        <span>{{ links.length > shown.length ? `+${links.length - shown.length} more` : '' }}{{ links.length > shown.length && hidden ? ' · ' : '' }}{{ hidden ? `${hidden} admin-only hidden` : '' }}</span>
      </div>
    </template>
  </Frame>
</template>

<style>
/* ───────────── Bookmarks ───────────── */
.bm {
  flex: 1;
  min-height: 0;
  overflow: hidden;
  display: grid;
  grid-auto-rows: min-content;
  gap: 2px var(--space-3);
  margin: 0;
  padding: 0;
  list-style: none;
}
.bm.two {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}
.bm a {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
  min-height: 28px;
  border-radius: var(--radius-sm);
  font-size: var(--type-small);
  color: var(--text);
  text-decoration: none;
}
.bm a:hover .bm-t,
.bm a:focus-visible .bm-t {
  text-decoration: underline;
  text-underline-offset: 2px;
}
.bm a:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.bm .mono {
  width: 18px;
  height: 18px;
  font-size: 7.5px;
}
.bm-t {
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.bm-t small {
  margin-left: 6px;
  font: var(--weight-regular) var(--type-caption) / 1 var(--font-num);
  color: var(--text-3);
}
.bm a.down .mono {
  background: var(--down);
  color: var(--on-down);
  border-color: transparent;
}
.bm .g {
  width: 8px;
  height: 8px;
  margin-left: auto;
}
</style>
