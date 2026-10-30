<script setup lang="ts">
/**
 * Add a widget, in edit mode (its own chunk, loaded on the first "Add").
 *
 * Left, the catalog (model/widgets.ts) read generically: search, groups, and
 * a card per widget with what it shows and the sizes that fit the section.
 * Ready ones are what the Home Graph can feed (model/feeds.ts) and the board
 * doesn't show yet; the rest fold away under "Not available here", each
 * saying why. Right, the chosen widget as it will look: the real widget,
 * live, at the chosen size and in proportion (inert, so it's only a
 * picture), then its size, what it shows when it's bound to one resource
 * (a filterable list: metrics run to dozens) or, for the generic service
 * tile, a name, a link and what gives it a status.
 *
 * Keys: type to search (names, what a widget shows, and what it can be
 * bound to); ↓ from the search box, then ↑↓ through the cards
 * (the preview follows); Enter in the search picks the top match; ⌘/Ctrl
 * Enter, or a double-click on a card, adds. It only describes the widget:
 * EditableBoard places it (model/edit.ts) in the first gap that fits,
 * records it in the history and saves it.
 */
import { computed, nextTick, onMounted, ref, useId, useTemplateRef, watch } from 'vue';
import { SIZES, type Board, type SizeId } from '@chadash/layout';
import { WIDGET_GROUPS, WIDGETS, itemId, type WidgetGroup, type WidgetSpec } from '../model/widgets';
import { bindKind, candidateLabel, decodeBinding, feedFor, serviceBindingGroups, type BindKind } from '../model/feeds';
import { newItemId, sizeName, sizesFor, type AddRequest } from '../model/edit';
import type { Resource } from '../model/types';
import { monogram, plural } from '../lib/format';
import { rank } from '../lib/fuzzy';
import { serviceIconUrl } from '../lib/serviceIcons';
import { useLive } from '../stores/live';
import { hrefOk, type TileConfig, type TilesDoc } from '../stores/tiles';
import WidgetHost from '../widgets/WidgetHost.vue';
import { preloadWidgets } from '../widgets/registry';
import { useModal } from './useModal';
import './editDialog.css';

const { board, tiles, sectionId: initialSection } = defineProps<{ board: Board; tiles: TilesDoc; sectionId?: string | undefined }>();
const emit = defineEmits<{ add: [req: AddRequest]; close: [] }>();

const live = useLive();
const uid = useId();
const dlg = useTemplateRef<HTMLDialogElement>('dlg');
const form = useTemplateRef<HTMLFormElement>('form');
const search = useTemplateRef<HTMLInputElement>('search');
const list = useTemplateRef<HTMLElement>('list');
useModal(dlg);
// The widgets a board only has once someone adds them load on demand; fetch them now so previews don't flicker.
onMounted(() => void preloadWidgets());

const sectionId = ref(initialSection && board.sections.some((s) => s.id === initialSection) ? initialSection : (board.sections[0]?.id ?? ''));
const section = computed(() => board.sections.find((s) => s.id === sectionId.value));
const sectionTitle = (s: { id: string; title?: string | undefined } | undefined) => s?.title ?? s?.id ?? '';

// ── the catalog, as this section and this graph see it ─────────────────

interface Entry {
  spec: WidgetSpec;
  kind: BindKind;
  /** Resources it can still show here (bound widgets). */
  free: Resource[];
  /** Resources it could show that the board shows already. */
  shown: Resource[];
  fits: SizeId[];
  ready: boolean;
  /** Why it isn't ready, or (ready) what to choose and in which sizes. */
  status: string;
  mark: { icon?: string; text: string };
  label: string;
  keywords: string;
}

const sizesWords = (sizes: SizeId[]) => sizes.map((s) => sizeName(s)).join(' · ');
const groupName = (g: WidgetGroup) => WIDGET_GROUPS.find((x) => x.id === g)?.name ?? g;

