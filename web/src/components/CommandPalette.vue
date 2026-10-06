<script setup lang="ts">
/**
 * The command bar (⌘K, or /): search boards, resources, integrations and the
 * Inbox, and run page commands. Keyboard-first, with ARIA combobox semantics:
 * focus stays in the input, the active option is `aria-activedescendant`, and
 * arrows, Page Up/Down, Enter and Esc do what they say.
 *
 * Actions (the Run group): for the resource in focus (the tile that had focus
 * when it opened, or the top result once you type), what the hub would plan
 * on it (`GET /api/actions?target=`, filtered by your role) plus its open
 * incidents' actions; typing also finds any open incident's action by name.
 * Choosing one runs the normal safety ladder. Unavailable ones stay listed,
 * marked, with the hub's sentence.
 *
 * Its own chunk, loaded on first use, and mounted only while open.
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { listActions } from '../lib/actions';
import { navigate } from '../lib/router';
import { currentTheme, setTheme } from '../lib/theme';
import { label } from '../model/graph';
import { actionCommands, buildCommands, searchCommands, type ActionOffer, type Command } from '../model/commands';
import { integrations, loadInstances } from '../stores/integrations';
import { useBoard } from '../stores/board';
import { useLive } from '../stores/live';
import { useSession } from '../stores/session';
import { useUi } from '../stores/ui';

const live = useLive();
const ui = useUi();
const board = useBoard();
const session = useSession();

const query = ref('');
const active = ref(0);
const input = ref<HTMLInputElement | null>(null);
const list = ref<HTMLElement | null>(null);
let lastFocus: Element | null = null;

/** The resource of the tile that had focus when the bar opened, if any. */
const focused = ref<string | undefined>();
const offers = ref<{ target: string; list: ActionOffer[] }>({ target: '', list: [] });
const cache = new Map<string, ActionOffer[]>();
let pending: AbortController | undefined;
let timer: ReturnType<typeof setTimeout> | undefined;

const commands = computed(() =>
  buildCommands({
    graph: live.graph.value,
    inbox: live.inbox.value,
    boards: board.state.boards,
    currentBoard: board.state.boardId,
    instances: integrations.instances,
    editing: ui.state.editing,
    theme: currentTheme.value,
    ...(session.state.user ? { username: session.state.user.username } : {}),
    can: ui.can.value,
    do: {
      openInbox: (id) => ui.openInbox(id ?? ''),
      toggleEdit: () => (ui.state.editing = !ui.state.editing),
      addWidget: () => {
        ui.state.editing = true;
        ui.state.addingWidget = true;
      },
      openBoard: (id) => navigate(id === board.state.boards[0]?.id ? '/' : `/boards/${encodeURIComponent(id)}`),
      reveal: (id) => ui.reveal(id),
      openSettings: (pane, id) => ui.openSettings(pane, id),
      addIntegration: () => navigate('/settings/integrations/new'),
      setTheme,
      signOut: () => void ui.signOut(),
    },
  }),
);

/** The resource in focus: the top result once something is typed, else the focused tile's. */
const subject = computed<string | undefined>(() => {
  if (!query.value.trim()) return focused.value;
  const top = searchCommands(commands.value, query.value)[0]?.items[0];
  return top?.id.startsWith('resource:') ? top.id.slice('resource:'.length) : undefined;
});
const subjectName = computed(() => (subject.value ? label(live.graph.value?.resources[subject.value]) || subject.value : ''));

// What the hub would plan on the subject: asked once per resource, a moment after it settles.
watch(
  subject,
  (target) => {
    clearTimeout(timer);
    pending?.abort();
    if (!target || live.stale.value) return;
    const known = cache.get(target);
    if (known) {
      offers.value = { target, list: known };
      return;
    }
    timer = setTimeout(async () => {
      const ctrl = new AbortController();
      pending = ctrl;
      const list = await listActions(target, ctrl.signal);
      if (ctrl.signal.aborted) return;
      cache.set(target, list);
      if (subject.value === target) offers.value = { target, list };
    }, 120);
  },
  { immediate: true },
);

const actions = computed(() =>
  actionCommands({
    graph: live.graph.value,
    inbox: ui.can.value.inbox ? live.inbox.value : undefined,
    subject: subject.value,
    offers: offers.value.target === subject.value ? offers.value.list : [],
    onlySubject: !query.value.trim(),
    can: ui.can.value,
    act: (incident, action) => ui.act(incident, action),
    run: (o, target) => void ui.runAction({ action: o.id, target, label: o.label, tier: o.tier }),
    refuse: (why) => ui.toast(why, 'error'),
  }),
);
/** For screen readers: what the Run group now offers for the resource in focus. */
const announce = computed(() => {
  if (!subject.value || offers.value.target !== subject.value) return '';
  const n = actions.value.length;
  return n ? `${n} ${n === 1 ? 'action' : 'actions'} for ${subjectName.value}.` : '';
});

