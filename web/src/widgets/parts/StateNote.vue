<script setup lang="ts">
/**
 * The one-line note for a state that isn't healthy: glyph plus words, and the
 * fix when there is one. Needs repair offers Reconnect to owners and admins
 * (the Repair in the Inbox, else the integration's settings) and tells
 * everyone else to ask one. In demo mode Reconnect runs the repair's own
 * action, which settles it in the Demo Home: there's no real token or API
 * to check. Permission-limited says "Ask an admin". Nothing when healthy.
 * A long line (an upstream's own health message) is cut to two lines and
 * opens whole in a dialog (ClampText); Reconnect stays below it, never cut.
 * A note the upstream explains on a page of its own (health.url) links there.
 */
import { computed } from 'vue';
import ClampText from '../../components/ClampText.vue';
import Glyph, { type GlyphKind } from '../../components/marks/Glyph.vue';
import { label } from '../../model/graph';
import { useLive } from '../../stores/live';
import { useUi } from '../../stores/ui';
import { canRepair, stateLine, type Subject } from '../subject';
import { useRole } from '../use';

const {
  subject,
  limited = false,
  lastKnown = false,
  line: override,
  title: titleProp,
} = defineProps<{
  subject?: Subject | undefined;
  /** Values the viewer's role can't read are withheld. */
  limited?: boolean;
  /** Say that the values on screen are last-known (needs repair). */
  lastKnown?: boolean;
  /** A widget-specific line instead of the subject's. */
  line?: { glyph: GlyphKind; text: string } | undefined;
  /** Whose note it is, for the full-text dialog; else the subject's name. */
  title?: string | undefined;
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
/** The words, with the last-known caveat when a repair offers Reconnect. */
const text = computed(() => {
  const l = line.value;
  if (!l) return '';
  return 'reconnect' in l && l.reconnect && lastKnown ? `${l.text} Values are last-known.` : l.text;
});
const href = computed(() => {
  const l = line.value;
  return l && 'href' in l && typeof l.href === 'string' ? l.href : undefined;
});
const title = computed(() => titleProp || (subject?.resource ? label(subject.resource) : '') || 'Note');

/** In the Demo Home, the repair's suggested action, when it can run now. */
const demoFix = computed(() => {
  const r = subject?.repair;
  return live.demo.value && r?.suggested && ui.canRun(r).ok ? r : undefined;
});

function reconnect(): void {
  const s = subject;
  if (!s) return;
  if (demoFix.value) return ui.act(demoFix.value);
  if (s.repair) return ui.openInbox(s.repair.id);
  const inst = s.integration?.id.replace(/^integration:/, '');
  if (inst) ui.openSettings('integrations', inst);
}
</script>

<template>
  <div v-if="line" :class="['note', 'w-note', `n-${line.glyph}`]">
    <Glyph :kind="line.glyph" />
    <span class="w-note-body">
      <ClampText :text="text" :title="title" :glyph="line.glyph" :href="href" />
      <span v-if="'reconnect' in line && line.reconnect">
        <button v-if="fixable" type="button" :title="demoFix ? 'Runs in the Demo Home' : undefined" @click.stop="reconnect">Reconnect</button>
        <template v-else>Ask an admin to reconnect it.</template>
      </span>
    </span>
  </div>
</template>