const entries = computed<Entry[]>(() => {
  const g = live.graph.value;
  const state = { board, tiles };
  const span = section.value?.span ?? 12;
  return Object.values(WIDGETS).map((spec): Entry => {
    const kind = bindKind(spec);
    const feed = feedFor(spec, g, live.now.value);
    const fits = sizesFor(spec, span);
    const free: Resource[] = [];
    const shown: Resource[] = [];
    if (kind === 'resource') for (const r of feed.candidates) (newItemId(state, spec, r.id) ? free : shown).push(r);
    let status: string;
    let ready = false;
    if (!feed.available) status = 'Nothing on your Home Graph feeds it yet';
    else if (kind === 'none' && !newItemId(state, spec)) status = 'Already on this board';
    else if (kind === 'resource' && !free.length) status = 'Every one is on this board already';
    else if (!fits.length) status = `Too wide for ${sectionTitle(section.value)}`;
    else {
      ready = true;
      status =
        kind === 'resource'
          ? `${free.length === 1 ? candidateLabel(g, free[0]!) : `${free.length} to choose from`} · ${sizesWords(fits)}`
          : kind === 'service'
            ? `A name, a link and a status · ${sizesWords(fits)}`
            : sizesWords(fits);
    }
    const icon = serviceIconUrl(spec.icon);
    const types = spec.accepts?.types?.join(' ') ?? '';
    return {
      spec,
      kind,
      free,
      shown,
      fits,
      ready,
      status,
      mark: icon ? { icon, text: monogram(spec.name) } : { text: monogram(spec.name) },
      label: spec.name,
      // What it can show counts too: "cpu" finds Metric, "sonarr" the Queue.
      keywords: `${spec.about} ${groupName(spec.group)} ${spec.type} ${types} ${free.map((r) => candidateLabel(g, r)).join(' ')}`,
    };
  });
});

// ── search and groups ───────────────────────────────────────────────────

const query = ref('');
const group = ref<WidgetGroup | 'all'>('all');
const matching = computed(() => rank(query.value, entries.value));
const inGroup = (e: Entry) => group.value === 'all' || e.spec.group === group.value;
const ready = computed(() => matching.value.filter((e) => e.ready && inGroup(e)));
const waiting = computed(() => matching.value.filter((e) => !e.ready && inGroup(e)));
const searching = computed(() => query.value.trim() !== '');
const counts = computed(() => {
  const c: Record<string, number> = { all: 0 };
  for (const e of matching.value) {
    if (!e.ready) continue;
    c.all!++;
    c[e.spec.group] = (c[e.spec.group] ?? 0) + 1;
  }
  return c;
});
const groups = computed(() => [{ id: 'all' as const, name: 'All' }, ...WIDGET_GROUPS.filter((g) => entries.value.some((e) => e.spec.group === g.id))]);
/** Without a search, the ready cards under their group's name, in catalog order. */
const sections = computed(() => {
  if (searching.value || group.value !== 'all') return [{ id: 'results', name: '', items: ready.value }];
  return WIDGET_GROUPS.map((g) => ({ id: g.id, name: g.name, items: ready.value.filter((e) => e.spec.group === g.id) })).filter((s) => s.items.length);
});

// ── the chosen widget ───────────────────────────────────────────────────

const chosen = ref('');
const entry = computed(() => entries.value.find((e) => e.ready && e.spec.type === chosen.value));
const size = ref<SizeId | ''>('');
const target = ref('');
const targetQuery = ref('');
const name = ref('');
const href = ref('');
const binding = ref('');
const bindingGroups = computed(() => serviceBindingGroups(live.graph.value));

// Something to look at from the start: the first ready widget.
if (ready.value[0]) chosen.value = ready.value[0].spec.type;

watch(
  () => [entry.value?.spec.type, entry.value?.fits.join(), entry.value?.free.map((r) => r.id).join()] as const,
  () => {
    const e = entry.value;
    if (!e) return;
    if (!size.value || !e.fits.includes(size.value)) size.value = e.fits.includes(e.spec.size) ? e.spec.size : (e.fits[0] ?? '');
    if (e.kind === 'resource' && !e.free.some((r) => r.id === target.value)) target.value = e.free[0]?.id ?? '';
  },
  { immediate: true },
);