const sections = computed(() => searchCommands([...actions.value, ...commands.value], query.value));
const groupTitle = (g: string) => (g === 'Run' ? (subject.value && !query.value.trim() ? `Run on ${subjectName.value}` : 'Run') : g);
const flat = computed(() => sections.value.flatMap((s) => s.items));
const current = computed<Command | undefined>(() => flat.value[active.value]);
const optionId = (c: Command) => `po-${flat.value.indexOf(c)}`;
const activeId = computed(() => (current.value ? optionId(current.value) : undefined));

watch(query, () => (active.value = 0));
watch(active, async () => {
  await nextTick();
  const id = activeId.value;
  if (id) list.value?.querySelector(`#${id}`)?.scrollIntoView?.({ block: 'nearest' });
});

function move(step: number) {
  const n = flat.value.length;
  if (!n) return;
  active.value = (active.value + step + n) % n;
}

function run(c: Command | undefined) {
  if (!c) return;
  ui.closePalette();
  c.run();
}

/** The resource behind an element: its tile's item id is `<type>@<resource>`. */
function resourceOf(el: Element | null): string | undefined {
  const item = el instanceof HTMLElement ? el.closest<HTMLElement>('[data-item]')?.dataset['item'] : undefined;
  const at = item?.indexOf('@') ?? -1;
  return item && at > 0 ? item.slice(at + 1) : undefined;
}

function onKey(e: KeyboardEvent) {
  switch (e.key) {
    case 'ArrowDown':
      e.preventDefault();
      move(1);
      break;
    case 'ArrowUp':
      e.preventDefault();
      move(-1);
      break;
    case 'PageDown':
      e.preventDefault();
      active.value = Math.min(flat.value.length - 1, active.value + 5);
      break;
    case 'PageUp':
      e.preventDefault();
      active.value = Math.max(0, active.value - 5);
      break;
    case 'Home':
      if (e.ctrlKey || e.metaKey) {
        e.preventDefault();
        active.value = 0;
      }
      break;
    case 'End':
      if (e.ctrlKey || e.metaKey) {
        e.preventDefault();
        active.value = Math.max(0, flat.value.length - 1);
      }
      break;
    case 'Enter':
      if (e.isComposing) return;
      e.preventDefault();
      run(current.value);
      break;
    case 'Escape':
      e.preventDefault();
      e.stopPropagation();
      ui.closePalette();
      break;
    case 'Tab':
      e.preventDefault(); // the dialog has one control; keep focus in it
      break;
  }
}

onMounted(() => {
  lastFocus = document.activeElement;
  const r = resourceOf(lastFocus);
  if (r && live.graph.value?.resources[r]) focused.value = r;
  input.value?.focus();
  // Integrations are admin settings: below admin the hub answers 403, so don't ask.
  if (ui.can.value.integrations) void loadInstances().catch(() => undefined);
});
onBeforeUnmount(() => {
  clearTimeout(timer);
  pending?.abort();
  // Give focus back, unless the command moved it somewhere (a tile, a sheet).
  const now = document.activeElement;
  if ((!now || now === document.body || now === input.value) && lastFocus instanceof HTMLElement && lastFocus.isConnected) lastFocus.focus();
});
</script>

