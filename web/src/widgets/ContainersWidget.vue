<script setup lang="ts">
/** Containers across Docker engines: how many run, which aren't healthy. */
import { computed } from 'vue';
import type { SizeId } from '@chadash/layout';
import Tile from '../components/Tile.vue';
import Glyph from '../components/marks/Glyph.vue';
import { plural } from '../lib/format';
import { engines, label } from '../model/graph';
import { glyphFor, tileStatus, worst } from '../model/status';
import { normalizeHealth } from '../model/types';
import { useLive } from '../stores/live';

const { size } = defineProps<{ size: SizeId; target?: string | undefined }>();
const live = useLive();

const list = computed(() => engines(live.graph.value));
const running = computed(() => list.value.reduce((n, e) => n + e.running, 0));
const total = computed(() => list.value.reduce((n, e) => n + e.total, 0));
const unhealthy = computed(() => list.value.flatMap((e) => e.unhealthy));
const status = computed(() => worst(unhealthy.value.map((c) => tileStatus(normalizeHealth(c), c))));
const tag = computed(() => (unhealthy.value.length ? `${unhealthy.value.length} not healthy` : total.value ? 'all healthy' : ''));
</script>

<template>
  <Tile
    :size="size"
    name="Containers"
    mono="DK"
    :context="list.length ? plural(list.length, 'engine') : ''"
    :status="status"
    :tag="tag"
    :tag-glyph="total ? glyphFor(status) : undefined"
    :loading="live.loading.value"
  >
    <template v-if="!list.length">
      <div class="v sm word">No Docker engines</div>
      <p class="lab">Point Cha Dash at a socket proxy to see containers.</p>
    </template>
    <template v-else>
      <div class="row">
        <div class="v">{{ running }}<span class="u">/{{ total }} running</span></div>
      </div>
      <ul v-if="size !== 'small' && unhealthy.length" class="list" aria-label="Containers that need attention">
        <li v-for="c in unhealthy" :key="c.id" class="li">
          <Glyph :kind="glyphFor(normalizeHealth(c).status)" />
          <span><span class="t">{{ label(c) }}</span><small>{{ normalizeHealth(c).reason }}</small></span>
          <span></span>
        </li>
      </ul>
      <div v-if="size === 'large'" class="col">
        <template v-for="e in list" :key="e.id">
          <div class="ag-day">{{ e.name }} · {{ e.running }}/{{ e.total }}</div>
          <div class="names">
            <span v-for="c in e.containers" :key="c.id" :class="{ attn: normalizeHealth(c).status !== 'ok' }">{{ label(c) }}</span>
          </div>
        </template>
      </div>
      <div class="tf">
        <span>
          <template v-for="(e, i) in list" :key="e.id">{{ i ? ' · ' : '' }}{{ e.name }} <span class="n">{{ e.total }}</span></template>
        </span>
      </div>
    </template>
  </Tile>
</template>
