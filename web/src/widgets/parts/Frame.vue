<script setup lang="ts">
/**
 * <Tile> with the subject's state worked out: the frame treatment, the
 * header's state word, last-known values while the integration needs repair,
 * and a click that opens the incident (the problem first, else the repair).
 * Anything passed explicitly (status, tag) wins over the subject.
 *
 * While the Pulse strip is scrubbed, the frame and the state word are the
 * subject's health at that moment ("down at 03:15"), when the hub knows it.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Tile, { type TileStatus } from '../../components/Tile.vue';
import type { GlyphKind } from '../../components/marks/Glyph.vue';
import { hhmm, monogram } from '../../lib/format';
import { pastStatus, stateWords } from '../../model/history';
import { cursor, pulse } from '../../stores/cursor';
import type { Health, Incident } from '../../model/types';
import { useLive } from '../../stores/live';
import { useUi } from '../../stores/ui';
import { incidentOf, stateLine, tagOf, type Subject } from '../subject';
import type { TileViews } from '../views';

const {
  size,
  name,
  mono,
  logo,
  context = '',
  href,
  subject,
  status,
  tag,
  tagGlyph,
  tagNote,
  okTag = '',
  stale = false,
  asOf,
  loading,
  incident,
  views,
} = defineProps<{
  size: SizeId;
  name: string;
  mono?: string | undefined;
  /** A bundled logo slug for the tile's mark (lib/serviceIcons.ts). */
  logo?: string | undefined;
  context?: string | undefined;
  href?: string | undefined;
  subject?: Subject | undefined;
  status?: TileStatus | undefined;
  tag?: string | undefined;
  tagGlyph?: GlyphKind | undefined;
  /** The warning in full for the glyph's tooltip; else the subject's state line, else the tag. */
  tagNote?: string | undefined;
  /** The state word while healthy ("idle", "WAN up"); quiet when empty. */
  okTag?: string;
  stale?: boolean;
  asOf?: string | undefined;
  /** Still loading something of its own (on top of the first snapshot). */
  loading?: boolean | undefined;
  incident?: Incident | undefined;
  /** The widget's views (widgets/views.ts → useViews), paged by the tile. */
  views?: TileViews | undefined;
}>();

const live = useLive();
const ui = useUi();

/** The subject's health at the moment the Pulse strip is on. */
const past = computed(() => {
  const c = cursor.value;
  const id = subject?.resource?.id;
  if (c?.source !== 'pulse' || !id) return undefined;
  const was = pastStatus(pulse.value, id, c.at);
  return was ? { status: was, at: c.at } : undefined;
});
const PAST_GLYPH: Record<string, GlyphKind> = { ok: 'ok', attention: 'attn', down: 'down', repair: 'repair' };
const st = computed<TileStatus>(() => {
  const p = past.value?.status;
  if (p === 'down' || p === 'attention') return p;
  if (p) return 'ok'; // a repair stays quiet, as it does now
  return status ?? subject?.status ?? 'ok';
});
const header = computed(() => {
  const p = past.value;
  if (p) return { tag: `${stateWords(p.status).toLowerCase()} at ${hhmm(p.at)}`, glyph: PAST_GLYPH[p.status] };
  if (tag !== undefined) return { tag, glyph: tagGlyph };
  const health: Health = subject?.health ?? { status: 'ok' };
  return tagOf({ status: st.value, health, resource: subject?.resource }, live.now.value, okTag);
});
const line = computed(() => (subject ? stateLine(subject, live.now.value) : undefined));
const note = computed(() => tagNote || line.value?.text || header.value.tag);
/** The upstream's page for the subject's note, when the note is the subject's. */
const noteHref = computed(() => (tagNote ? undefined : line.value?.href));
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
    :logo="logo"
    :context="context"
    :href="href"
    :status="st"
    :stale="isStale"
    :as-of="asOf"
    :tag="header.tag"
    :tag-glyph="header.glyph"
    :tag-note="note"
    :tag-href="noteHref"
    :loading="busy"
    :views="views"
    :clickable="Boolean(target)"
    @open="target && ui.openInbox(target.id)"
  >
    <slot></slot>
  </Tile>
</template>
