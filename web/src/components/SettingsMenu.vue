<script setup lang="ts">
/**
 * The masthead's settings menu: Theme, Add integration (admins), Settings and Sign out. Theme opens the
 * six built-in themes in the same popover, remembered on this device. Each
 * preview renders inside its own `data-theme`, so it shows that theme's real tokens.
 * The dropdown is a UiMenu.
 */
import { computed, ref, useTemplateRef } from 'vue';
import { navigate, settingsPath } from '../lib/router';
import { currentTheme, setTheme, THEMES, type ThemeId } from '../lib/theme';
import { permissions } from '../model/roles';
import { useSession } from '../stores/session';
import UiMenu from '../ui/Menu.vue';

const emit = defineEmits<{ signOut: [] }>();
const session = useSession();
const canSettings = () => permissions(session.state.user?.role).settings;
/** The first Settings pane this role has: Integrations for admins, Security (passkeys) for everyone else. */
const settingsHref = () => settingsPath(permissions(session.state.user?.role).panes[0] ?? 'security');
const canAdd = () => permissions(session.state.user?.role).integrations;
const addHref = settingsPath('integrations', 'new');
const who = computed(() => session.state.user?.username ?? '—');
const themeName = computed(() => THEMES.find((t) => t.id === currentTheme.value)?.name ?? '');

/** Which page of the popover shows: the menu, or the theme grid. */
const view = ref('menu');
const menu = useTemplateRef('menu');
const cards = ref<HTMLButtonElement[]>([]);

function close() {
  menu.value?.close();
}

function pick(id: ThemeId) {
  setTheme(id);
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
  <div class="settings-menu">
    <UiMenu ref="menu" v-model:view="view" class="tool" label="Settings" :pop-class="view === 'theme' ? 'theme-pop' : undefined">
      <template #trigger>
        <svg viewBox="0 0 16 16" aria-hidden="true">
          <path d="M2.5 4.5h1.4M7.1 4.5h6.4M2.5 11.5h6.4M12.1 11.5h1.4" />
          <circle cx="5.5" cy="4.5" r="1.6" /><circle cx="10.5" cy="11.5" r="1.6" />
        </svg>
      </template>
      <template #default="{ goto }">
        <template v-if="view === 'menu'">
          <!-- The menu's name says it too: the panel is in <body>, outside the page's landmarks. -->
          <p class="menu-who" aria-hidden="true">Signed in as {{ who }}</p>
          <div role="menu" :aria-label="`Settings, signed in as ${who}`">
            <button type="button" role="menuitem" class="menu-item" data-item="theme" tabindex="-1" @click="goto('theme')">
              <span>Theme</span>
              <span class="menu-val">{{ themeName }}<svg viewBox="0 0 16 16" aria-hidden="true"><path d="m6 3.5 4.5 4.5L6 12.5" /></svg></span>
            </button>
            <a v-if="canAdd()" role="menuitem" class="menu-item" tabindex="-1" :href="addHref" @click.prevent="close(), navigate(addHref)">
              <span>Add integration</span>
            </a>
            <a v-if="canSettings()" role="menuitem" class="menu-item" tabindex="-1" :href="settingsHref()" @click.prevent="close(), navigate(settingsHref())">
              <span>Settings</span>
            </a>
            <button type="button" role="menuitem" class="menu-item" tabindex="-1" @click="close(), emit('signOut')">
              <span>Sign out</span>
            </button>
          </div>
        </template>
        <template v-else>
          <div class="theme-head">
            <button type="button" class="tool back" aria-label="Back to settings menu" @click="goto('menu')">
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
              :data-focus="currentTheme === t.id || undefined"
              @click="pick(t.id)"
              @keydown="onArrow($event, i)"
            >
              <span class="pv" :data-theme="t.id" :data-surface="t.surface" aria-hidden="true">
                <i class="p-tile"></i><i class="p-t1"></i><i class="p-t2"></i><i class="p-a"></i><i class="p-d"></i>
              </span>
              <span class="nm">{{ t.name }}<small>{{ t.note }}</small></span>
            </button>
          </div>
        </template>
      </template>
    </UiMenu>
  </div>
</template>
