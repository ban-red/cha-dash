<script setup lang="ts">
/**
 * "Where Cha Dash runs", in Settings → Security: the chain the hub runs on,
 * read from the live Home Graph (model/hub.ts), e.g. container chadash →
 * LXC rook → host egret, network through router opnsense. Anything that would
 * take one of these down takes Cha Dash down too, so such an action's
 * confirmation says so ("Cha Dash itself"), and stopping or rebooting them
 * is the self-aware T4.
 */
import { computed } from 'vue';
import Glyph from '../ui/marks/Glyph.vue';
import { hubPlace, placeWords } from '../model/hub';
import { useLive } from '../stores/live';

const live = useLive();
const place = computed(() => hubPlace(live.graph.value));
</script>

<template>
  <section class="st-section hub-place" aria-labelledby="hp-h">
    <header><h3 id="hp-h">Where Cha Dash runs</h3></header>
    <template v-if="place.chain.length">
      <ol class="hp-chain" aria-label="What Cha Dash runs on, nearest first">
        <li class="hp-self"><span class="hp-pill">Cha Dash</span></li>
        <li v-for="r in place.chain" :key="r.id"><span class="hp-arrow" aria-hidden="true">→</span><span class="hp-pill">{{ placeWords(r) }}</span></li>
        <li v-if="place.router"><span class="hp-arrow" aria-hidden="true">·</span><span class="hp-pill">network through router {{ place.router.name || place.router.id }}</span></li>
      </ol>
      <p class="st-lede">
        Stopping or rebooting any of these takes Cha Dash down with it. Their confirmations say so, and rebooting the host or the router needs the
        owner to allow it first.
      </p>
    </template>
    <div v-else class="st-note" role="note">
      <Glyph kind="link" />
      <span>
        Cha Dash can’t tell where it runs yet. Read this host’s Docker through chadash-node (or a socket proxy), or set Proxmox’s “Cha Dash’s own guest”, and
        the chain shows here.
        <template v-if="place.router"> Your network runs through router {{ place.router.name || place.router.id }}.</template>
      </span>
    </div>
  </section>
</template>

<style>
.hp-chain {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
  margin: 0;
  padding: 0;
  list-style: none;
  color: var(--text);
}
.hp-chain li {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
}
.hp-pill {
  padding: 2px 10px;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-full);
  font-size: var(--type-small);
}
.hp-arrow {
  color: var(--text-2);
}
.hp-self .hp-pill {
  font-weight: var(--weight-medium);
}
</style>