function choose(type: string): void {
  if (chosen.value === type) return;
  chosen.value = type;
  size.value = '';
  target.value = '';
  targetQuery.value = '';
  name.value = '';
  href.value = '';
  binding.value = '';
}

/**
 * Candidates for a bound widget: the free ones, then the ones the board
 * shows, disabled. The filter wants every word as typed (not the command
 * bar's letters-in-order), so "cpu" doesn't also find "backups used".
 */
const targets = computed(() => {
  const e = entry.value;
  if (!e || e.kind !== 'resource') return [];
  const g = live.graph.value;
  const words = targetQuery.value.toLowerCase().split(/\s+/).filter(Boolean);
  const row = (r: Resource, taken: boolean) => {
    const full = candidateLabel(g, r);
    const at = full.lastIndexOf(' · on ');
    return { id: r.id, taken, main: at < 0 ? full : full.slice(0, at), place: at < 0 ? '' : full.slice(at + 3), text: `${full} ${r.id} ${r.kind}`.toLowerCase() };
  };
  return [...e.free.map((r) => row(r, false)), ...e.shown.map((r) => row(r, true))].filter((r) => words.every((w) => r.text.includes(w)));
});
const manyTargets = computed(() => (entry.value?.free.length ?? 0) + (entry.value?.shown.length ?? 0) > 6);

const hrefBad = computed(() => href.value.trim() !== '' && !hrefOk(href.value.trim()));
const canAdd = computed(() => {
  const e = entry.value;
  if (!e || !size.value || !sectionId.value) return false;
  if (e.kind === 'resource' && !target.value) return false;
  if (e.kind === 'service' && (!name.value.trim() || hrefBad.value)) return false;
  return true;
});

function serviceConfig(): TileConfig {
  const config: TileConfig = { name: name.value.trim() };
  if (href.value.trim()) config.href = href.value.trim();
  const bind = decodeBinding(binding.value);
  if (bind) config.bind = bind;
  return config;
}

// ── the preview ─────────────────────────────────────────────────────────

const preview = computed(() => {
  const e = entry.value;
  if (!e || !size.value) return undefined;
  const s = SIZES[size.value];
  const id = e.kind === 'resource' ? itemId(e.spec.type, target.value || undefined) : e.kind === 'service' ? 'service@tile:preview' : e.spec.type;
  const config = e.kind === 'service' ? { ...serviceConfig(), name: name.value.trim() || 'Service' } : undefined;
  return { item: { id, type: e.spec.type }, size: size.value, config, w: s.w, h: s.h };
});

// ── keys ────────────────────────────────────────────────────────────────

const cards = () => [...(list.value?.querySelectorAll<HTMLButtonElement>('button.pk-item') ?? [])];

function step(by: number, from?: EventTarget | null): void {
  const all = cards();
  if (!all.length) return;
  const at = all.indexOf(from as HTMLButtonElement);
  const next = all[at < 0 ? (by > 0 ? 0 : all.length - 1) : Math.min(all.length - 1, Math.max(0, at + by))]!;
  next.focus();
  choose(next.dataset.type ?? '');
  next.scrollIntoView?.({ block: 'nearest' });
}

function onSearchKey(ev: KeyboardEvent): void {
  if (ev.key === 'ArrowDown') {
    ev.preventDefault();
    step(1);
  } else if (ev.key === 'Enter' && !ev.metaKey && !ev.ctrlKey) {
    ev.preventDefault(); // not a submit: pick the top match
    const top = ready.value[0];
    if (top) choose(top.spec.type);
  }
}

function onListKey(ev: KeyboardEvent): void {
  if (ev.key === 'ArrowDown' || ev.key === 'ArrowUp') {
    ev.preventDefault();
    step(ev.key === 'ArrowDown' ? 1 : -1, ev.target);
  } else if (ev.key === 'Home' || ev.key === 'End') {
    ev.preventDefault();
    step(ev.key === 'Home' ? -Infinity : Infinity, cards()[0]);
  }
}

