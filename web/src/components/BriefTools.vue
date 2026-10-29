<script setup lang="ts">
/**
 * The Briefing's button row: Inbox, search, Edit layout and Settings.
 * It sticks to the top right of the page once you scroll past it. The slot
 * keeps the row's place in the flow, so nothing jumps when it lets go.
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';
import SettingsMenu from './SettingsMenu.vue';
import { groupInbox } from '../model/inbox';
import { useDiscoveryIfAny } from '../stores/discovery';
import { useLive } from '../stores/live';
import { useUi } from '../stores/ui';

const emit = defineEmits<{ signOut: [] }>();
const live = useLive();
const ui = useUi();
const discovery = useDiscoveryIfAny();

/** What the Inbox lists as open: needs you, not already being fixed, plus a discovery and suggestions waiting for an answer. */
const inboxCount = computed(() => {
  const g = groupInbox(live.inbox.value, live.fixes);
  return g.open.length + g.suggestions.length + (discovery?.suggestions.value.length ? 1 : 0);
});
const mac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

const TOP = 12;
const slot = ref<HTMLElement>();
const stuck = ref(false);
const box = ref({ w: 0, h: 0, right: 0 });
let frame = 0;

function measure() {
  frame = 0;
  const el = slot.value;
  // Hidden in the other briefing style: nothing to stick.
  if (!el || el.offsetParent === null) {
    stuck.value = false;
    return;
  }
  const r = el.getBoundingClientRect();
  const page = el.closest('.page')?.getBoundingClientRect();
  const past = r.top < TOP;
  if (past && !stuck.value) box.value = { w: r.width, h: r.height, right: 0 };
  if (past && page) box.value.right = document.documentElement.clientWidth - page.right;
  stuck.value = past;
}
function schedule() {
  if (!frame) frame = requestAnimationFrame(measure);
}
onMounted(() => {
  addEventListener('scroll', schedule, { passive: true });
  addEventListener('resize', schedule);
  measure();
});
onBeforeUnmount(() => {
  removeEventListener('scroll', schedule);
  removeEventListener('resize', schedule);
  if (frame) cancelAnimationFrame(frame);
});
</script>

<template>
  <div ref="slot" class="brief-tools-slot" :style="stuck ? { width: `${box.w}px`, height: `${box.h}px` } : undefined">
    <div :class="['brief-tools', { stuck }]" :style="stuck ? { right: `${box.right}px` } : undefined">
      <button v-if="ui.can.value.inbox" type="button" class="pill" aria-haspopup="dialog" :aria-label="`Inbox: ${inboxCount} need you`" @click="ui.openInbox()">
        Inbox <span :class="['badge', { zero: !inboxCount }]">{{ inboxCount }}</span>
      </button>
      <button type="button" class="tool search-tool" aria-haspopup="dialog" :aria-keyshortcuts="mac ? 'Meta+K' : 'Control+K'" aria-label="Search and commands" :title="`Search and commands (${mac ? '⌘K' : 'Ctrl K'})`" @click="ui.openPalette()">
        <svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="7" cy="7" r="4.6" /><path d="m10.4 10.4 3.4 3.4" /></svg>
      </button>
      <button v-if="ui.can.value.editBoards" type="button" class="tool edit-tool" :aria-pressed="ui.state.editing" aria-label="Edit layout" title="Edit layout" @click="ui.state.editing = !ui.state.editing">
        <svg viewBox="0 0 16 16" aria-hidden="true"><rect x="2.5" y="2.5" width="4.5" height="6" rx="1" /><rect x="9" y="2.5" width="4.5" height="3" rx="1" /><rect x="9" y="7.5" width="4.5" height="6" rx="1" /><rect x="2.5" y="10.5" width="4.5" height="3" rx="1" /></svg>
      </button>
      <SettingsMenu @sign-out="emit('signOut')" />
    </div>
  </div>
</template>
