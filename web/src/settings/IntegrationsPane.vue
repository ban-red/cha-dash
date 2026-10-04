<script setup lang="ts">
/**
 * Settings → Integrations: the instances Cha Dash reads, with their status and
 * warnings, and the catalog to add one from. The route picks the view:
 *
 *   /settings/integrations             the list
 *   /settings/integrations/new         the catalog
 *   /settings/integrations/new/{type}  the add form
 *   /settings/integrations/{id}        the edit form (?field=… focuses a field)
 */
import { computed, onMounted, ref } from 'vue';
import Glyph from '../components/marks/Glyph.vue';
import IntegrationEditor from './IntegrationEditor.vue';
import { ago, monogram } from '../lib/format';
import { navigate, route, settingsPath, view } from '../lib/router';
import { rank } from '../lib/fuzzy';
import { STATUS_WORDS, statusGlyph } from '../model/integrations';
import { catalogEntry, integrations, loadCatalog, loadInstances } from '../stores/integrations';
import { accessOf } from './recipe';
import { openWizard } from '../onboarding/state';

const v = computed(() => (view.value.name === 'settings' ? view.value : undefined));
const query = ref('');

const catalogShown = computed(() => rank(query.value, integrations.catalog.map((c) => ({ ...c, label: c.name, keywords: `${c.id} ${(c.capabilities ?? []).join(' ')}` }))));
const now = computed(() => Date.now());
const warnCount = (n: number) => `${n} warning${n === 1 ? '' : 's'}`;

/** The first-run wizard, again: over the front page. */
function openGuide(): void {
  openWizard();
  navigate('/', { replace: true });
}

onMounted(() => {
  void loadCatalog();
  void loadInstances(true);
});
</script>