function onDialogKey(ev: KeyboardEvent): void {
  if (ev.key === 'Enter' && (ev.metaKey || ev.ctrlKey)) {
    ev.preventDefault();
    form.value?.requestSubmit?.();
  } else if (ev.key === '/' && !(ev.target instanceof HTMLInputElement || ev.target instanceof HTMLSelectElement)) {
    ev.preventDefault();
    search.value?.focus();
  }
}

function clearSearch(): void {
  query.value = '';
  search.value?.focus();
}

function submit(): void {
  const e = entry.value;
  if (!e || !canAdd.value || !size.value) return;
  const req: AddRequest = { spec: e.spec, sectionId: sectionId.value, size: size.value };
  if (e.kind === 'resource') req.target = target.value;
  if (e.kind === 'service') req.config = serviceConfig();
  emit('add', req);
}

async function quickAdd(type: string): Promise<void> {
  choose(type);
  await nextTick(); // its size and target settle first
  if (canAdd.value) submit();
}

const footprint = (s: SizeId) => ({ '--fw': SIZES[s].w, '--fh': SIZES[s].h });
</script>

<template>
  <dialog ref="dlg" class="edit-dlg picker" :aria-labelledby="`${uid}-h`" @cancel.prevent="emit('close')" @keydown="onDialogKey">
    <form ref="form" class="edit-dlg-body" novalidate @submit.prevent="submit">
      <header class="edit-dlg-head">
        <h2 :id="`${uid}-h`">Add a widget</h2>
        <label class="pk-to">
          <span>to</span>
          <select v-model="sectionId" aria-label="Section">
            <option v-for="s in board.sections" :key="s.id" :value="s.id">{{ sectionTitle(s) }}</option>
          </select>
        </label>
        <button type="button" class="x" aria-label="Close" @click="emit('close')">
          <svg viewBox="0 0 10 10" aria-hidden="true"><path d="m2.5 2.5 5 5m0-5-5 5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
        </button>
      </header>

      <div class="pk-cols">
        <div class="pk-browse">
          <div class="pk-search">
            <svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="7" cy="7" r="4.5" fill="none" stroke="currentColor" stroke-width="1.5" /><path d="m10.5 10.5 3 3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
            <input
              ref="search"
              v-model="query"
              type="search"
              autofocus
              autocomplete="off"
              spellcheck="false"
              aria-label="Search widgets"
              placeholder="Search widgets"
              :aria-controls="`${uid}-list`"
              @keydown="onSearchKey"
            />
            <kbd v-if="!query" class="kbd" aria-hidden="true">/</kbd>
          </div>
          <div class="pk-groups" role="group" aria-label="Groups">
            <button v-for="g in groups" :key="g.id" type="button" class="pk-chip" :aria-pressed="group === g.id" @click="group = g.id">
              {{ g.name }}<span class="n" aria-hidden="true">{{ counts[g.id] ?? 0 }}</span>
            </button>
          </div>

          <div :id="`${uid}-list`" ref="list" class="pk-list" role="group" aria-label="Widgets" @keydown="onListKey">
            <template v-for="sec in sections" :key="sec.id">
              <h3 v-if="sec.name" class="pk-group-h caps">{{ sec.name }}</h3>
              <button
                v-for="e in sec.items"
                :key="e.spec.type"
                type="button"
                class="pk-item"
                :aria-pressed="chosen === e.spec.type"
                :data-type="e.spec.type"
                @click="choose(e.spec.type)"
                @dblclick="quickAdd(e.spec.type)"
              >
                <span class="pk-mark" aria-hidden="true">
                  <img v-if="e.mark.icon" :src="e.mark.icon" alt="" width="20" height="20" />
                  <template v-else>{{ e.mark.text }}</template>
                </span>
                <span class="pk-text">
                  <span class="pk-name">{{ e.spec.name }}</span>
                  <span class="pk-about">{{ e.spec.about }}</span>
                  <span class="pk-meta">
                    <span class="pk-feet" aria-hidden="true">
                      <span v-for="s in e.fits" :key="s" class="pk-foot" :style="footprint(s)" />
                    </span>
                    <span class="sr">{{ e.status }}</span>
                    <span v-if="e.kind === 'resource' && e.free.length > 1" class="pk-count" aria-hidden="true">{{ e.free.length }} to choose from</span>
                  </span>
                </span>
              </button>
            </template>
            <div v-if="!ready.length" class="pk-empty">
              <template v-if="searching">
                <p>No widget that fits here matches “{{ query.trim() }}”.</p>
                <button type="button" class="btn ghost" @click="clearSearch">Clear the search</button>
              </template>
              <p v-else>Nothing more fits {{ sectionTitle(section) }} from what the Home Graph has right now.</p>
            </div>
            <details v-if="waiting.length" class="pk-waiting">
              <summary>Not available here · {{ waiting.length }}</summary>
              <div v-for="e in waiting" :key="e.spec.type" class="pk-item off" :data-type="e.spec.type">
                <span class="pk-mark" aria-hidden="true">
                  <img v-if="e.mark.icon" :src="e.mark.icon" alt="" width="20" height="20" />
                  <template v-else>{{ e.mark.text }}</template>
                </span>
                <span class="pk-text">
                  <span class="pk-name">{{ e.spec.name }}</span>
                  <span class="pk-sub">{{ e.status }}</span>
                </span>
              </div>
            </details>
          </div>
        </div>

        <div v-if="entry" class="pk-detail" aria-live="polite">
          <div class="pk-detail-head">
            <h3 class="pk-title">{{ entry.spec.name }}</h3>
            <p class="pk-lede">{{ entry.spec.about }}</p>
          </div>

          <figure class="pk-stage">
            <div v-if="preview" class="pk-preview" inert :style="{ '--w': preview.w, '--h': preview.h }">
              <WidgetHost :item="preview.item" :size="preview.size" :preview="preview.config" />
            </div>
            <figcaption>Live from your Home Graph · {{ size ? sizeName(size) : '' }}</figcaption>
          </figure>

          <fieldset class="pk-sizes">
            <legend>Size</legend>
            <div class="pk-size-row">
              <label v-for="s in entry.fits" :key="s" class="pk-size">
                <input v-model="size" type="radio" :name="`${uid}-size`" :value="s" />
                <span class="pk-foot lg" :style="footprint(s)" aria-hidden="true" />
                <span class="pk-size-name">{{ SIZES[s].name }}</span>
                <span class="pk-size-dim n">{{ SIZES[s].w }}×{{ SIZES[s].h }}</span>
              </label>
            </div>
          </fieldset>

          <fieldset v-if="entry.kind === 'resource'" class="pk-targets">
            <legend>Shows</legend>
            <input
              v-if="manyTargets"
              v-model="targetQuery"
              class="pk-target-q"
              type="search"
              autocomplete="off"
              spellcheck="false"
              :aria-label="`Search what ${entry.spec.name} can show`"
              :placeholder="`Search ${plural(entry.free.length + entry.shown.length, 'choice')}`"
            />
            <div class="pk-target-list">
              <label v-for="t in targets" :key="t.id" class="pk-target" :class="{ taken: t.taken }">
                <input v-model="target" type="radio" :name="`${uid}-target`" :value="t.id" :disabled="t.taken" />
                <span class="pk-target-main">{{ t.main }}</span>
                <span v-if="t.place" class="pk-target-place">{{ t.place }}</span>
                <span v-if="t.taken" class="pk-target-taken">On the board</span>
              </label>
              <p v-if="!targets.length" class="pk-hint">Nothing matches “{{ targetQuery.trim() }}”.</p>
            </div>
          </fieldset>

          <template v-if="entry.kind === 'service'">
            <div class="field">
              <label :for="`${uid}-name`">Name</label>
              <input :id="`${uid}-name`" v-model="name" type="text" autocomplete="off" maxlength="200" required placeholder="Router admin" />
            </div>
            <div class="field">
              <label :for="`${uid}-href`">Link</label>
              <input :id="`${uid}-href`" v-model="href" type="url" inputmode="url" autocomplete="off" placeholder="https://" :aria-invalid="hrefBad || undefined" />
              <span v-if="hrefBad" class="help bad">Links start with http:// or https://.</span>
            </div>
            <div class="field">
              <label :for="`${uid}-bind`">Status from</label>
              <select :id="`${uid}-bind`" v-model="binding">
                <option value="">Nothing (no status)</option>
                <optgroup v-for="gr in bindingGroups" :key="gr.label" :label="gr.label">
                  <option v-for="c in gr.choices" :key="c.value" :value="c.value">{{ c.label }}</option>
                </optgroup>
              </select>
            </div>
          </template>
        </div>
        <div v-else class="pk-detail pk-detail-empty">
          <p class="pk-hint">Pick a widget to see it here, live, before it goes on the board.</p>
        </div>
      </div>

      <footer class="edit-dlg-foot pk-foot-bar">
        <p class="pk-where">Goes into the first gap in {{ sectionTitle(section) }} where it fits; drag it anywhere after.</p>
        <span class="pk-keys" aria-hidden="true"><kbd class="kbd">⌘</kbd><kbd class="kbd">↵</kbd></span>
        <button type="button" class="btn ghost" @click="emit('close')">Cancel</button>
        <button type="submit" class="btn primary" :disabled="!canAdd">Add to {{ sectionTitle(section) }}</button>
      </footer>
    </form>
  </dialog>
