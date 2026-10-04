<script setup lang="ts">
/**
 * Now playing (Plex). A broken integration is not an outage: when Plex
 * rejects the token the tile keeps last-known counts with "as of", says why,
 * and points at the Repair in the Inbox.
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Tile from '../components/Tile.vue';
import Glyph from '../components/marks/Glyph.vue';
import { fmtInt, plural } from '../lib/format';
import { attr, integrationOf, placeOf, resolve, series } from '../model/graph';
import { glyphFor, tileStatus } from '../model/status';
import { normalizeHealth, resourceLinks } from '../model/types';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';

const { size } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();
const ui = useUi();
const WANT = 'app:plex';

const app = computed(() => resolve(live.graph.value, WANT));
const ID = computed(() => app.value?.id ?? WANT);
const integration = computed(() => integrationOf(live.graph.value, app.value?.id) ?? live.graph.value?.resources['integration:plex']);
const streams = computed(() => series(live.metrics.value, ID.value, 'streams'));
const repair = computed(() => live.repairs.value.find((i) => i.target === (integration.value?.id ?? 'integration:plex')));
const needsRepair = computed(() => Boolean(repair.value) || normalizeHealth(integration.value).status === 'repair');
const health = computed(() => normalizeHealth(app.value));
const status = computed(() => tileStatus(health.value, app.value));
const n = computed(() => streams.value?.value ?? null);
const place = computed(() => placeOf(live.graph.value, ID.value));
const tag = computed(() => (status.value !== 'ok' ? health.value.reason : n.value ? plural(n.value, 'stream') : 'idle'));
</script>

<template>
  <Tile
    :size="size"
    name="Now playing"
    mono="PX"
    :context="place ? `Plex · ${place}` : 'Plex'"
    :href="resourceLinks(app)[0]?.url"
    :status="app ? status : 'unknown'"
    :stale="needsRepair || Boolean(streams?.stale)"
    :as-of="streams?.as_of"
    :tag="tag"
    :tag-glyph="app ? glyphFor(status) : undefined"
    :loading="live.loading.value"
    :clickable="Boolean(repair)"
    @open="repair && ui.openInbox(repair.id)"
  >
    <template v-if="!app">
      <div class="v sm word">Not connected</div>
      <p class="lab">Connect Plex to see what's playing.</p>
    </template>
    <template v-else-if="size === 'small'">
      <div v-if="n" class="v">{{ fmtInt(n) }}<span class="u">playing</span></div>
      <div v-else :class="['v', 'sm', 'word', { unknown: n === null }]">{{ n === null ? '—' : 'Nothing playing' }}</div>
      <div v-if="needsRepair" class="note"><Glyph kind="repair" label="repair needed" /><span>Token rejected</span></div>
      <div v-else class="lab"><span class="n">{{ fmtInt(attr<number>(app, 'movies')) }}</span> movies</div>
    </template>
    <div v-else class="split">
      <div class="col">
        <div v-if="n" class="v">{{ fmtInt(n) }}<span class="u">playing</span></div>
        <div v-else :class="['v', 'sm', 'word', { unknown: n === null }]">{{ n === null ? '—' : 'Nothing playing' }}</div>
        <div class="sub">{{ needsRepair ? 'last known · ' : '' }}{{ n ? 'active streams' : 'no active streams' }}</div>
      </div>
      <div class="col">
        <div class="sub"><b class="n">{{ fmtInt(attr<number>(app, 'movies')) }}</b> movies · <b class="n">{{ fmtInt(attr<number>(app, 'shows')) }}</b> shows</div>
        <div v-if="needsRepair" class="note">
          <Glyph kind="repair" label="repair needed" />
          <span>Plex rejected its token. Counts are last-known. <button type="button" @click.stop="ui.openInbox(repair?.id ?? '')">Reconnect</button></span>
        </div>
      </div>
    </div>
  </Tile>
</template>
