<script setup lang="ts">
/**
 * The six built-in themes, remembered on this device. Each preview renders
 * inside its own `data-theme`, so it shows that theme's real tokens.
 */
import { nextTick, onBeforeUnmount, ref, watch } from 'vue';
import { navigate } from '../lib/router';
import { currentTheme, setTheme, THEMES, type ThemeId } from '../lib/theme';
import { permissions } from '../model/roles';
import { useSession } from '../stores/session';

const emit = defineEmits<{ signOut: [] }>();
const session = useSession();
const canSettings = () => permissions(session.state.user?.role).settings;
const open = ref(false);
const root = ref<HTMLElement | null>(null);
const first = ref<HTMLButtonElement[]>([]);

function pick(id: ThemeId) {
  setTheme(id);
}

function onDoc(e: Event) {
  if (root.value && !root.value.contains(e.target as Node)) open.value = false;
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') open.value = false;
}

watch(open, async (v) => {
  if (v) {
    document.addEventListener('pointerdown', onDoc);
    document.addEventListener('keydown', onKey);
    await nextTick();
    const i = THEMES.findIndex((t) => t.id === currentTheme.value);
    first.value[i]?.focus();
  } else {
    document.removeEventListener('pointerdown', onDoc);
    document.removeEventListener('keydown', onKey);
  }
});
onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onDoc);
  document.removeEventListener('keydown', onKey);
});

function onArrow(e: KeyboardEvent, i: number) {
  const step = e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : 0;
  if (!step) return;
  e.preventDefault();
  const next = (i + step + THEMES.length) % THEMES.length;
  pick(THEMES[next]!.id);
  first.value[next]?.focus();
}
</script>

<template>
  <div ref="root" class="theme-menu">
    <button type="button" class="tool" :aria-expanded="open" aria-haspopup="true" aria-label="Appearance" @click="open = !open">
      <svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="6" /><path d="M8 2v12M2.6 5.5l10.8 5M2.6 10.5l10.8-5" /></svg>
    </button>
    <div v-if="open" class="theme-pop glass">
      <h3 id="theme-h">Theme · this device</h3>
      <div class="theme-grid" role="radiogroup" aria-labelledby="theme-h">
        <button
          v-for="(t, i) in THEMES"
          :key="t.id"
          ref="first"
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
      <div class="theme-foot">
        <span>Signed in as {{ session.state.user?.username ?? '—' }}</span>
        <span class="theme-acts">
          <a v-if="canSettings()" class="btn sm ghost" href="/settings/integrations" @click.prevent="(open = false), navigate('/settings/integrations')">Settings</a>
          <button type="button" class="btn sm ghost" @click="emit('signOut')">Sign out</button>
        </span>
      </div>
    </div>
  </div>
</template>
