<script setup lang="ts">
/**
 * Backup coverage: what in this house is backed up, and what's stale (the
 * `backups` topic; Settings → Backups has the whole map and its actions).
 *
 * Glyph and strip: what needs you, "2 stale · 1 not covered", or "all
 * fresh". Small: how many of the protectable things are fresh. Wide: that,
 * and the count in each state. Tall and large: the things that need you
 * first, each with why; large adds the counts.
 *
 * A household viewer gets the whole map once a board shows this widget
 * (boards.Scope.Backups).
 */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Glyph from '../ui/marks/Glyph.vue';
import type { TileStatus } from '../components/Tile.vue';
import { plural } from '../lib/format';
import { BACKUP_STATES, STATE_GLYPH, STATE_WORDS, counts, items, kindWord, lastWords, overBy } from '../model/backups';
import { glyphFor } from '../model/status';
import { useLive } from '../stores/live';
import Frame from './parts/Frame.vue';

const { size } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const doc = computed(() => live.backups.value);
const n = computed(() => counts(doc.value));
const total = computed(() => BACKUP_STATES.reduce((s, k) => s + n.value[k], 0));
const need = computed(() => n.value.stale + n.value.uncovered);
const status = computed<TileStatus>(() => (need.value ? 'attention' : 'ok'));
const words = computed(() => {
  const parts: string[] = [];
  if (n.value.stale) parts.push(`${n.value.stale} stale`);
  if (n.value.uncovered) parts.push(`${n.value.uncovered} not covered`);
  return parts.join(' · ');
});
const tag = computed(() => words.value);
/** What needs you first: stale, then not covered, then unknown. */
const rows = computed(() => {
  const order = (s: string) => BACKUP_STATES.indexOf(s as (typeof BACKUP_STATES)[number]);
  return items(doc.value)
    .filter((i) => i.state !== 'fresh')
    .sort((a, b) => order(a.state) - order(b.state));
});
const limit = computed(() => (size === 'large' ? 6 : size === 'tall' || size === 'column' ? 8 : 4));
const shown = computed(() => rows.value.slice(0, limit.value));
const more = computed(() => rows.value.length - shown.value.length);
const why = (i: (typeof rows.value)[number]) =>
  [i.through_name ? `through ${i.through_name}` : kindWord(i.kind), overBy(i, live.now.value) || lastWords(i, live.now.value)].join(' · ');
const context = computed(() => (total.value ? `${plural(total.value, 'thing')} to protect` : ''));
</script>

<template>
  <Frame
    :size="size"
    name="Backup coverage"
    mono="BK"
    :context="context"
    :status="doc && total ? status : 'ok'"
    :tag="tag"
    :tag-glyph="tag ? glyphFor(status) : undefined"
    :stale="live.stale.value"
  >
    <template v-if="!doc">
      <div class="v sm word">Coverage</div>
      <p v-if="size !== 'glyph' && size !== 'strip'" class="lab">The hub hasn’t sent the coverage map yet.</p>
    </template>

    <template v-else-if="!total">
      <div class="v sm word">Nothing yet</div>
      <p v-if="size !== 'glyph' && size !== 'strip'" class="lab">Guests, containers with volumes and Unraid shares show here once they’re connected.</p>
    </template>

    <template v-else-if="size === 'glyph' || size === 'strip'">
      <span class="sv n">{{ need || n.fresh }}<small>{{ need ? ' need you' : ' fresh' }}</small></span>
      <span v-if="size === 'strip'" class="sx">{{ words || 'all fresh' }}</span>
    </template>

    <template v-else-if="size === 'small'">
      <div class="v">{{ n.fresh }}<span class="u"> of {{ total }} fresh</span></div>
      <div class="tf">
        <span>{{ words || 'Everything is backed up and fresh.' }}</span>
      </div>
    </template>

    <template v-else>
      <div class="split">
        <div class="col">
          <div class="v">{{ n.fresh }}<span class="u"> of {{ total }} fresh</span></div>
          <div class="lab">{{ words || 'Everything is backed up and fresh.' }}</div>
        </div>
        <dl v-if="size === 'wide' || size === 'large'" class="kv">
          <template v-for="k in BACKUP_STATES" :key="k">
            <dt>{{ STATE_WORDS[k].title }}</dt>
            <dd>{{ n[k] }}</dd>
          </template>
        </dl>
      </div>
      <ul v-if="size !== 'wide' && shown.length" class="list" aria-label="Backups that need you">
        <li v-for="i in shown" :key="i.resource" class="li">
          <Glyph :kind="STATE_GLYPH[i.state]" :label="STATE_WORDS[i.state].short" />
          <span><span class="t">{{ i.name }}</span><small>{{ why(i) }}</small></span>
          <span></span>
        </li>
      </ul>
      <p v-else-if="size !== 'wide'" class="lab">Everything is backed up and fresh.</p>
      <div v-if="size !== 'wide' && more > 0" class="tf">
        <span>+{{ more }} more in Settings → Backups</span>
      </div>
    </template>
  </Frame>
</template>