</template>

<style>
.edit-dlg.picker {
  width: min(1040px, calc(100% - 32px));
  overflow: hidden;
}
.picker .edit-dlg-body {
  max-height: calc(100dvh - 64px);
  padding-bottom: var(--space-4);
}
.pk-to {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--type-small);
  color: var(--text-3);
}

/* Two panes: browse on the left, the chosen widget on the right. */
.pk-cols {
  display: grid;
  grid-template-columns: minmax(300px, 380px) minmax(0, 1fr);
  gap: var(--space-5);
  min-height: 0;
  flex: 1;
}
.pk-browse {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  min-height: 0;
}

.pk-search {
  position: relative;
  display: flex;
  align-items: center;
}
.pk-search svg {
  position: absolute;
  left: 10px;
  width: 14px;
  height: 14px;
  color: var(--text-3);
  pointer-events: none;
}
.pk-search input {
  width: 100%;
  min-height: var(--target-min);
  padding: 8px 34px 8px 32px;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-md);
  background: var(--surface-2);
  color: var(--text);
}
.pk-search input::placeholder {
  color: var(--text-3);
}
.pk-search .kbd {
  position: absolute;
  right: 8px;
}

.pk-groups {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.pk-chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  min-height: 26px;
  padding: 3px 9px;
  border: var(--hairline) solid var(--line-2);
  border-radius: 999px;
  background: none;
  color: var(--text-2);
  font-size: var(--type-small);
  transition: background-color var(--motion-fade-duration) var(--motion-fade-easing);
}
.pk-chip .n {
  font-size: var(--type-caption);
  color: var(--text-3);
}
.pk-chip:hover {
  background: color-mix(in oklch, var(--text) 5%, transparent);
}
.pk-chip[aria-pressed='true'] {
  border-color: var(--text-2);
  background: var(--text);
  color: var(--bg);
}
.pk-chip[aria-pressed='true'] .n {
  color: color-mix(in oklch, var(--bg) 70%, transparent);
}

