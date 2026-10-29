<script setup lang="ts">
/**
 * The masthead's settings menu: Theme, Settings and Sign out. Theme opens the
 * six built-in themes in the same popover, remembered on this device. Each
 * preview renders inside its own `data-theme`, so it shows that theme's real tokens.
 */
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue';
import { navigate, settingsPath } from '../lib/router';
import { currentTheme, setTheme, THEMES, type ThemeId } from '../lib/theme';
import { permissions } from '../model/roles';
import { useSession } from '../stores/session';

const emit = defineEmits<{ signOut: [] }>();
const session = useSession();
const canSettings = () => permissions(session.state.user?.role).settings;
/** The first Settings pane this role has: Integrations for admins, Security (passkeys) for everyone else. */
const settingsHref = () => settingsPath(permissions(session.state.user?.role).panes[0] ?? 'security');
const themeName = computed(() => THEMES.find((t) => t.id === currentTheme.value)?.name ?? '');

const open = ref(false);
/** Which page of the popover shows: the menu, or the theme grid. */
const view = ref<'menu' | 'theme'>('menu');
const root = ref<HTMLElement | null>(null);
const trigger = ref<HTMLButtonElement | null>(null);
const items = ref<HTMLElement | null>(null);
const cards = ref<HTMLButtonElement[]>([]);

function menuItems(): HTMLElement[] {
  return [...(items.value?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [])];
}

async function showMenu(focus: 'first' | 'theme' = 'first') {
  view.value = 'menu';
  await nextTick();
  const all = menuItems();
  (focus === 'theme' ? all.find((el) => el.dataset.item === 'theme') : all[0])?.focus();
}
async function showTheme() {
  view.value = 'theme';
  await nextTick();
  const i = THEMES.findIndex((t) => t.id === currentTheme.value);
  cards.value[i]?.focus();
}
function close(refocus = false) {
  open.value = false;
  if (refocus) trigger.value?.focus();
}

function pick(id: ThemeId) {
  setTheme(id);
}

function onDoc(e: Event) {
  if (root.value && !root.value.contains(e.target as Node)) close();
}
function onKey(e: KeyboardEvent) {
  if (e.key !== 'Escape') return;
  // Escape steps back from the theme grid first, then closes.
  if (view.value === 'theme') void showMenu('theme');
  else close(true);
}

watch(open, (v) => {
  if (v) {
    document.addEventListener('pointerdown', onDoc);
    document.addEventListener('keydown', onKey);
    void showMenu();
  } else {
    document.removeEventListener('pointerdown', onDoc);
    document.removeEventListener('keydown', onKey);
  }
});
onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onDoc);
  document.removeEventListener('keydown', onKey);
});

function onMenuKey(e: KeyboardEvent) {
  const all = menuItems();
  const i = all.indexOf(document.activeElement as HTMLElement);
  let next = -1;
  if (e.key === 'ArrowDown') next = (i + 1) % all.length;
  else if (e.key === 'ArrowUp') next = (i - 1 + all.length) % all.length;
  else if (e.key === 'Home') next = 0;
  else if (e.key === 'End') next = all.length - 1;
  else if (e.key === 'Tab') close();
  if (next < 0) return;
  e.preventDefault();
  all[next]?.focus();
}

function onArrow(e: KeyboardEvent, i: number) {
  const step = e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : 0;
  if (!step) return;
  e.preventDefault();
  const next = (i + step + THEMES.length) % THEMES.length;
  pick(THEMES[next]!.id);
  cards.value[next]?.focus();
}
</script>

<template>
  <div ref="root" class="settings-menu">
    <button ref="trigger" type="button" class="tool" :aria-expanded="open" aria-haspopup="menu" aria-label="Settings" title="Settings" @click="open = !open">
      <svg viewBox="0 0 16 16" aria-hidden="true">
        <path d="M2.5 4.5h1.4M7.1 4.5h6.4M2.5 11.5h6.4M12.1 11.5h1.4" />
        <circle cx="5.5" cy="4.5" r="1.6" /><circle cx="10.5" cy="11.5" r="1.6" />
      </svg>
    </button>
    <div v-if="open && view === 'menu'" class="settings-pop menu-pop glass">
      <p class="menu-who">Signed in as {{ session.state.user?.username ?? '—' }}</p>
      <div ref="items" role="menu" aria-label="Settings" @keydown="onMenuKey">
        <button type="button" role="menuitem" class="menu-item" data-item="theme" tabindex="-1" @click="showTheme">
          <span>Theme</span>
          <span class="menu-val">{{ themeName }}<svg viewBox="0 0 16 16" aria-hidden="true"><path d="m6 3.5 4.5 4.5L6 12.5" /></svg></span>
        </button>
        <a v-if="canSettings()" role="menuitem" class="menu-item" tabindex="-1" :href="settingsHref()" @click.prevent="close(), navigate(settingsHref())">
          <span>Settings</span>
        </a>
        <button type="button" role="menuitem" class="menu-item" tabindex="-1" @click="close(), emit('signOut')">
          <span>Sign out</span>
        </button>
      </div>
    </div>
    <div v-else-if="open" class="settings-pop theme-pop glass">
      <div class="theme-head">
        <button type="button" class="tool back" aria-label="Back to settings menu" @click="showMenu('theme')">
          <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M10 3.5 5.5 8l4.5 4.5" /></svg>
        </button>
        <p id="theme-h" class="theme-title">Theme · this device</p>
      </div>
      <div class="theme-grid" role="radiogroup" aria-labelledby="theme-h">
        <button
          v-for="(t, i) in THEMES"
          :key="t.id"
          ref="cards"
          type="button"
          role="radio"
          class="th-card"
          :aria-checked="currentTheme === t.id"
          :tabindex="currentTheme === t.id ? 0 : -1"
          @click="pick(t.id)"
          @keydown="onArrow($event, i)"
        >
          <span class="pv" :data-theme="t.id" :data-surface="t.surface" aria-hidden="true">
            <i class="p-tile"></i><i class="p-t1"></i><i class="p-t2"></i><i class="p-a"></i><i class="p-d"></i>
          </span>
          <span class="nm">{{ t.name }}<small>{{ t.note }}</small></span>
        </button>
      </div>
    </div>
  </div>
</template>
