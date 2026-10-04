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
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph, { type GlyphKind } from './marks/Glyph.vue';
import { hhmm, monogram } from '../lib/format';
import { useLive } from '../stores/live';

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
  clickable?: boolean;
  loading?: boolean;
}>();

const emit = defineEmits<{ open: [] }>();
const live = useLive();

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

const label = computed(() => [name, context, tag].filter(Boolean).join(', '));
const mark = computed(() => mono ?? monogram(name));

function onKey(e: KeyboardEvent) {
  if (!clickable) return;
  if (e.key === 'Enter' || e.key === ' ') {
    e.preventDefault();
    emit('open');
  }
}
</script>

<template>
  <article
    :class="classes"
    :aria-label="label"
    :aria-busy="loading || undefined"
    :tabindex="clickable ? 0 : undefined"
    @click="clickable && emit('open')"
    @keydown="onKey"
  >
    <div class="th">
      <a v-if="href" class="mono" :href="href" target="_blank" rel="noopener noreferrer" :aria-label="`Open ${name}`" @click.stop>{{ mark }}</a>
      <span v-else class="mono" aria-hidden="true">{{ mark }}</span>
      <span class="tn">{{ name }}</span>
      <span v-if="context" class="tc">{{ context }}</span>
      <span v-if="isStale && staleSince" class="tg"><span class="asof" :title="'Last-known value'">as of {{ staleSince }}</span></span>
      <span v-else-if="tag || tagGlyph" class="tg">
        <Glyph v-if="tagGlyph" :kind="tagGlyph" />
        <span class="tgt">{{ tag }}</span>
      </span>
    </div>
    <template v-if="loading">
      <span class="skel"></span>
      <span class="skel"></span>
    </template>
    <slot v-else></slot>
  </article>
</template>
