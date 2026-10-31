<script setup lang="ts">
/**
 * Settings → General: how this device shows things. Every role can open it.
 *
 * Time format: 24-hour (14:02) or 12-hour (2:02 pm), for every time on the
 * page: the clock, "since" times, the Inbox, history and audit. It applies
 * as soon as it's picked and is remembered in this browser (lib/format.ts);
 * it isn't sent to the hub, so each device keeps its own.
 */
import { useId } from 'vue';
import { setTimeFormat, timeFormat, hhmm, type TimeFormat } from '../lib/format';

const name = useId();
const OPTIONS: { id: TimeFormat; label: string; sample: string }[] = [
  { id: '24h', label: '24-hour', sample: '14:05' },
  { id: '12h', label: '12-hour', sample: '2:05 pm' },
];
const now = new Date();
</script>

<template>
  <section class="st-section gen-time" aria-labelledby="gen-time-h">
    <header><h3 id="gen-time-h">Time format</h3></header>
    <p class="st-lede">Applies to every time on this device. Now: {{ hhmm(now) }}.</p>
    <div class="choice-row" role="radiogroup" aria-labelledby="gen-time-h">
      <label v-for="o in OPTIONS" :key="o.id">
        <input type="radio" :name="name" :value="o.id" :checked="timeFormat === o.id" @change="setTimeFormat(o.id)" />
        {{ o.label }}<span class="gen-sample">{{ o.sample }}</span>
      </label>
    </div>
  </section>
</template>

<style>
.gen-time .choice-row label {
  gap: var(--space-2);
  white-space: nowrap;
}
.gen-sample {
  font-family: var(--font-num);
  font-size: var(--type-small);
  font-variant-numeric: tabular-nums;
  color: var(--text-3, var(--text-2));
}
</style>