.pk-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-height: 0;
  max-height: min(56vh, 540px);
  margin: 0 calc(-1 * var(--space-2));
  padding: 0 var(--space-2) var(--space-2);
  overflow: auto;
  overscroll-behavior: contain;
}
.pk-group-h {
  margin: var(--space-3) var(--space-3) var(--space-1);
  font: var(--weight-medium) var(--type-micro) / 1 var(--font-num);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-3);
}
.pk-group-h:first-child {
  margin-top: var(--space-1);
}
.pk-item {
  display: flex;
  align-items: flex-start;
  gap: var(--space-3);
  padding: 10px var(--space-3);
  border: var(--hairline) solid transparent;
  border-radius: var(--radius-lg);
  background: none;
  color: inherit;
  text-align: left;
  transition: background-color var(--motion-fade-duration) var(--motion-fade-easing);
}
button.pk-item:hover {
  background: color-mix(in oklch, var(--text) 4%, transparent);
}
.pk-item[aria-pressed='true'] {
  border-color: var(--line-2);
  background: var(--surface-2);
}
.pk-item.off {
  opacity: 0.62;
}
.pk-mark {
  flex: none;
  display: grid;
  place-items: center;
  width: 36px;
  height: 36px;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-md);
  background: var(--tile-bg);
  font: var(--weight-medium) 11px / 1 var(--font-num);
  letter-spacing: 0.02em;
  color: var(--text-2);
}
.pk-mark img {
  width: 20px;
  height: 20px;
  object-fit: contain;
}
.pk-item[aria-pressed='true'] .pk-mark {
  border-color: var(--text-3);
  color: var(--text);
}
.pk-text {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}
.pk-name {
  font: var(--weight-medium) var(--type-body) / 1.3 var(--font-ui);
  color: var(--text);
}
.pk-about,
.pk-sub {
  font-size: var(--type-caption);
  line-height: 1.4;
  color: var(--text-3);
}
.pk-meta {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  margin-top: 3px;
}
.pk-feet {
  display: inline-flex;
  align-items: flex-end;
  gap: 4px;
}
/* A size as its footprint on the grid: one cell is 4px (8px in the size picker). */
.pk-foot {
  --c: 4px;
  --g: 1px;
  display: block;
  width: calc(var(--fw) * var(--c) + (var(--fw) - 1) * var(--g));
  height: calc(var(--fh) * var(--c) + (var(--fh) - 1) * var(--g));
  border-radius: 1.5px;
  background: color-mix(in oklch, var(--text-3) 55%, transparent);
}
.pk-item[aria-pressed='true'] .pk-foot {
  background: var(--text-2);
}
.pk-count {
  font: var(--weight-regular) var(--type-caption) / 1 var(--font-num);
  color: var(--text-3);
}
.pk-empty {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: var(--space-2);
  padding: var(--space-4) var(--space-3);
  font-size: var(--type-small);
  color: var(--text-3);
}
.pk-empty p {
  margin: 0;
}
.pk-waiting {
  margin-top: var(--space-3);
  border-top: var(--hairline) solid var(--line);
  padding-top: var(--space-2);
}
.pk-waiting summary {
  padding: var(--space-2) var(--space-3);
  font-size: var(--type-small);
  color: var(--text-3);
  cursor: pointer;
}