<template>
  <!-- Edit or add -->
  <IntegrationEditor
    v-if="v?.id || v?.add"
    :key="(v.id ?? '') + '|' + (v.add ?? '')"
    :instance-ref="v.id"
    :integration-id="v.add || undefined"
    :focus="route.query['field']"
    :reason="route.query['reason']"
  />

  <!-- The catalog -->
  <section v-else-if="v?.add === ''" class="st-section" aria-labelledby="catalog-h">
    <a class="st-crumb" :href="settingsPath('integrations')" @click.prevent="navigate(settingsPath('integrations'), { replace: true })">‹ Integrations</a>
    <header><h3 id="catalog-h">Add an integration</h3><span class="st-meta">{{ integrations.catalog.length }} available</span></header>
    <p class="st-lede">Each integration reads one kind of service. Start read-only; Cha Dash shows what the credential can actually do when you test it.</p>
    <div class="field">
      <label for="catalog-q" class="sr">Filter integrations</label>
      <input id="catalog-q" v-model="query" type="search" placeholder="Filter: plex, proxmox, docker…" autocomplete="off" spellcheck="false" />
    </div>
    <div v-if="integrations.catalogStatus === 'loading'" class="st-skel" aria-busy="true"><span class="skel"></span><span class="skel"></span><span class="skel"></span></div>
    <div v-else-if="integrations.catalogStatus === 'error'" class="st-note bad" role="alert"><Glyph kind="down" /><span>{{ integrations.error }}</span></div>
    <ul v-else class="catalog">
      <li v-for="c in catalogShown" :key="c.id">
        <a class="cat-card" :href="`/settings/integrations/new/${encodeURIComponent(c.id)}`" @click.prevent="navigate(`/settings/integrations/new/${encodeURIComponent(c.id)}`)">
          <span class="mono-badge" aria-hidden="true">{{ monogram(c.name) }}</span>
          <span class="t">{{ c.name }}</span>
          <span class="m">{{ [accessOf(c.recipe)?.label ?? '', c.tier !== undefined ? `tier ${c.tier}` : '', c.version ? `v${c.version}` : ''].filter(Boolean).join(' · ') }}</span>
        </a>
      </li>
      <li v-if="!catalogShown.length" class="cat-none">Nothing matches “{{ query }}”.</li>
    </ul>
  </section>

  <!-- The list -->
  <template v-else>
    <section class="st-section" aria-labelledby="inst-h">
      <header>
        <h3 id="inst-h">Connected</h3>
        <button type="button" class="btn sm ghost" @click="openGuide">Setup guide</button>
        <a class="btn sm" :href="'/settings/integrations/new'" @click.prevent="navigate('/settings/integrations/new')">Add integration</a>
      </header>
      <div v-if="integrations.instancesStatus === 'loading' && !integrations.instances.length" class="st-skel" aria-busy="true">
        <span class="skel"></span><span class="skel"></span><span class="skel"></span>
      </div>
      <div v-else-if="integrations.instancesStatus === 'error' && !integrations.instances.length" class="st-note bad" role="alert">
        <Glyph kind="down" /><span>{{ integrations.error }}</span>
        <button type="button" class="btn sm" @click="loadInstances(true)">Try again</button>
      </div>
      <div v-else-if="!integrations.instances.length" class="st-empty">
        <div class="t">Nothing connected yet</div>
        <div class="m">Add an integration to read a service: Proxmox, Docker, Plex and the rest. Coming from Homepage? Import its config and every service comes along, secrets into the vault.</div>
        <div class="st-acts">
          <a class="btn primary" href="/settings/integrations/new" @click.prevent="navigate('/settings/integrations/new')">Add integration</a>
          <a class="btn" href="/settings/import" @click.prevent="navigate('/settings/import')">Import from Homepage</a>
        </div>
      </div>
      <ul v-else class="st-list">
        <li v-for="x in integrations.instances" :key="x.id">
          <a class="st-row" :href="settingsPath('integrations', x.id)" @click.prevent="navigate(settingsPath('integrations', x.id))">
            <span class="mono-badge" aria-hidden="true">{{ monogram(catalogEntry(x.integration)?.name ?? x.name) }}</span>
            <span>
              <span class="t">{{ x.name }}</span>
              <span class="m">
                {{ catalogEntry(x.integration)?.name ?? x.integration }}{{ x.version ? ` · ${x.version}` : '' }}
                <template v-if="x.status !== 'ok' && x.last_error"> · {{ x.last_error }}</template>
                <template v-else-if="x.last_ok"> · read {{ ago(x.last_ok, now) }}</template>
              </span>
            </span>
            <span class="end">
              <span v-if="x.warnings?.length" class="st-status"><Glyph kind="attn" />{{ warnCount(x.warnings.length) }}</span>
              <span class="st-status"><Glyph :kind="statusGlyph(x.status)" :label="STATUS_WORDS[x.status] ?? x.status" /><span aria-hidden="true">{{ STATUS_WORDS[x.status] ?? x.status }}</span></span>
              <span aria-hidden="true">›</span>
            </span>
          </a>
        </li>
      </ul>
    </section>
  </template>
</template>

<style>
.catalog {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(176px, 1fr));
  gap: var(--space-2);
}
.cat-card {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  grid-template-rows: auto auto;
  column-gap: var(--space-3);
  align-items: center;
  padding: var(--space-3);
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-md);
  background: var(--surface);
  color: inherit;
  text-decoration: none;
  transition:
    background-color var(--motion-press-duration) var(--motion-press-easing),
    transform var(--motion-press-duration) var(--motion-press-easing);
}
.cat-card:hover {
  background: var(--surface-hover);
}
.cat-card:hover .m {
  color: var(--text-2); /* text-3 is too faint on a hover row (WCAG AA) */
}
.cat-card:active {
  transform: scale(0.98);
}
.cat-card .mono-badge {
  grid-row: 1 / span 2;
}
.cat-card .t {
  font: var(--weight-medium) var(--type-body) / 1.2 var(--font-ui);
}
.cat-card .m {
  font: var(--weight-regular) var(--type-caption) / 1.3 var(--font-num);
  color: var(--text-3);
}
.cat-none {
  grid-column: 1 / -1;
  padding: var(--space-4) 0;
  font-size: var(--type-small);
  color: var(--text-3);
}
</style>