<template>
  <Teleport to="body">
    <div class="layer layer-palette">
      <div class="scrim palette-scrim" @click="ui.closePalette()"></div>
      <div class="palette glass" role="dialog" aria-modal="true" aria-label="Command bar">
        <div class="pq">
          <svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="7" cy="7" r="4.6" /><path d="m10.4 10.4 3.4 3.4" /></svg>
          <input
            ref="input"
            v-model="query"
            role="combobox"
            aria-expanded="true"
            aria-controls="palette-list"
            aria-autocomplete="list"
            aria-haspopup="listbox"
            :aria-activedescendant="activeId"
            aria-label="Search boards, services, hosts and commands"
            placeholder="Search boards, services, hosts, commands…"
            autocomplete="off"
            autocapitalize="off"
            spellcheck="false"
            enterkeyhint="go"
            @keydown="onKey"
          />
          <kbd class="kbd" aria-hidden="true">esc</kbd>
        </div>
        <div id="palette-list" ref="list" class="pl" role="listbox" aria-label="Results">
          <div v-for="sec in sections" :key="sec.group" role="group" :aria-labelledby="`pg-${sec.group}`">
            <div :id="`pg-${sec.group}`" class="grp" role="presentation">{{ groupTitle(sec.group) }}</div>
            <div
              v-for="c in sec.items"
              :id="optionId(c)"
              :key="c.id"
              role="option"
              :class="['opt', { off: c.disabled }]"
              :aria-selected="c === current"
              :aria-disabled="c.disabled ? 'true' : undefined"
              :title="c.disabled"
              @pointermove="active = flat.indexOf(c)"
              @click="run(c)"
            >
              <span class="ol">{{ c.label }}<span v-if="c.disabled" class="sr">. Unavailable: {{ c.disabled }}</span></span>
              <span v-if="c.hint" class="oh">{{ c.hint }}</span>
              <kbd v-if="c.kbd" class="kbd" aria-hidden="true">{{ c.kbd }}</kbd>
            </div>
          </div>
          <div v-if="!flat.length" class="none" role="presentation">No matches for “{{ query }}”. Try a host, a service or a command.</div>
        </div>
        <div class="sr" role="status" aria-live="polite">{{ announce }}</div>
        <div class="pfoot" aria-hidden="true">
          <span><kbd class="kbd">↑</kbd><kbd class="kbd">↓</kbd> move</span>
          <span><kbd class="kbd">↵</kbd> open</span>
          <span><kbd class="kbd">esc</kbd> close</span>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style>
.layer-palette {
  z-index: 60;
}
.palette-scrim {
  animation: palette-fade var(--motion-fade-duration) var(--motion-fade-easing);
}
.palette {
  position: absolute;
  left: 50%;
  top: 13vh;
  width: min(620px, calc(100% - 24px));
  overflow: hidden;
  border-radius: var(--radius-xl);
  transform: translateX(-50%);
  animation: palette-in var(--motion-press-duration) var(--motion-press-easing);
}
@keyframes palette-in {
  from {
    opacity: 0;
    transform: translateX(-50%) scale(0.98);
  }
}
@keyframes palette-fade {
  from {
    opacity: 0;
  }
}
@media (prefers-reduced-motion: reduce) {
  html:not([data-motion='full']) .palette {
    animation-name: palette-fade;
  }
}
.pq {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: 0 14px 0 18px;
  border-bottom: var(--hairline) solid var(--line);
}
.pq svg {
  width: 16px;
  height: 16px;
  flex: none;
  fill: none;
  stroke: var(--text-3);
  stroke-width: 1.6;
  stroke-linecap: round;
}
.pq input {
  flex: 1;
  min-width: 0;
  padding: 16px 0;
  border: 0;
  background: transparent;
  outline: none;
  font: var(--weight-medium) var(--type-callout) / 1.2 var(--font-ui);
  color: var(--text);
}
.pq input::placeholder {
  color: var(--text-3);
}
.pl {
  max-height: min(52vh, 460px);
  overflow: auto;
  overscroll-behavior: contain;
  padding: 6px;
}
.pl .grp {
  padding: 10px 10px 4px;
  font: var(--weight-medium) var(--type-micro) / 1 var(--font-num);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-3);
}
.pl .opt {
  display: flex;
  align-items: center;
  gap: 10px;
  min-height: 36px;
  padding: 8px 10px;
  border-radius: var(--radius-md);
  font-size: var(--type-body);
  color: var(--text-2);
  cursor: pointer;
}
.pl .opt[aria-selected='true'] {
  background: color-mix(in oklch, var(--text) 9%, transparent);
  color: var(--text);
}
.pl .opt.off {
  cursor: default;
}
.pl .opt.off .ol {
  color: var(--text-3);
}
.pl .opt.off[aria-selected='true'] .ol {
  color: var(--text-2);
}
.pl .ol {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pl .oh {
  margin-left: auto;
  flex: none;
  max-width: 45%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font: var(--weight-regular) var(--type-caption) / 1.3 var(--font-num);
  color: var(--text-3);
}
.pl .opt .kbd {
  flex: none;
}
.pl .oh + .kbd {
  margin-left: 0;
}
.pl .ol + .kbd {
  margin-left: auto;
}
.pl .none {
  padding: 18px 12px;
  font-size: var(--type-small);
  color: var(--text-3);
}
.pfoot {
  display: flex;
  gap: var(--space-4);
  padding: 8px 16px;
  border-top: var(--hairline) solid var(--line);
  font-size: var(--type-caption);
  color: var(--text-3);
}
.pfoot span {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
@media (max-width: 639px) {
  .palette {
    top: max(12px, env(safe-area-inset-top));
  }
  .pfoot {
    display: none;
  }
}
</style>
