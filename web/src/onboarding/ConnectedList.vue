<script setup lang="ts">
/**
 * What's connected so far, with each instance's status in words: "Reading"
 * once its first sync worked, "Needs repair" with a way to fix it.
 */
import Glyph from '../ui/marks/Glyph.vue';
import { navigate, settingsPath } from '../lib/router';
import { STATUS_WORDS, statusGlyph } from '../model/integrations';
import { catalogEntry, integrations } from '../stores/integrations';
import UiButton from '../ui/Button.vue';
</script>

<template>
  <section v-if="integrations.instances.length" class="ob-connected" aria-labelledby="ob-connected-h">
    <h3 id="ob-connected-h" class="caps">Connected so far · {{ integrations.instances.length }}</h3>
    <ul>
      <li v-for="x in integrations.instances" :key="x.id">
        <span class="ob-c-name">
          <b>{{ x.name }}</b>
          <span class="ob-c-kind">{{ catalogEntry(x.integration)?.name ?? x.integration }}</span>
        </span>
        <span class="ob-c-status">
          <Glyph :kind="statusGlyph(x.status)" :label="STATUS_WORDS[x.status] ?? x.status" />
          <span aria-hidden="true">{{ STATUS_WORDS[x.status] ?? x.status }}</span>
          <UiButton v-if="x.status === 'repair' || x.status === 'error' || x.status === 'degraded'" size="sm" :href="settingsPath('integrations', x.id)" @click.prevent="navigate(settingsPath('integrations', x.id))">Fix</UiButton>
        </span>
      </li>
    </ul>
  </section>
</template>
