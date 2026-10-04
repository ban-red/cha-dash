<script setup lang="ts">
/**
 * <Tile> with the subject's state worked out: the frame treatment, the
 * header's state word, last-known values while the integration needs repair,
 * and a click that opens the incident (the problem first, else the repair).
 * Anything passed explicitly (status, tag) wins over the subject.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Tile, { type TileStatus } from '../../components/Tile.vue';
import type { GlyphKind } from '../../components/marks/Glyph.vue';
import { monogram } from '../../lib/format';
import type { Health, Incident } from '../../model/types';
import { useLive } from '../../stores/live';
import { useUi } from '../../stores/ui';
import { incidentOf, tagOf, type Subject } from '../subject';

const {
  size,
  name,
  mono,
  context = '',
  href,
  subject,
  status,
  tag,
  tagGlyph,
  okTag = '',
  stale = false,
  asOf,
  loading,
  incident,
} = defineProps<{
  size: SizeId;
  name: string;
  mono?: string | undefined;
  context?: string | undefined;
  href?: string | undefined;
  subject?: Subject | undefined;
  status?: TileStatus | undefined;
  tag?: string | undefined;
  tagGlyph?: GlyphKind | undefined;
  /** The state word while healthy ("idle", "WAN up"); quiet when empty. */
  okTag?: string;
  stale?: boolean;
  asOf?: string | undefined;
  /** Still loading something of its own (on top of the first snapshot). */
  loading?: boolean | undefined;
  incident?: Incident | undefined;
}>();

const live = useLive();
const ui = useUi();

const st = computed<TileStatus>(() => status ?? subject?.status ?? 'ok');
const header = computed(() => {
  if (tag !== undefined) return { tag, glyph: tagGlyph };
  const health: Health = subject?.health ?? { status: 'ok' };
  return tagOf({ status: st.value, health, resource: subject?.resource }, live.now.value, okTag);
});
const isStale = computed(() => stale || Boolean(subject?.needsRepair && st.value !== 'down'));
const target = computed(() => incident ?? (subject ? incidentOf(subject) : undefined));
/** Skeletons until the first snapshot arrives, or while the widget says it's still loading. */
const busy = computed(() => loading || live.loading.value);
</script>

<template>
  <Tile
    :size="size"
    :name="name"
    :mono="mono || monogram(name)"
    :context="context"
    :href="href"
    :status="st"
    :stale="isStale"
    :as-of="asOf"
    :tag="header.tag"
    :tag-glyph="header.glyph"
    :loading="busy"
    :clickable="Boolean(target)"
    @open="target && ui.openInbox(target.id)"
  >
    <slot></slot>
  </Tile>
</template>
