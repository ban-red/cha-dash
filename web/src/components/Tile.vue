<script setup lang="ts">
/**
 * Widget anatomy (02 · Experience): identity line (monogram · name · context ·
 * tag), then the body. The frame carries the state treatment:
 *
 *   attention / down → inset bar, tinted hairline, monogram colours in
 *   stale            → hatch, dashed hairline, "as of 14:02" (last-known values stay)
 *   off / maintenance → dimmed, quiet
 *
 * When the hub is unreachable every tile is stale, as of the last frame.
 *
 * The board's tile config (stores/tiles.ts, provided by WidgetHost) has the
 * last word on identity, for every widget alike: its name replaces the
 * widget's, its link the widget's, and a chromeless tile has no header.
 *
 * The mark is the tile's icon when the hub has cached one (stores/icons.ts:
 * the CSP keeps images from other origins out), else the monogram. A tile
 * that needs you keeps the monogram, whose colour is the state.
 *
 * Hovering or focusing a tile shows its settings cog (TileCog.vue).
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph, { type GlyphKind } from './marks/Glyph.vue';
import GlyphTip from './GlyphTip.vue';
import TileCog from './TileCog.vue';
import { hhmm, monogram } from '../lib/format';
import { focusScrollables } from '../lib/scrollable';
import { safeUrl } from '../model/types';
import { useLive } from '../stores/live';
import { useTile } from '../stores/tiles';
import { iconFor, iconMap, wantsIcon } from '../stores/icons';

export type TileStatus = 'ok' | 'attention' | 'down' | 'off' | 'maintenance' | 'repair' | 'unknown';

const {
  size,
  name,
  context = '',
  mono,
  href,
  status = 'ok',
  stale = false,
  asOf,
  tag,
  tagGlyph,
  tagNote,
  clickable = false,
  loading = false,
} = defineProps<{
  size: SizeId;
  name: string;
  context?: string;
  mono?: string;
  href?: string | undefined;
  status?: TileStatus;
  /** This widget's own data is last-known (e.g. its integration needs repair). */
  stale?: boolean;
  /** When the last-known values are from. */
  asOf?: string | undefined;
  /** The state word, after the glyph ("1 needs you", "idle"). */
  tag?: string | undefined;
  tagGlyph?: GlyphKind | undefined;
  /** The warning in full, for the glyph's tooltip and dialog; else the tag. */
  tagNote?: string | undefined;
  clickable?: boolean;
  loading?: boolean;
}>();

const emit = defineEmits<{ open: [] }>();
const live = useLive();
const tile = useTile();

const shownName = computed(() => tile.config.value?.name ?? name);
/** A warning glyph explains itself: hover for the words, click for all of them. */
const WARNS = new Set<GlyphKind>(['attn', 'down', 'repair', 'maint']);
const glyphTip = computed(() => (tagGlyph && WARNS.has(tagGlyph) ? tagNote || tag || '' : ''));
/** The tile's link, from its config or its widget: http(s) only. */
const shownHref = computed(() => safeUrl(tile.config.value?.href ?? href));
const chromeless = computed(() => tile.config.value?.chromeless === true);

const hubStale = computed(() => live.stale.value);
const isStale = computed(() => stale || hubStale.value);
const staleSince = computed(() => {
  if (hubStale.value && live.offlineAt.value) return hhmm(live.offlineAt.value);
  return asOf ? hhmm(asOf) : '';
});

const classes = computed(() => [
  'tile',
  `s-${size}`,
  {
    'is-attn': status === 'attention',
    'is-down': status === 'down',
    'is-off': status === 'off',
    'is-maint': status === 'maintenance',
    'is-stale': isStale.value,
    'is-loading': loading,
    clickable,
  },
]);

const label = computed(() => [shownName.value, context, tag].filter(Boolean).join(', '));
const mark = computed(() => (tile.config.value?.name ? monogram(tile.config.value.name) : (mono ?? monogram(name))));

// The cached icon, once the board has loaded the map; an image that fails
// to load falls back to the monogram.
const broken = ref('');
const icon = computed(() => {
  if (status === 'attention' || status === 'down' || !wantsIcon(tile.config.value, shownHref.value)) return undefined;
  const src = iconFor(iconMap.value, tile.config.value, shownHref.value);
  return src && src !== broken.value ? src : undefined;
});

// The mark's link. An icon is a graphic, so its name is "Open Photos"; a
// monogram is text people see and may say ("click PH"), so the name starts
// with it (WCAG 2.5.3, Label in Name).
const linkLabel = computed(() => (icon.value ? `Open ${shownName.value}` : `${mark.value}, open ${shownName.value}`));

/** A control inside the tile (a row that opens its sheet, the mark's link, a list that scrolls) keeps its own click and keys. */
const fromControl = (e: Event) =>
  e.target !== e.currentTarget && (e.target as Element | null)?.closest?.('button, a, input, select, textarea, summary, [data-scrolls]') != null;

// A list with more rows than room scrolls, so it takes keyboard focus meanwhile.
const root = ref<HTMLElement | null>(null);
let stopScrollables: (() => void) | undefined;
onMounted(() => root.value && (stopScrollables = focusScrollables(root.value)));
onBeforeUnmount(() => stopScrollables?.());

function onClick(e: MouseEvent) {
  if (clickable && !fromControl(e)) emit('open');
}

function onKey(e: KeyboardEvent) {
  if (!clickable || fromControl(e)) return;
  if (e.key === 'Enter' || e.key === ' ') {
    e.preventDefault();
    emit('open');
  }
}
</script>

<template>
  <article
    ref="root"
    :class="classes"
    :aria-label="label"
    :aria-busy="loading || undefined"
    :tabindex="clickable ? 0 : undefined"
    @click="onClick"
    @keydown="onKey"
  >
    <div v-if="!chromeless" class="th">
      <a v-if="shownHref" :class="['mono', { 'has-icon': icon }]" :href="shownHref" target="_blank" rel="noopener noreferrer" :aria-label="linkLabel" @click.stop>
        <img v-if="icon" class="mono-img" :src="icon" alt="" @error="broken = icon ?? ''" />
        <template v-else>{{ mark }}</template>
      </a>
      <span v-else :class="['mono', { 'has-icon': icon }]" aria-hidden="true">
        <img v-if="icon" class="mono-img" :src="icon" alt="" @error="broken = icon ?? ''" />
        <template v-else>{{ mark }}</template>
      </span>
      <span class="tn">{{ shownName }}</span>
      <span v-if="context" class="tc">{{ context }}</span>
      <span v-if="isStale && staleSince" class="tg"><span class="asof" :title="'Last-known value'">as of {{ staleSince }}</span></span>
      <span v-else-if="tag || tagGlyph" :class="['tg', { 'glyph-only': !tag }]">
        <GlyphTip v-if="tagGlyph && glyphTip" :kind="tagGlyph" :text="glyphTip" :title="shownName" />
        <Glyph v-else-if="tagGlyph" class="tg-mark" :kind="tagGlyph" />
        <span class="tgt">{{ tag }}</span>
      </span>
    </div>
    <TileCog :name="shownName" />
    <template v-if="loading">
      <span class="skel"></span>
      <span class="skel"></span>
    </template>
    <slot v-else></slot>
  </article>
</template>