/* The chosen widget. */
.pk-detail {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  min-width: 0;
  max-height: min(64vh, 620px);
  overflow: auto;
  overscroll-behavior: contain;
}
.pk-detail-empty {
  justify-content: center;
  align-items: center;
  text-align: center;
}
.pk-title {
  margin: 0;
  font: var(--weight-semibold) var(--type-body) / 1.3 var(--font-ui);
}
.pk-lede {
  margin: 4px 0 0;
  font-size: var(--type-small);
  color: var(--text-3);
}
.pk-stage {
  container-type: inline-size;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--space-3);
  margin: 0;
  padding: var(--space-5) var(--space-4) var(--space-3);
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-xl);
  /* The board's grid, faintly: where the tile will sit. */
  background:
    radial-gradient(circle, color-mix(in oklch, var(--text) 14%, transparent) 1px, transparent 1.5px) 0 0 / 16px 16px,
    var(--bg);
}
.pk-preview {
  --u: min(72px, calc((100cqi - var(--space-4) * 2 - (var(--w) - 1) * var(--gutter)) / var(--w)));
  display: grid;
  width: calc(var(--w) * var(--u) + (var(--w) - 1) * var(--gutter));
  height: calc(var(--h) * var(--u) + (var(--h) - 1) * var(--gutter));
  transition:
    width var(--motion-move-duration) var(--motion-move-easing),
    height var(--motion-move-duration) var(--motion-move-easing);
}
.pk-preview > * {
  min-width: 0;
  min-height: 0;
}
.pk-stage figcaption {
  font: var(--weight-regular) var(--type-caption) / 1.2 var(--font-num);
  color: var(--text-3);
}

