<script setup lang="ts">
/**
 * Status glyphs: state is always shape + word + colour, never colour alone.
 * ▲ attention · ■ down · ○ ok/off · ◔ progress · ‖ maintenance · ◌ repair
 */
export type GlyphKind = 'attn' | 'down' | 'ok' | 'off' | 'prog' | 'maint' | 'repair' | 'link' | 'lock' | 'check' | 'cross';

const { kind, label = '' } = defineProps<{ kind: GlyphKind; label?: string }>();
</script>

<template>
  <span :class="['g', `g-${kind}`]" :role="label ? 'img' : undefined" :aria-label="label || undefined" :aria-hidden="label ? undefined : 'true'">
    <svg v-if="kind === 'attn'" viewBox="0 0 10 10"><path d="M5 .8 9.4 9H.6Z" fill="currentColor" /></svg>
    <svg v-else-if="kind === 'down'" viewBox="0 0 10 10"><rect x="1" y="1" width="8" height="8" rx="1" fill="currentColor" /></svg>
    <svg v-else-if="kind === 'ok' || kind === 'off'" viewBox="0 0 10 10"><circle cx="5" cy="5" r="3.8" fill="none" stroke="currentColor" stroke-width="1.4" /></svg>
    <svg v-else-if="kind === 'prog'" viewBox="0 0 10 10">
      <circle cx="5" cy="5" r="3.8" fill="none" stroke="currentColor" stroke-width="1.4" opacity=".32" />
      <path d="M5 1.2A3.8 3.8 0 0 1 8.8 5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
    </svg>
    <svg v-else-if="kind === 'maint'" viewBox="0 0 10 10">
      <rect x="2" y="1.5" width="2" height="7" rx=".6" fill="currentColor" />
      <rect x="6" y="1.5" width="2" height="7" rx=".6" fill="currentColor" />
    </svg>
    <svg v-else-if="kind === 'repair'" viewBox="0 0 10 10">
      <circle cx="5" cy="5" r="3.8" fill="none" stroke="currentColor" stroke-width="1.4" stroke-dasharray="1.6 1.4" />
    </svg>
    <svg v-else-if="kind === 'link'" viewBox="0 0 10 10">
      <path d="M3.5 6.5 8 2M4.5 2H8v3.5" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" />
    </svg>
    <svg v-else-if="kind === 'lock'" viewBox="0 0 10 10">
      <rect x="1.8" y="4.4" width="6.4" height="4.8" rx="1" fill="none" stroke="currentColor" stroke-width="1.3" />
      <path d="M3.3 4.4V3.2a1.7 1.7 0 0 1 3.4 0v1.2" fill="none" stroke="currentColor" stroke-width="1.3" />
    </svg>
    <svg v-else-if="kind === 'check'" viewBox="0 0 10 10">
      <path d="m2 5.2 2 2 4-4.4" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
    <svg v-else viewBox="0 0 10 10"><path d="m2.5 2.5 5 5m0-5-5 5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
  </span>
</template>
