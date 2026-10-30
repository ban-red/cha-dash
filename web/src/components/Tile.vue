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
 *
 * A tile with views (widgets/views.ts) pages between them: a horizontal swipe
 * on a touch screen, or the dots at the foot, which take the arrow keys, Home
 * and End. The new view slides in from the side it came from (a fade under
 * reduced motion). Edit mode drags tiles, so it doesn't swipe them.
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph, { type GlyphKind } from './marks/Glyph.vue';
import GlyphTip from './GlyphTip.vue';
import TileCog from './TileCog.vue';
import { hhmm, monogram } from '../lib/format';
import { focusScrollables } from '../lib/scrollable';
import { safeUrl } from '../model/types';
import { useLive } from '../stores/live';
import { useTile } from '../stores/tiles';
import { instanceName } from '../stores/integrations';
import { serviceIconUrl } from '../lib/serviceIcons';
import { iconFor, iconMap, wantsIcon } from '../stores/icons';
import { useUiIfAny } from '../stores/ui';
import type { TileViews } from '../widgets/views';

export type TileStatus = 'ok' | 'attention' | 'down' | 'off' | 'maintenance' | 'repair' | 'unknown';

const {
  size,
  name,
  context = '',
  mono,
  logo,
  href,
  status = 'ok',
  stale = false,
  asOf,
  tag,
  tagGlyph,
  tagNote,
  tagHref,
  clickable = false,
  loading = false,
  views,
} = defineProps<{
  size: SizeId;
  name: string;
  context?: string;
  mono?: string;
  /** A bundled logo slug (lib/serviceIcons.ts) for a widget that is one service; shown in every state. */
  logo?: string | undefined;
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
  /** The upstream's page for the warning, offered in its dialog. */
  tagHref?: string | undefined;
  clickable?: boolean;
  loading?: boolean;
  /** Views to page between (widgets/views.ts); none or one shows no dots. */
  views?: TileViews | undefined;
}>();

const emit = defineEmits<{ open: [] }>();
const live = useLive();
const tile = useTile();
const ui = useUiIfAny();

/** A tile bound to an integration instance carries that instance's name, so renaming it in Settings renames the tile. */
const boundName = computed(() => instanceName(tile.config.value?.bind?.instance));
const shownName = computed(() => boundName.value ?? tile.config.value?.name ?? name);
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

// Views: the dots, the swipe, and which way the new view slides in.
const paging = computed(() => (views?.list.length ?? 0) > 1 && !loading);
const viewIndex = computed(() => views?.list.findIndex((v) => v.id === views.current) ?? -1);
const currentView = computed(() => (paging.value ? views?.list[viewIndex.value] : undefined));
const slide = ref<'' | 'next' | 'prev'>('');
let slideTimer: ReturnType<typeof setTimeout> | undefined;
const dots = ref<HTMLElement | null>(null);

function show(i: number, focus = false): void {
  const list = views?.list;
  if (!list || !paging.value) return;
  const n = list.length;
  const to = ((i % n) + n) % n;
  if (to === viewIndex.value) return;
  slide.value = to > viewIndex.value ? 'next' : 'prev';
  clearTimeout(slideTimer);
  slideTimer = setTimeout(() => (slide.value = ''), 600);
  views.select(list[to]!.id);
  if (focus) void nextTick(() => dots.value?.querySelectorAll<HTMLElement>('button')[to]?.focus());
}

function onDotsKey(e: KeyboardEvent): void {
  const n = views?.list.length ?? 0;
  const to = { ArrowRight: viewIndex.value + 1, ArrowDown: viewIndex.value + 1, ArrowLeft: viewIndex.value - 1, ArrowUp: viewIndex.value - 1, Home: 0, End: n - 1 }[e.key];
  if (to === undefined) return;
  e.preventDefault();
  e.stopPropagation();
  show(to, true);
}

// A swipe: a touch or pen that moves mostly sideways, far enough. The click
// that ends it is swallowed, so a swipe never opens the tile or follows a link.
const SWIPE_PX = 40;
let start: { id: number; x: number; y: number } | undefined;
let swiped = false;
const canSwipe = computed(() => paging.value && !ui?.state.editing);

function onPointerDown(e: PointerEvent): void {
  swiped = false;
  start = canSwipe.value && e.pointerType !== 'mouse' && e.isPrimary ? { id: e.pointerId, x: e.clientX, y: e.clientY } : undefined;
}
function onPointerUp(e: PointerEvent): void {
  if (!start || e.pointerId !== start.id) return;
  const dx = e.clientX - start.x;
  const dy = e.clientY - start.y;
  start = undefined;
  if (Math.abs(dx) < SWIPE_PX || Math.abs(dx) < Math.abs(dy) * 1.5) return;
  swiped = true;
  show(viewIndex.value + (dx < 0 ? 1 : -1));
}
function onPointerCancel(): void {
  start = undefined;
}
function onClickCapture(e: MouseEvent): void {
  if (!swiped) return;
  swiped = false;
  e.preventDefault();
  e.stopPropagation();
}

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
    'has-views': paging.value,
    clickable,
  },
  slide.value ? `slide-${slide.value}` : '',
]);

const label = computed(() => [shownName.value, context, tag, currentView.value?.label].filter(Boolean).join(', '));
const mark = computed(() => (tile.config.value?.name || boundName.value ? monogram(shownName.value) : (mono ?? monogram(name))));

// The cached icon, once the board has loaded the map; an image that fails
// to load falls back to the monogram.
const broken = ref('');
const icon = computed(() => {
  const own =
    status === 'attention' || status === 'down' || !wantsIcon(tile.config.value, shownHref.value, shownName.value)
      ? undefined
      : iconFor(iconMap.value, tile.config.value, shownHref.value, shownName.value);
  const src = own ?? serviceIconUrl(logo);
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
onBeforeUnmount(() => {
  stopScrollables?.();
  clearTimeout(slideTimer);
});

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
    @click.capture="onClickCapture"
    @keydown="onKey"
    @pointerdown="onPointerDown"
    @pointerup="onPointerUp"
    @pointercancel="onPointerCancel"
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
        <GlyphTip v-if="tagGlyph && glyphTip" :kind="tagGlyph" :text="glyphTip" :title="shownName" :href="tagHref" />
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
    <div v-if="paging && views" ref="dots" class="tile-views" role="group" :aria-label="`${shownName} views`" @keydown="onDotsKey">
      <button
        v-for="(v, i) in views.list"
        :key="v.id"
        type="button"
        class="tile-view-dot"
        :aria-label="v.label"
        :aria-pressed="i === viewIndex"
        :title="v.label"
        :tabindex="i === viewIndex ? 0 : -1"
        @click.stop="show(i)"
      ></button>
    </div>
  </article>
</template>