.pk-sizes,
.pk-targets {
  margin: 0;
  padding: 0;
  border: 0;
  min-width: 0;
}
.pk-sizes legend,
.pk-targets legend {
  margin-bottom: var(--space-2);
  font: var(--weight-medium) var(--type-small) / 1.2 var(--font-ui);
  color: var(--text-2);
}
.pk-size-row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}
.pk-size {
  position: relative;
  display: grid;
  grid-template-rows: 36px auto auto;
  justify-items: center;
  align-items: center;
  gap: 2px;
  min-width: 68px;
  padding: 8px 10px 7px;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-md);
  cursor: pointer;
  transition:
    border-color var(--motion-fade-duration) var(--motion-fade-easing),
    background-color var(--motion-fade-duration) var(--motion-fade-easing);
}
.pk-size input {
  position: absolute;
  inset: 0;
  margin: 0;
  opacity: 0;
  cursor: pointer;
}
.pk-size:hover {
  background: color-mix(in oklch, var(--text) 4%, transparent);
}
.pk-size:has(input:checked) {
  border-color: var(--text-2);
  background: var(--surface-2);
}
.pk-size:has(input:focus-visible) {
  box-shadow:
    0 0 0 2px var(--surface),
    0 0 0 4px var(--focus);
}
.pk-foot.lg {
  --c: 7px;
  --g: 1.5px;
}
.pk-size:has(input:checked) .pk-foot {
  background: var(--text);
}
.pk-size-name {
  font-size: var(--type-small);
  color: var(--text);
}
.pk-size-dim {
  font-size: var(--type-caption);
  color: var(--text-3);
}

.pk-target-q {
  width: 100%;
  min-height: var(--target-min);
  margin-bottom: var(--space-2);
  padding: 8px 10px;
  border: var(--hairline) solid var(--line-2);
  border-radius: var(--radius-md);
  background: var(--surface-2);
  color: var(--text);
}
.pk-target-list {
  display: flex;
  flex-direction: column;
  max-height: 232px;
  overflow: auto;
  overscroll-behavior: contain;
  border: var(--hairline) solid var(--line);
  border-radius: var(--radius-md);
}
.pk-target {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-height: var(--target-min);
  padding: 6px var(--space-3);
  font-size: var(--type-small);
  cursor: pointer;
}
.pk-target + .pk-target {
  border-top: var(--hairline) solid var(--line);
}
.pk-target:has(input:checked) {
  background: var(--surface-2);
}
.pk-target input {
  flex: none;
  margin: 0;
  accent-color: var(--text);
}
.pk-target-main {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text);
}
.pk-target-place {
  flex: none;
  color: var(--text-3);
}
.pk-target-taken {
  margin-left: auto;
  flex: none;
  font-size: var(--type-caption);
  color: var(--text-3);
}
.pk-target.taken {
  cursor: default;
  opacity: 0.6;
}
.pk-target-list .pk-hint {
  padding: var(--space-3);
}
.pk-hint {
  margin: 0;
  font-size: var(--type-small);
  color: var(--text-3);
}

.pk-foot-bar {
  align-items: center;
  padding-top: var(--space-4);
  border-top: var(--hairline) solid var(--line);
}
.pk-where {
  margin: 0 auto 0 0;
  min-width: 0;
  font-size: var(--type-caption);
  color: var(--text-3);
}
.pk-keys {
  display: inline-flex;
  gap: 2px;
}

@media (max-width: 720px) {
  .pk-cols {
    grid-template-columns: minmax(0, 1fr);
    overflow: auto;
  }
  .pk-list,
  .pk-detail {
    max-height: none;
    overflow: visible;
  }
  .pk-keys,
  .pk-where {
    display: none;
  }
}
@media (pointer: coarse) {
  .pk-keys,
  .pk-search .kbd {
    display: none;
  }
}
@media (prefers-reduced-motion: reduce) {
  .pk-preview {
    transition: none;
  }
}
</style>
