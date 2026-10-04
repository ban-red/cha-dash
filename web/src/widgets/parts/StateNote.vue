<script setup lang="ts">
/**
 * The one-line note for a state that isn't healthy: glyph plus words, and the
 * fix when there is one. Needs repair offers Reconnect to owners and admins
 * (the Repair in the Inbox, else the integration's settings) and tells
 * everyone else to ask one. Permission-limited says "Ask an admin". Nothing
 * when healthy.
 */
import { computed } from 'vue';
import Glyph, { type GlyphKind } from '../../components/marks/Glyph.vue';
import { useLive } from '../../stores/live';
import { useUi } from '../../stores/ui';
import { canRepair, stateLine, type Subject } from '../subject';
import { useRole } from '../use';

const {
  subject,
  limited = false,
  lastKnown = false,
  line: override,
} = defineProps<{
  subject?: Subject | undefined;
  /** Values the viewer's role can't read are withheld. */
  limited?: boolean;
  /** Say that the values on screen are last-known (needs repair). */
  lastKnown?: boolean;
  /** A widget-specific line instead of the subject's. */
  line?: { glyph: GlyphKind; text: string } | undefined;
}>();

const live = useLive();
const ui = useUi();
const role = useRole();

const line = computed(() => {
  if (override) return override;
  const l = subject ? stateLine(subject, live.now.value) : undefined;
  if (l) return l;
  if (limited) return { glyph: 'lock' as const, text: 'Hidden for your role. Ask an admin.' };
  return undefined;
});
const fixable = computed(() => canRepair(role()));

function reconnect(): void {
  const s = subject;
  if (!s) return;
  if (s.repair) return ui.openInbox(s.repair.id);
  const inst = s.integration?.id.replace(/^integration:/, '');
  if (inst) ui.openSettings('integrations', inst);
}
</script>

<template>
  <div v-if="line" :class="['note', 'w-note', `n-${line.glyph}`]">
    <Glyph :kind="line.glyph" />
    <span>
      {{ line.text }}<template v-if="'reconnect' in line && line.reconnect">
        {{ lastKnown ? ' Values are last-known.' : '' }}
        <button v-if="fixable" type="button" @click.stop="reconnect">Reconnect</button>
        <template v-else>Ask an admin to reconnect it.</template>
      </template>
    </span>
  </div>
</template>
